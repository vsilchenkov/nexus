// Package usecase — бизнес-логика Receiver Service.
//
// RouteSyncUsecase — центральная функция /v1/request/*: лукап узла,
// проверка авторизации, резолв URL, сборка outgoing-заголовков,
// вызов Sender по gRPC, маппинг ответа.
package usecase

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/receiver/usecase/port"
	senderv1 "nexus/proto/sender/v1"
)

// SenderClient — интерфейс, который реализует grpcsender.Client.
// Объявлен здесь, чтобы usecase не зависел от конкретного adapter'а
// (см. §17.2 — accept interfaces, return structs).
type SenderClient interface {
	Send(ctx context.Context, req *senderv1.SendRequest) (*senderv1.SendResponse, error)
}

// RouteInput — параметры входящего sync-запроса.
type RouteInput struct {
	// TeamSlug — slug команды, под которую был адресован запрос. Из URL
	// /v1/request/<team_slug>/<path> (Phase 10.E.1). Пустая строка =
	// legacy URL без слога; NodeReader подставит DefaultTeamSlug.
	TeamSlug string
	NodePath string
	Method   string
	Header   http.Header
	Query    url.Values
	Body     []byte
	ClientIP string
	// RequireCallback включается для POST /v1/callback/{path} (§16 ТЗ).
	// RouteAsync отклоняет такой запрос, если у узла IncomingAuthType !=
	// webhook_signature — чтобы случайный клиент не пробрасывал произвольное
	// тело через callback-маршрут на узел с обычной авторизацией.
	RequireCallback bool
}

// RouteOutput — что Receiver вернёт клиенту.
type RouteOutput struct {
	StatusCode int
	Headers    map[string]string
	Body       []byte
}

// RouteUsecase — sync-роутер.
type RouteUsecase struct {
	nodes   port.NodeReader
	sender  SenderClient
	maxHops int
	logger  logging.Logger
}

// NewRouteUsecase создаёт sync-роутер. maxHops — лимит переходов запроса через
// шину (§32, X-Nexus-Hops); <= 0 выключает защиту от зацикливания.
func NewRouteUsecase(nodes port.NodeReader, sender SenderClient, maxHops int, logger logging.Logger) *RouteUsecase {
	return &RouteUsecase{nodes: nodes, sender: sender, maxHops: maxHops, logger: logger}
}

// Route — sync-обработка (POST /v1/request/{path}).
func (u *RouteUsecase) Route(ctx context.Context, in RouteInput) (*RouteOutput, error) {
	// §32: защита от зацикливания. Считаем переходы запроса через шину по
	// служебному заголовку X-Nexus-Hops; превышение лимита — петля.
	hop, loop := nextHop(in.Header, u.maxHops)
	if loop {
		return nil, domain.ErrLoopDetected
	}

	node, err := resolveNode(ctx, u.nodes, in.TeamSlug, in.NodePath)
	if err != nil {
		return nil, err
	}

	switch node.Status {
	case domain.NodeStatusDisabled:
		return nil, domain.ErrNodeNotFound
	case domain.NodeStatusPaused:
		// §3.6: sync на paused-узел превращается в async — handler
		// переключается на RouteAsync и отвечает 202 + queued:true.
		return nil, domain.ErrNodePaused
	}

	if node.RootMethod != domain.RootMethodRequest {
		return nil, fmt.Errorf("%w: node is %s, not request", domain.ErrNodeNotFound, node.RootMethod)
	}

	// §3.2 (#5): узел принимает только сконфигурированный входящий метод.
	if !methodMatches(in.Method, node.IncomingMethod) {
		return nil, domain.ErrNodeMethodNotAllowed
	}

	if err := CheckIncomingAuth(node, in.Header, in.Body); err != nil {
		return nil, err
	}

	// Динамическая авторизация: извлекаем и удаляем служебные значения
	// из query/headers/body ДО ResolveURL и pickForwardHeaders, чтобы
	// очищенные данные ушли внешнему узлу (§3.5 «Исключение»).
	effHeader := in.Header
	effQuery := in.Query
	effBody := in.Body
	var authHeader string

	if node.AuthType.IsDynamic() {
		dyn, derr := BuildDynamicOutgoingAuth(node, in.Header, in.Query, in.Body)
		if derr != nil {
			return nil, derr
		}
		authHeader = dyn.Header
		effHeader = dyn.Headers
		effQuery = dyn.Query
		effBody = dyn.Body
	} else {
		authHeader, err = BuildOutgoingAuth(node)
		if err != nil {
			return nil, err
		}
	}

	targetURL, cleanQuery, err := ResolveURL(node, effQuery)
	if err != nil {
		return nil, err
	}
	finalURL := appendQuery(targetURL, cleanQuery)

	headers := pickForwardHeaders(effHeader, node.ForwardHeaders)
	if ct := effHeader.Get("Content-Type"); ct != "" {
		headers["Content-Type"] = ct
	}
	// §32: служебный hop-счётчик в обход allowlist узла.
	if u.maxHops > 0 {
		headers[HeaderHops] = strconv.Itoa(hop)
	}

	id := uuid.NewString()
	resp, err := u.sender.Send(ctx, &senderv1.SendRequest{
		Id:                 id,
		NodePath:           node.Path,
		TargetUrl:          finalURL,
		Method:             string(node.OutgoingMethod),
		Auth:               &senderv1.AuthConfig{AuthorizationHeader: authHeader},
		Headers:            headers,
		Body:               effBody,
		TimeoutMs:          node.TimeoutMs,
		RetryCount:         node.RetryCount,
		RetryBackoffMs:     node.RetryBackoffMs,
		ClickhouseTable:    node.ClickHouseTable,
		LogRequestBody:     node.LogRequestBody,
		LogResponseBody:    node.LogResponseBody,
		LogHeaders:         node.LogHeaders,
		ClientIp:           in.ClientIP,
		LoggingEnabled:     node.LoggingEnabled,
		MaxBodySizeEnabled: node.MaxBodySizeEnabled,
		MaxBodySize:        node.MaxBodySize,
	})
	if err != nil {
		return nil, fmt.Errorf("sender.Send: %w", err)
	}

	out := &RouteOutput{
		StatusCode: int(resp.GetStatusCode()),
		Headers:    resp.GetHeaders(),
		Body:       resp.GetBody(),
	}
	if out.StatusCode == 0 {
		// Sender не получил ответ от внешнего узла (timeout, dns, conn refused).
		out.StatusCode = http.StatusBadGateway
		if errors.Is(err, context.DeadlineExceeded) {
			out.StatusCode = http.StatusGatewayTimeout
		}
		if out.Body == nil {
			out.Body = []byte(`{"error":"upstream unavailable"}`)
		}
	}
	return out, nil
}

// methodMatches сравнивает фактический HTTP-метод входящего запроса с
// сконфигурированным методом узла (§3.2, #5), без учёта регистра. Пустой
// want трактуется как POST (дефолт), чтобы узлы, созданные до миграции 0015 и
// переживший её L1/Redis-кеш без поля, не отклоняли трафик.
func methodMatches(got string, want domain.HTTPMethod) bool {
	if want == "" {
		want = domain.HTTPMethodPOST
	}
	return strings.EqualFold(got, string(want))
}

func appendQuery(target string, q url.Values) string {
	if len(q) == 0 {
		return target
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return target
	}
	existing := parsed.Query()
	for k, vv := range q {
		for _, v := range vv {
			existing.Add(k, v)
		}
	}
	parsed.RawQuery = existing.Encode()
	return parsed.String()
}

// pickForwardHeaders собирает map по списку имён (case-insensitive).
func pickForwardHeaders(in http.Header, names []string) map[string]string {
	if len(names) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(names))
	for _, name := range names {
		v := in.Get(name)
		if v != "" {
			out[http.CanonicalHeaderKey(name)] = v
		}
	}
	return out
}
