package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"

	"nexus/internal/domain"
	"nexus/internal/domain/ackspec"
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

	// §83: готовый ответ по шаблону узла. nil — отвечаем как раньше
	// ({"result":true,"id":…} либо 202 queued).
	Ack *AckResponse
	// §83: рендер шаблона не удался, но политика узла — «ответить как раньше».
	// Строка — ackspec.Reason; handler инкрементит по ней метрику (warn уже
	// написан в usecase). Пусто — деградации не было.
	AckDegraded string
}

// RouteAsyncUsecase — обработка /v1/requestAsync/*.
type RouteAsyncUsecase struct {
	nodes      port.NodeReader
	producer   AsyncProducer
	asyncTopic string
	maxHops    int
	logger     logging.Logger

	// §83: скомпилированные шаблоны ответа. Узел приезжает из кеша строкой, а
	// компиляция на каждый принятый запрос — лишняя работа на горячем пути
	// (937 нс против 70 нс на попадание в кеш).
	ackCache *ackspec.Cache
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
		// §83: размер по умолчанию — порядка числа активных async-узлов
		// инсталляции; наружу не выносится, настраивать нечего.
		ackCache: ackspec.NewCache(0),
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
	if !in.RequireCallback && !MethodMatches(in.Method, node.IncomingMethod) {
		return nil, domain.ErrNodeMethodNotAllowed
	}

	// §82.3: во внешний async-эндпоинт пускаем только узлы, которые и настроены
	// асинхронными. Послабление «любой root_method можно отправить через async»
	// делалось ради §3.6 (paused-узел копит запросы в очереди) — но выдано было
	// безусловно, то есть наружу, и настройка узла «request» ничего не значила.
	//
	// Чем это обернулось: боевой узел acs_sigur с root_method=request принимал
	// ~11 запросов в минуту в async-форме от клиента, застрявшего на одной
	// записи журнала более суток. В async шина отвечает своим
	// {"result":true,"id":…} мгновенно, а клиент ждал ответ приёмника — и не
	// сдвигался никогда. Отбить его настройкой узла было нечем.
	//
	// Внутренние вызовы RouteAsync (§3.6 из sync-ветки и §16 callback) флага не
	// ставят и проверку не проходят — см. godoc RouteInput.ExternalAsync.
	if in.ExternalAsync && node.RootMethod != domain.RootMethodRequestAsync {
		// §51.9: без этой строки отбитый интегратор ломается молча, а по 404
		// «node not found» причину не восстановить. Warn, а не debug: это
		// рассинхронизация настроек, её надо чинить, а не наблюдать.
		u.logger.Warn("async ingress rejected: node is not async",
			u.logger.Str("op", "receiver.async"),
			u.logger.Str("node", node.Path),
			u.logger.Str("node_id", node.ID),
			u.logger.Str("root_method", string(node.RootMethod)),
			u.logger.Str("client_ip", in.ClientIP))
		return nil, domain.ErrNodeNotAsyncIngress
	}

	if err := CheckIncomingAuth(node, in.Header, in.Query, in.Body); err != nil {
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
	targetURL = AppendPathSuffix(targetURL, remainder)

	id := uuid.NewString()

	// §83: ответ собирается ДО публикации. При on_error=error отказ обязан
	// означать «не принято»: 400 на уже лежащее в Kafka сообщение заставил бы
	// клиента повторить пакет, то есть шина сама порождала бы дубли.
	// Источники — effBody и cleanQuery, то есть тело и query БЕЗ вырезанных
	// кред: шаблон не должен возвращать вызывающей стороне её секрет.
	ack, ackDegraded, err := u.renderAck(node, in, id, remainder, effBody, cleanQuery)
	if err != nil {
		return nil, err
	}

	env := BuildEnvelope(id, node, EffectiveOutgoingMethod(node, in.Method), targetURL, authHeader, in.ClientIP, remainder,
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
	start := time.Now()
	if err := u.producer.Produce(produceCtx, u.asyncTopic, node.Path, payload, headers); err != nil {
		finish(err)
		return nil, fmt.Errorf("produce: %w", err)
	}
	finish(nil)
	// §51.9: публикация envelope в Kafka — id/размер/длительность; offset
	// producer наружу не отдаёт (интерфейс возвращает только error).
	u.logger.Debug("route: async envelope produced",
		u.logger.Str("id", id),
		u.logger.Str("node", node.Path),
		u.logger.Str("topic", u.asyncTopic),
		u.logger.Int("payload_bytes", len(payload)),
		u.logger.Int("duration_ms", int(time.Since(start).Milliseconds())),
		u.logger.Any("queued", node.Status == domain.NodeStatusPaused))

	return &RouteAsyncResult{
		ID:          id,
		NodeStatus:  node.Status,
		Queued:      node.Status == domain.NodeStatusPaused,
		Ack:         ack,
		AckDegraded: ackDegraded,
	}, nil
}
