package usecase

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"bus/internal/domain"
	"bus/internal/platform/logging"
	otelpf "bus/internal/platform/otel"
	"bus/internal/receiver/usecase/port"
)

// AsyncProducer — интерфейс, который реализует kafka.Producer.
// Объявлен здесь, чтобы usecase не зависел от конкретного broker'а
// (см. §17.2 — accept interfaces).
type AsyncProducer interface {
	Produce(ctx context.Context, topic, key string, value []byte, headers map[string]string) error
}

// RouteAsyncResult — что Receiver возвращает клиенту /v1/requestAsync.
type RouteAsyncResult struct {
	ID         string
	NodeStatus domain.NodeStatus
	Queued     bool // true для paused-узлов (§3.6)
}

// RouteAsyncUsecase — обработка /v1/requestAsync/*.
type RouteAsyncUsecase struct {
	nodes      port.NodeReader
	producer   AsyncProducer
	asyncTopic string
	logger     logging.Logger
}

func NewRouteAsyncUsecase(
	nodes port.NodeReader,
	producer AsyncProducer,
	asyncTopic string,
	logger logging.Logger,
) *RouteAsyncUsecase {
	return &RouteAsyncUsecase{
		nodes:      nodes,
		producer:   producer,
		asyncTopic: asyncTopic,
		logger:     logger,
	}
}

// RouteAsync принимает sync RouteInput (тот же формат), валидирует и
// кладёт envelope в databus.async с key=node_path (для сохранения
// порядка обработки одного узла).
func (u *RouteAsyncUsecase) RouteAsync(ctx context.Context, in RouteInput) (*RouteAsyncResult, error) {
	node, err := u.nodes.GetByPath(ctx, in.NodePath)
	if err != nil {
		return nil, err
	}
	if node.Status == domain.NodeStatusDisabled {
		return nil, domain.ErrNodeNotFound
	}

	// §16 ТЗ: callback-маршрут разрешён только для узлов с подписью.
	if in.RequireCallback && node.IncomingAuthType != domain.IncomingAuthTypeWebhookSignature {
		return nil, domain.ErrCallbackNotAllowed
	}

	// Любой root_method можно отправить через async — §3.6 «При paused
	// все запросы превращаются в async». Поэтому RouteAsync доступен
	// и для request-узлов, если они в paused.

	if err := CheckIncomingAuth(node, in.Header, in.Body); err != nil {
		return nil, err
	}

	// Динамическая авторизация / очистка query и body.
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

	id := uuid.NewString()
	env := BuildEnvelope(id, node, in.Method, targetURL, authHeader, in.ClientIP,
		effHeader, cleanQuery, effBody)

	payload, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("marshal envelope: %w", err)
	}

	headers := map[string]string{
		"id":        id,
		"node_path": node.Path,
		"attempt":   "0",
	}

	// OTel (Phase 8.4): producer-span + traceparent в headers. На consumer-стороне
	// ExtractKafkaHeaders восстановит parent, и обработка envelope попадёт в тот
	// же trace, что и входящий HTTP-запрос /v1/requestAsync.
	produceCtx, finish := otelpf.StartKafkaProducerSpan(ctx, u.asyncTopic)
	otelpf.InjectKafkaHeaders(produceCtx, headers)
	if err := u.producer.Produce(produceCtx, u.asyncTopic, node.Path, payload, headers); err != nil {
		finish(err)
		return nil, fmt.Errorf("produce: %w", err)
	}
	finish(nil)

	return &RouteAsyncResult{
		ID:         id,
		NodeStatus: node.Status,
		Queued:     node.Status == domain.NodeStatusPaused,
	}, nil
}
