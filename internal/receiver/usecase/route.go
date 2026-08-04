// Package usecase — бизнес-логика Receiver Service.
//
// RouteSyncUsecase — центральная функция /v1/request/*: лукап узла,
// проверка авторизации, резолв URL, сборка outgoing-заголовков,
// вызов Sender по gRPC, маппинг ответа.
package usecase

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

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

	node, remainder, err := resolveNode(ctx, u.nodes, in.TeamSlug, in.NodePath)
	if err != nil {
		return nil, err
	}
	// §51.9: маршрутизация невосстановима постфактум — на debug видно, какой
	// узел выбран и какой хвост пути уйдёт дальше.
	u.logger.Debug("route: node resolved",
		u.logger.Str("team", in.TeamSlug),
		u.logger.Str("path", in.NodePath),
		u.logger.Str("node", node.Path),
		u.logger.Str("node_id", node.ID),
		u.logger.Str("remainder", remainder),
		u.logger.Str("status", string(node.Status)),
		u.logger.Int("hop", hop))

	if err := checkNodeAcceptsSync(node, in); err != nil {
		return nil, err
	}

	req, err := u.buildSendRequest(node, in, remainder, hop)
	if err != nil {
		return nil, err
	}
	return u.callSender(ctx, node, req)
}

// NodeRootMethod сообщает корневой метод узла, адресованного коротким URL
// §78.1 (`/api/v1/<team_slug>/<node_path>` — без сегмента request/requestAsync).
// Синхронность узла — свойство конфигурации, а не запроса, поэтому handler
// сначала спрашивает её здесь, а затем ведёт запрос в ту же ветку, что и
// legacy-URL.
//
// Отключённый узел неотличим от несуществующего (ErrNodeNotFound): факт
// существования наружу не раскрываем. Статус paused здесь НЕ обрабатывается —
// его разбирает выбранная ветка (§3.6: sync на paused → async).
//
// Резолв тот же и с тем же порядком интерпретаций, что у Route/RouteAsync,
// поэтому повторный вызов внутри выбранной ветки бьёт в L1/Redis-кеш узла.
func (u *RouteUsecase) NodeRootMethod(ctx context.Context, teamSlug, nodePath string) (domain.RootMethod, error) {
	node, _, err := resolveNode(ctx, u.nodes, teamSlug, nodePath)
	if err != nil {
		return "", err
	}
	if node.Status == domain.NodeStatusDisabled {
		return "", domain.ErrNodeNotFound
	}
	u.logger.Debug("route: root method resolved for short url",
		u.logger.Str("team", teamSlug),
		u.logger.Str("path", nodePath),
		u.logger.Str("node", node.Path),
		u.logger.Str("node_id", node.ID),
		u.logger.Str("root_method", string(node.RootMethod)),
		u.logger.Str("status", string(node.Status)))
	return node.RootMethod, nil
}

// checkNodeAcceptsSync — допуск запроса к узлу: статус, тип узла, входящий
// метод и входящая авторизация. Отдельно от сборки запроса: здесь только
// «пускать или нет», без единого побочного эффекта.
func checkNodeAcceptsSync(node *domain.Node, in RouteInput) error {
	switch node.Status {
	case domain.NodeStatusDisabled:
		return domain.ErrNodeNotFound
	case domain.NodeStatusPaused:
		// §3.6: sync на paused-узел превращается в async — handler
		// переключается на RouteAsync и отвечает 202 + queued:true.
		return domain.ErrNodePaused
	}

	if node.RootMethod != domain.RootMethodRequest {
		return fmt.Errorf("%w: node is %s, not request", domain.ErrNodeNotFound, node.RootMethod)
	}

	// §3.2 (#5): узел принимает только сконфигурированный входящий метод.
	if !MethodMatches(in.Method, node.IncomingMethod) {
		return domain.ErrNodeMethodNotAllowed
	}

	return CheckIncomingAuth(node, in.Header, in.Query, in.Body)
}

// buildSendRequest собирает gRPC-запрос к Sender: исходящая авторизация,
// целевой URL с хвостом path-passthrough и query, forward-заголовки.
func (u *RouteUsecase) buildSendRequest(
	node *domain.Node,
	in RouteInput,
	remainder string,
	hop int,
) (*senderv1.SendRequest, error) {
	// Динамическая авторизация: извлекаем и удаляем служебные значения
	// из query/headers/body ДО ResolveURL и pickForwardHeaders, чтобы
	// очищенные данные ушли внешнему узлу (§3.5 «Исключение»).
	effHeader := in.Header
	effQuery := in.Query
	effBody := in.Body
	var authHeader string

	if node.AuthType.IsDynamic() {
		dyn, err := BuildDynamicOutgoingAuth(node, in.Header, in.Query, in.Body)
		if err != nil {
			return nil, err
		}
		authHeader = dyn.Header
		effHeader = dyn.Headers
		effQuery = dyn.Query
		effBody = dyn.Body
	} else {
		var err error
		authHeader, err = BuildOutgoingAuth(node)
		if err != nil {
			return nil, err
		}
	}

	targetURL, cleanQuery, err := ResolveURL(node, effQuery)
	if err != nil {
		return nil, err
	}
	// §39: при path-passthrough приклеиваем хвост входящего пути к целевому URL.
	targetURL = AppendPathSuffix(targetURL, remainder)
	finalURL := appendQuery(targetURL, cleanQuery)

	headers := pickForwardHeaders(effHeader, node.ForwardHeaders)
	if ct := effHeader.Get("Content-Type"); ct != "" {
		headers["Content-Type"] = ct
	}
	// §32: служебный hop-счётчик в обход allowlist узла.
	if u.maxHops > 0 {
		headers[HeaderHops] = strconv.Itoa(hop)
	}

	return &senderv1.SendRequest{
		Id:                 uuid.NewString(),
		NodePath:           node.Path,
		NodeId:             node.ID,
		TargetUrl:          finalURL,
		Method:             EffectiveOutgoingMethod(node, in.Method),
		RequestPath:        remainder,
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
	}, nil
}

// callSender выполняет вызов и превращает ответ Sender'а в ответ клиенту.
func (u *RouteUsecase) callSender(
	ctx context.Context,
	node *domain.Node,
	req *senderv1.SendRequest,
) (*RouteOutput, error) {
	// §51.9: параметры исходящего вызова (URL без query — там могут быть
	// токены; тело/заголовки не логируем).
	u.logger.Debug("route: forwarding to sender",
		u.logger.Str("id", req.GetId()),
		u.logger.Str("node", node.Path),
		u.logger.Str("method", req.GetMethod()),
		u.logger.Str("target", redactURLString(req.GetTargetUrl())),
		u.logger.Int("body_len", len(req.GetBody())),
		u.logger.Int("timeout_ms", int(node.TimeoutMs)),
		u.logger.Int("retry_count", int(node.RetryCount)))

	start := time.Now()
	resp, err := u.sender.Send(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("sender.Send: %w", err)
	}
	// §51.9: итог вызова — статус, попытки и длительности (внешняя + полная).
	u.logger.Debug("route: sender responded",
		u.logger.Str("id", req.GetId()),
		u.logger.Str("node", node.Path),
		u.logger.Int("status", int(resp.GetStatusCode())),
		u.logger.Int("attempts", int(resp.GetAttempts())),
		u.logger.Int("upstream_ms", int(resp.GetDurationMs())),
		u.logger.Int("total_ms", int(time.Since(start).Milliseconds())))

	out := &RouteOutput{
		StatusCode: int(resp.GetStatusCode()),
		Headers:    resp.GetHeaders(),
		Body:       resp.GetBody(),
	}
	if out.StatusCode == 0 {
		// Sender не получил ответ от внешнего узла (timeout, dns, conn refused).
		// 504 отдаём именно на таймаут: для клиента это повод повторить, а отказ
		// соединения — нет. Признак приходит от Sender'а полем timeout, а не
		// разбором текста ошибки (proto SendResponse.timeout).
		out.StatusCode = http.StatusBadGateway
		if resp.GetTimeout() {
			out.StatusCode = http.StatusGatewayTimeout
		}
		if out.Body == nil {
			out.Body = []byte(`{"error":"upstream unavailable"}`)
		}
	}
	return out, nil
}

// redactURLString отрезает query/fragment от URL для логов (§51.9): в query
// могут быть токены динамической авторизации. Невалидный URL — как есть без
// части после «?».
func redactURLString(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		if before, _, ok := strings.Cut(raw, "?"); ok {
			return before
		}
		return raw
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// methodMatches сравнивает фактический HTTP-метод входящего запроса с
// сконфигурированным методом узла (§3.2, #5), без учёта регистра. Пустой
// want трактуется как POST (дефолт), чтобы узлы, созданные до миграции 0015 и
// переживший её L1/Redis-кеш без поля, не отклоняли трафик.
func MethodMatches(got string, want domain.HTTPMethod) bool {
	// §40: ANY — узел принимает запрос с любым входящим методом.
	if want == domain.HTTPMethodAny {
		return true
	}
	if want == "" {
		want = domain.HTTPMethodPOST
	}
	return strings.EqualFold(got, string(want))
}

// effectiveOutgoingMethod вычисляет HTTP-метод исходящего вызова получателя.
// §40: OutgoingMethod=ANY → зеркалит метод входящего запроса (incomingMethod);
// пустой incoming → POST. Иначе — сконфигурированный метод узла (пустой → POST,
// как трактует БД-дефолт и старое поведение).
func EffectiveOutgoingMethod(node *domain.Node, incomingMethod string) string {
	if node.OutgoingMethod == domain.HTTPMethodAny {
		if m := strings.ToUpper(strings.TrimSpace(incomingMethod)); m != "" {
			return m
		}
		return string(domain.HTTPMethodPOST)
	}
	if node.OutgoingMethod == "" {
		return string(domain.HTTPMethodPOST)
	}
	return string(node.OutgoingMethod)
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
