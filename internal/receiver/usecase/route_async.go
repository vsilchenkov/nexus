package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/google/uuid"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	otelpf "nexus/internal/platform/otel"
	"nexus/internal/receiver/usecase/port"
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
	maxHops    int
	logger     logging.Logger
}

// NewRouteAsyncUsecase создаёт async-роутер. maxHops — лимит переходов запроса
// через шину (§32, X-Nexus-Hops); <= 0 выключает защиту от зацикливания.
func NewRouteAsyncUsecase(
	nodes port.NodeReader,
	producer AsyncProducer,
	asyncTopic string,
	maxHops int,
	logger logging.Logger,
) *RouteAsyncUsecase {
	return &RouteAsyncUsecase{
		nodes:      nodes,
		producer:   producer,
		asyncTopic: asyncTopic,
		maxHops:    maxHops,
		logger:     logger,
	}
}

// RouteAsync принимает sync RouteInput (тот же формат), валидирует и
// кладёт envelope в nexus.async с key=node_path (для сохранения
// порядка обработки одного узла).
func (u *RouteAsyncUsecase) RouteAsync(ctx context.Context, in RouteInput) (*RouteAsyncResult, error) {
	// §32: защита от зацикливания (тот же hop-счётчик, что и в sync-пути).
	hop, loop := nextHop(in.Header, u.maxHops)
	if loop {
		return nil, domain.ErrLoopDetected
	}

	node, remainder, err := resolveNode(ctx, u.nodes, in.TeamSlug, in.NodePath)
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

	// §3.2 (#5): узел принимает только сконфигурированный входящий метод.
	// Callback (webhook) — исключение: маршрут уже зафиксирован как POST и
	// защищён HMAC-подписью, метод диктует внешний провайдер.
	if !in.RequireCallback && !methodMatches(in.Method, node.IncomingMethod) {
		return nil, domain.ErrNodeMethodNotAllowed
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
	// §39: при path-passthrough приклеиваем хвост входящего пути к целевому URL.
	targetURL = appendPathSuffix(targetURL, remainder)

	id := uuid.NewString()
	env := BuildEnvelope(id, node, string(node.OutgoingMethod), targetURL, authHeader, in.ClientIP, remainder,
		effHeader, cleanQuery, effBody)
	// §32: служебный hop-счётчик в обход allowlist узла. На стороне Sender
	// заголовок уйдёт во внешний запрос; если цель — снова Receiver, счётчик
	// продолжит расти и оборвёт петлю.
	if u.maxHops > 0 {
		env.Headers[HeaderHops] = strconv.Itoa(hop)
	}

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
