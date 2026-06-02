package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/metrics"
	otelpf "nexus/internal/platform/otel"
)

// ---- Порты (consumer-side, §17.2) -------------------------------------------

// PullNodeLister — список узлов RabbitMQAsync с ПОЛНЫМ конфигом (включая
// расшифрованные rmq_password/auth_credentials). Обычный NodeReader для этого
// не годится — он не тянет rmq_*-колонки. Реализуется PG-адаптером.
type PullNodeLister interface {
	ListRabbitMQAsync(ctx context.Context) ([]*domain.Node, error)
}

// RMQDelivery — одно сообщение, забранное из RabbitMQ через basic.get.
type RMQDelivery struct {
	DeliveryTag uint64
	Body        []byte
	ContentType string
	MessageID   string
	Exchange    string
	RoutingKey  string
	Timestamp   time.Time
	Headers     map[string]string
}

// RMQConsumer — открытый AMQP-канал к очереди одного узла (manual ack).
type RMQConsumer interface {
	// GetBatch забирает до max сообщений через basic.get. Пустой батч — без ошибки.
	GetBatch(ctx context.Context, max int) ([]RMQDelivery, error)
	Ack(tag uint64) error
	Nack(tag uint64, requeue bool) error
	Reject(tag uint64, requeue bool) error
	// QueueStats — passive-метрики очереди (messages, consumers).
	QueueStats() (messages int, consumers int, err error)
	Close() error
}

// RMQConnector открывает RMQConsumer к очереди узла (TLS/qos внутри).
type RMQConnector interface {
	Connect(ctx context.Context, n *domain.Node) (RMQConsumer, error)
}

// HealthSink — куда воркер публикует свой health-снимок (§27.4). Реализуется
// Redis-адаптером; Web читает оттуда. nil — no-op (unit-тесты).
type HealthSink interface {
	Publish(ctx context.Context, h domain.RMQHealth) error
}

// ---- Воркер -----------------------------------------------------------------

const (
	rmqBackoffMin   = 1 * time.Second
	rmqBackoffMax   = 60 * time.Second
	rmqDegradeAfter = 5 * time.Minute
	rmqBatchTimeout = 10 * time.Second // graceful: дождаться текущий батч
)

// PullerWorker — один воркер на узел RabbitMQAsync (§27.2). Крутит цикл забора
// basic.get → publish в nexus.async → basic.ack после acks=all. Переподключается
// с экспоненциальным backoff; после rmqDegradeAfter безуспешных попыток
// помечает узел degraded (runtime, не node.status).
type PullerWorker struct {
	node            *domain.Node
	connector       RMQConnector
	producer        AsyncProducer
	asyncTopic      string
	maxMessageBytes int
	metrics         *metrics.Metrics
	sink            HealthSink
	logger          logging.Logger

	degradeAfter time.Duration // через сколько непрерывной недоступности → degraded

	consumer       RMQConsumer
	state          domain.RMQConnState
	attempts       int64
	downSince      time.Time
	degraded       bool
	degradedReason string
	lastQueueDepth int64
	lastConsumers  int64
}

func NewPullerWorker(
	node *domain.Node,
	connector RMQConnector,
	producer AsyncProducer,
	asyncTopic string,
	maxMessageBytes int,
	m *metrics.Metrics,
	sink HealthSink,
	logger logging.Logger,
) *PullerWorker {
	return &PullerWorker{
		node:            node,
		connector:       connector,
		producer:        producer,
		asyncTopic:      asyncTopic,
		maxMessageBytes: maxMessageBytes,
		metrics:         m,
		sink:            sink,
		logger:          logger,
		state:           domain.RMQConnDown,
		degradeAfter:    rmqDegradeAfter,
	}
}

// SetDegradeAfter переопределяет порог перехода в degraded (дефолт
// rmqDegradeAfter=5м). Вызывать только до Run — например, в тестах, чтобы не
// ждать 5 минут реального простоя.
func (w *PullerWorker) SetDegradeAfter(d time.Duration) {
	if d > 0 {
		w.degradeAfter = d
	}
}

// Run крутит цикл забора до отмены ctx. При остановке закрывает канал
// (graceful, §27.5: текущий батч уже отработан или nack'нут внутри pullOnce).
func (w *PullerWorker) Run(ctx context.Context) {
	w.setState(ctx, domain.RMQConnConnecting, "")
	defer w.closeConsumer()

	interval := time.Duration(w.node.PullIntervalSec) * time.Second
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		if w.consumer == nil {
			if !w.reconnect(ctx) {
				return // ctx отменён во время backoff
			}
		}
		w.pullOnce(ctx)

		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// reconnect пытается открыть канал с экспоненциальным backoff. Возвращает
// false только если ctx отменён. Поднимает degraded после rmqDegradeAfter.
func (w *PullerWorker) reconnect(ctx context.Context) bool {
	backoff := rmqBackoffMin
	for {
		if err := ctx.Err(); err != nil {
			return false
		}
		w.setState(ctx, domain.RMQConnConnecting, "")
		cons, err := w.connector.Connect(ctx, w.node)
		if err == nil {
			w.consumer = cons
			w.attempts = 0
			w.downSince = time.Time{}
			w.clearDegraded(ctx)
			w.setState(ctx, domain.RMQConnUp, "")
			return true
		}

		w.attempts++
		if w.downSince.IsZero() {
			w.downSince = nowUTC()
		}
		reason := fmt.Sprintf("RabbitMQ unreachable: %v", err)
		if nowUTC().Sub(w.downSince) >= w.degradeAfter {
			w.markDegraded(ctx, reason)
		}
		w.setState(ctx, domain.RMQConnDown, reason)
		w.logger.Warn("rmq connect failed, backing off",
			w.logger.Op("puller.connect"),
			w.logger.Str("node", w.node.Path), w.logger.Err(err))

		select {
		case <-ctx.Done():
			return false
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > rmqBackoffMax {
			backoff = rmqBackoffMax
		}
	}
}

// pullOnce забирает один батч и публикует его в Kafka. Ошибки канала роняют
// consumer (на следующей итерации reconnect). Ошибка Kafka → nack(requeue)
// остатка батча.
func (w *PullerWorker) pullOnce(ctx context.Context) {
	start := nowUTC()
	w.updateQueueStats(ctx)

	msgs, err := w.consumer.GetBatch(ctx, int(w.node.PullBatchSize))
	if err != nil {
		w.logger.Warn("rmq get batch failed",
			w.logger.Op("puller.get"),
			w.logger.Str("node", w.node.Path), w.logger.Err(err))
		w.closeConsumer()
		return
	}

	for _, msg := range msgs {
		if err := ctx.Err(); err != nil {
			// graceful: остаток батча возвращаем в очередь.
			_ = w.consumer.Nack(msg.DeliveryTag, true)
			continue
		}
		w.handleDelivery(ctx, msg)
	}

	if w.metrics != nil && len(msgs) > 0 {
		w.metrics.RMQPullDuration.WithLabelValues(w.node.Path).
			Observe(time.Since(start).Seconds())
	}
}

// handleDelivery публикует одно сообщение в nexus.async и ack'ает его после
// подтверждения Kafka. Слишком большое сообщение — reject без requeue.
func (w *PullerWorker) handleDelivery(ctx context.Context, msg RMQDelivery) {
	payload, err := w.buildPayload(msg)
	if err != nil {
		// Конфиг узла битый (не должно случаться после Validate) — reject,
		// чтобы не зациклиться на одном сообщении.
		w.reject(msg.DeliveryTag)
		w.logger.ErrorWithOp("rmq build envelope failed", err, "puller.envelope",
			w.logger.Str("node", w.node.Path))
		return
	}
	if w.maxMessageBytes > 0 && len(payload) > w.maxMessageBytes {
		w.reject(msg.DeliveryTag)
		w.logger.Warn("rmq message exceeds max_message_bytes, rejected",
			w.logger.Op("puller.rejectOversize"),
			w.logger.Str("node", w.node.Path),
			w.logger.Int("size", len(payload)))
		return
	}

	headers := map[string]string{
		"id":        w.msgID(msg),
		"node_path": w.node.Path,
		"attempt":   "0",
	}
	produceCtx, finish := otelpf.StartKafkaProducerSpan(ctx, w.asyncTopic)
	otelpf.InjectKafkaHeaders(produceCtx, headers)
	if err := w.producer.Produce(produceCtx, w.asyncTopic, w.node.Path, payload, headers); err != nil {
		finish(err)
		// Kafka недоступна — возвращаем сообщение в очередь (потерь нет, §27.2).
		_ = w.consumer.Nack(msg.DeliveryTag, true)
		w.incPulled("nack")
		w.logger.Warn("rmq publish to kafka failed, requeue",
			w.logger.Op("puller.produce"),
			w.logger.Str("node", w.node.Path), w.logger.Err(err))
		return
	}
	finish(nil)

	// acks=all уже подтверждён producer'ом (RequireAll) — теперь безопасно ack.
	if err := w.consumer.Ack(msg.DeliveryTag); err != nil {
		// Сообщение опубликовано, но ack не прошёл → дубль после reconnect
		// (at-least-once, §27.2). Логируем, не падаем.
		w.logger.Warn("rmq ack failed after publish (possible duplicate)",
			w.logger.Op("puller.ack"),
			w.logger.Str("node", w.node.Path), w.logger.Err(err))
		return
	}
	w.incPulled("ok")
}

// buildPayload формирует Kafka-envelope из AMQP-сообщения (§27.3).
func (w *PullerWorker) buildPayload(msg RMQDelivery) ([]byte, error) {
	targetURL, _, err := ResolveURL(w.node, nil)
	if err != nil {
		return nil, err
	}
	authHeader, err := BuildOutgoingAuth(w.node)
	if err != nil {
		return nil, err
	}

	headers := map[string]string{}
	// Проброс только заголовков из forward_headers (§27.3).
	for _, name := range w.node.ForwardHeaders {
		if v, ok := msg.Headers[name]; ok {
			headers[name] = v
		}
	}
	ct := msg.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	headers["Content-Type"] = ct
	headers["X-Nexus-Source"] = "rabbitmq"
	headers["X-Nexus-Routing-Key"] = msg.RoutingKey

	outMethod := string(w.node.OutgoingMethod) // §3.2 (#5): метод вызова получателя
	if outMethod == "" {
		outMethod = "POST"
	}
	env := &Envelope{
		ID:         w.msgID(msg),
		NodePath:   w.node.Path,
		Method:     outMethod,
		TargetURL:  targetURL,
		AuthHeader: authHeader,
		Headers:    headers,
		Body:       msg.Body,
		ClientIP:   w.sourceURI(),
		ReceivedAt: nowUTC(),
		RMQ: &RMQMeta{
			Exchange:    msg.Exchange,
			RoutingKey:  msg.RoutingKey,
			DeliveryTag: msg.DeliveryTag,
			MessageID:   msg.MessageID,
			Timestamp:   msg.Timestamp,
		},
	}
	return json.Marshal(env)
}

// sourceURI — значение поля ClientIP/IP лога для RabbitMQAsync (§27.10).
func (w *PullerWorker) sourceURI() string {
	return fmt.Sprintf("rabbitmq://%s:%d/%s", w.node.RMQHost, w.node.RMQPort,
		trimLeadingSlash(w.node.RMQVHost))
}

func (w *PullerWorker) msgID(msg RMQDelivery) string {
	if msg.MessageID != "" {
		return msg.MessageID
	}
	return uuid.NewString()
}

func (w *PullerWorker) reject(tag uint64) {
	if w.consumer != nil {
		_ = w.consumer.Reject(tag, false)
	}
	w.incPulled("reject")
}

func (w *PullerWorker) incPulled(status string) {
	if w.metrics != nil {
		w.metrics.RMQMessagesPulledTotal.WithLabelValues(w.node.Path, status).Inc()
	}
}

func (w *PullerWorker) updateQueueStats(ctx context.Context) {
	if w.consumer == nil {
		return
	}
	msgs, consumers, err := w.consumer.QueueStats()
	if err != nil {
		return
	}
	if w.metrics != nil {
		w.metrics.RMQQueueDepth.WithLabelValues(w.node.Path).Set(float64(msgs))
		w.metrics.RMQConsumerCount.WithLabelValues(w.node.Path).Set(float64(consumers))
	}
	w.publishHealth(ctx, int64(msgs), int64(consumers))
}

func (w *PullerWorker) closeConsumer() {
	if w.consumer != nil {
		_ = w.consumer.Close()
		w.consumer = nil
	}
}

// ---- Health / метрики состояния --------------------------------------------

func (w *PullerWorker) setState(ctx context.Context, s domain.RMQConnState, reason string) {
	w.state = s
	if w.metrics != nil {
		var v float64
		switch s {
		case domain.RMQConnConnecting:
			v = 1
		case domain.RMQConnUp:
			v = 2
		}
		w.metrics.RMQConnectionState.WithLabelValues(w.node.Path).Set(v)
	}
	w.publishHealth(ctx, -1, -1)
	_ = reason
}

func (w *PullerWorker) markDegraded(ctx context.Context, reason string) {
	if !w.degraded {
		w.degraded = true
		w.logger.ErrorWithOp("rmq node degraded", fmt.Errorf("%s", reason), "puller.degraded",
			w.logger.Str("node", w.node.Path))
	}
	if w.metrics != nil {
		w.metrics.NodeDegraded.WithLabelValues(w.node.Path, reason).Set(1)
	}
	w.degradedReason = reason
	w.publishHealth(ctx, -1, -1)
}

func (w *PullerWorker) clearDegraded(ctx context.Context) {
	if w.degraded {
		w.degraded = false
		if w.metrics != nil {
			w.metrics.NodeDegraded.WithLabelValues(w.node.Path, w.degradedReason).Set(0)
		}
		w.degradedReason = ""
		w.publishHealth(ctx, -1, -1)
	}
}

// publishHealth пишет снимок в sink (Redis). qd/cc<0 — «не обновлять», берём
// предыдущие значения из снимка.
func (w *PullerWorker) publishHealth(ctx context.Context, qd, cc int64) {
	if w.sink == nil {
		return
	}
	if qd >= 0 {
		w.lastQueueDepth = qd
	}
	if cc >= 0 {
		w.lastConsumers = cc
	}
	h := domain.RMQHealth{
		NodePath:      w.node.Path,
		ConnState:     w.state,
		Degraded:      w.degraded,
		Reason:        w.degradedReason,
		QueueDepth:    w.lastQueueDepth,
		ConsumerCount: w.lastConsumers,
		Attempts:      w.attempts,
		UpdatedAt:     nowUTC(),
	}
	if !w.downSince.IsZero() {
		h.Since = w.downSince
	}
	if err := w.sink.Publish(ctx, h); err != nil {
		w.logger.Warn("rmq health publish failed",
			w.logger.Str("node", w.node.Path), w.logger.Err(err))
	}
}

func trimLeadingSlash(s string) string {
	if s == "/" {
		return ""
	}
	return s
}

func nowUTC() time.Time { return time.Now().UTC() }
