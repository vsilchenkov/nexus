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

	"github.com/google/uuid"

	"bus/internal/domain"
	"bus/internal/platform/logging"
	"bus/internal/receiver/usecase/port"
	senderv1 "bus/proto/sender/v1"
)

// SenderClient — интерфейс, который реализует grpcsender.Client.
// Объявлен здесь, чтобы usecase не зависел от конкретного adapter'а
// (см. §17.2 — accept interfaces, return structs).
type SenderClient interface {
	Send(ctx context.Context, req *senderv1.SendRequest) (*senderv1.SendResponse, error)
}

// RouteInput — параметры входящего sync-запроса.
type RouteInput struct {
	NodePath    string
	Method      string
	Header      http.Header
	Query       url.Values
	Body        []byte
	ClientIP    string
}

// RouteOutput — что Receiver вернёт клиенту.
type RouteOutput struct {
	StatusCode int
	Headers    map[string]string
	Body       []byte
}

// RouteUsecase — sync-роутер.
type RouteUsecase struct {
	nodes  port.NodeReader
	sender SenderClient
	logger logging.Logger
}

func NewRouteUsecase(nodes port.NodeReader, sender SenderClient, logger logging.Logger) *RouteUsecase {
	return &RouteUsecase{nodes: nodes, sender: sender, logger: logger}
}

// Route — sync-обработка (POST /v1/request/{path}).
func (u *RouteUsecase) Route(ctx context.Context, in RouteInput) (*RouteOutput, error) {
	node, err := u.nodes.GetByPath(ctx, in.NodePath)
	if err != nil {
		return nil, err
	}

	switch node.Status {
	case domain.NodeStatusDisabled:
		return nil, domain.ErrNodeNotFound
	case domain.NodeStatusPaused:
		// В Phase 1 синхронный paused = 503 (накопление в Kafka — Phase 2).
		return nil, fmt.Errorf("%w: node paused", domain.ErrNodeDisabled)
	}

	if node.RootMethod != domain.RootMethodRequest {
		return nil, fmt.Errorf("%w: node is %s, not request", domain.ErrNodeNotFound, node.RootMethod)
	}

	if err := CheckIncomingAuth(node, in.Header); err != nil {
		return nil, err
	}

	targetURL, cleanQuery, err := ResolveURL(node, in.Query)
	if err != nil {
		return nil, err
	}
	finalURL := appendQuery(targetURL, cleanQuery)

	// Outgoing auth: для AuthType=none/basic/token используем BuildOutgoingAuth.
	// Динамические режимы — Phase 1.9.
	var authHeader string
	if node.AuthType.IsDynamic() {
		authHeader, err = BuildDynamicOutgoingAuth(node, in.Header, in.Query, in.Body)
	} else {
		authHeader, err = BuildOutgoingAuth(node)
	}
	if err != nil {
		return nil, err
	}

	headers := pickForwardHeaders(in.Header, node.ForwardHeaders)
	if ct := in.Header.Get("Content-Type"); ct != "" {
		headers["Content-Type"] = ct
	}

	id := uuid.NewString()
	resp, err := u.sender.Send(ctx, &senderv1.SendRequest{
		Id:               id,
		NodePath:         node.Path,
		TargetUrl:        finalURL,
		Method:           in.Method,
		Auth:             &senderv1.AuthConfig{AuthorizationHeader: authHeader},
		Headers:          headers,
		Body:             in.Body,
		TimeoutMs:        node.TimeoutMs,
		RetryCount:       node.RetryCount,
		RetryBackoffMs:   node.RetryBackoffMs,
		ClickhouseTable:  node.ClickHouseTable,
		LogRequestBody:   node.LogRequestBody,
		LogResponseBody:  node.LogResponseBody,
		LogHeaders:       node.LogHeaders,
		ClientIp:         in.ClientIP,
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
