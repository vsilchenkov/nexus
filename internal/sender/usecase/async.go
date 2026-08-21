package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/metrics"
	otelpf "nexus/internal/platform/otel"
)

// NodeReader — interface чтения актуального узла перед обработкой
// сообщения (sender перечитывает status/retry на каждое сообщение).
type NodeReader interface {
	GetByPath(ctx context.Context, path string) (*domain.Node, error)
}

// DLQProducer — publish failed message в nexus.async.dlq.
type DLQProducer interface {
	Produce(ctx context.Context, topic, key string, value []byte, headers map[string]string) error
}

// CancelSet — проверка отменённых через UI async-сообщений (§34.4).
// Реализация — internal/platform/queuecancel (Redis). nil → проверка выключена
// (нет Redis). Объявлен на стороне consumer'а (accept interfaces, §17.2).
type CancelSet interface {
	IsCancelled(ctx context.Context, id string) (bool, error)
}

// NodeStatusWriter — персист исхода последнего исходящего вызова узла
// (§46, §52: ok/degraded/down). Реализация — internal/platform/nodestatus
// (Redis) либо Noop при отсутствии Redis. Best-effort: без возврата ошибки
// (фиксация статуса не влияет на доставку). Объявлен на стороне consumer'а
// (accept interfaces, §17.2).
type NodeStatusWriter interface {
	// SetLastOutcome возвращает ЭФФЕКТИВНЫЙ исход: сырой down остаётся degraded,
	// пока подряд идущих отказов меньше порога (§52-доп). По нему же выставляется
	// Prometheus-гаудж — иначе бейдж и алерты трактовали бы один узел по-разному.
	SetLastOutcome(ctx context.Context, nodePath string, outcome domain.NodeOutcome) domain.NodeOutcome
}

// AsyncProcessor — обработчик одного Kafka-сообщения для Sender.
type AsyncProcessor struct {
	nodes      NodeReader
	send       *SendUsecase
	dlq        DLQProducer
	cancel     CancelSet        // §34.4: может быть nil (Redis отсутствует)
	nodeStatus NodeStatusWriter // §46: персист «Down» в Redis (Noop без Redis)
	dlqTopic   string
	logger     logging.Logger
	metrics    *metrics.Metrics

	pausedRetryAfter time.Duration
	// pausedTopic — delay-топик отложенных сообщений paused-узлов (§3.6).
	// Пусто → старое поведение (удерживать сообщение на месте до снятия паузы).
	pausedTopic string
}

// AsyncOption — функциональная опция конструктора AsyncProcessor.
type AsyncOption func(*AsyncProcessor)

// WithPausedRetryAfter переопределяет паузу перед повторной обработкой
// сообщения paused-узла (дефолт 30s, §3.6). Нужна тестам: иначе каждое
// сообщение paused-узла держит партицию полминуты. Прод-конфигурацию не
// меняем — дефолт остаётся прежним.
func WithPausedRetryAfter(d time.Duration) AsyncOption {
	return func(p *AsyncProcessor) {
		if d > 0 {
			p.pausedRetryAfter = d
		}
	}
}

// WithPausedRequeue включает перенос сообщений paused-узлов в delay-топик
// (§3.6): вместо удержания сообщения на месте (что блокирует всю партицию, а
// в ней по хешу node_path лежат и ДРУГИЕ узлы) processor переиздаёт его в
// topic и разрешает коммит основного offset'а.
//
// Ту же опцию получает sweeper delay-топика: для него republish в тот же топик
// означает «узел всё ещё на паузе, вернуть в хвост очереди».
func WithPausedRequeue(topic string) AsyncOption {
	return func(p *AsyncProcessor) { p.pausedTopic = topic }
}

func NewAsyncProcessor(
	nodes NodeReader,
	send *SendUsecase,
	dlq DLQProducer,
	cancel CancelSet,
	nodeStatus NodeStatusWriter,
	dlqTopic string,
	m *metrics.Metrics,
	logger logging.Logger,
	opts ...AsyncOption,
) *AsyncProcessor {
	p := &AsyncProcessor{
		nodes:            nodes,
		send:             send,
		dlq:              dlq,
		cancel:           cancel,
		nodeStatus:       nodeStatus,
		dlqTopic:         dlqTopic,
		logger:           logger,
		metrics:          m,
		pausedRetryAfter: 30 * time.Second,
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// HandleResult — что делать с сообщением.
type HandleResult int

const (
	HandleAck   HandleResult = iota // commit offset
	HandleRetry                     // не коммитить — переобработать это же сообщение на месте
	HandleDLQed                     // отправлено в DLQ, commit offset
	// HandleRequeued — сообщение перенесено в delay-топик paused-узлов (§3.6):
	// commit offset, как у DLQ. Отдельный от HandleAck код нужен, чтобы «ушло
	// в отложенные» не выглядело в логах и метриках как «доставлено».
	HandleRequeued
)

// Handle обрабатывает одно сообщение. Возвращает решение по offset.
//
// Логика:
//   - десериализация envelope;
//   - перечитываем актуальный узел; если disabled — ack (нет смысла
//     ретраить узел, который явно отключён);
//   - если paused — перенос в delay-топик (§3.6, при заданном pausedTopic),
//     иначе устаревшее поведение: sleep pausedRetryAfter + retry без commit;
//   - иначе — HTTP-вызов через SendUsecase. Если done=true → ack.
//     Иначе при первой обработке → DLQ с метаданными причины.
//
// Retry-цикл с экспоненциальным backoff живёт ВНУТРИ SendUsecase
// (см. internal/sender/usecase/send.go); ConsumerWorker делает
// только одну попытку доставки на одно Kafka-сообщение.
// Handle принимает raw envelope (JSON-payload Kafka-сообщения) и msgHeaders
// (Kafka message headers, для propagation OTel trace-context'а — §16 ТЗ,
// Phase 8.4). msgHeaders может быть nil — пропуск extraction'а тогда no-op.
func (p *AsyncProcessor) Handle(ctx context.Context, raw []byte, msgHeaders map[string]string) HandleResult {
	// OTel: extract traceparent → consumer-span. При выключенном tracing —
	// extract возвращает входной ctx, span будет no-op'ным.
	ctx = otelpf.ExtractKafkaHeaders(ctx, msgHeaders)
	ctx, finishSpan := otelpf.StartKafkaConsumerSpan(ctx, "nexus.async")
	defer finishSpan(nil)

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		p.logger.ErrorWithOp("envelope unmarshal failed", err, "async.Handle")
		return HandleAck // не повторяем — это битое сообщение
	}

	node, err := p.nodes.GetByPath(ctx, env.NodePath)
	if err != nil {
		if errors.Is(err, domain.ErrNodeNotFound) {
			p.logger.Warn("async message for unknown node, dropping",
				p.logger.Str("node_path", env.NodePath),
				p.logger.Str("id", env.ID))
			return HandleAck
		}
		p.logger.ErrorWithOp("node read failed", err, "async.Handle",
			p.logger.Str("node_path", env.NodePath))
		return HandleRetry
	}
	if node.Status == domain.NodeStatusDisabled {
		p.logger.Info("async drop: node disabled",
			p.logger.Str("node_path", env.NodePath),
			p.logger.Str("id", env.ID))
		return HandleAck
	}

	// §34.4: сообщение отменено оператором через UI — не отправляем во внешний
	// адрес, коммитим offset (физически из лога Kafka удалить нельзя). Проверка
	// ДО paused-блока: бэклог paused-узла (мёртвый внешний адрес) — основной
	// сценарий, его тоже надо иметь возможность очистить. Ошибка Redis →
	// fail-open: доставку не блокируем (§9.4).
	if p.cancel != nil {
		cancelled, err := p.cancel.IsCancelled(ctx, env.ID)
		if err != nil {
			p.logger.Warn("cancel-set check failed; delivering",
				p.logger.Str("id", env.ID), p.logger.Err(err))
		} else if cancelled {
			p.logger.Info("async drop: cancelled by operator",
				p.logger.Str("node_path", env.NodePath),
				p.logger.Str("id", env.ID))
			if p.metrics != nil {
				p.metrics.RequestsTotal.WithLabelValues("requestAsync", env.NodePath, "cancelled").Inc()
			}
			return HandleAck
		}
	}

	// §83.7: узел больше не async — принятое ранее в очередь не доставляем.
	// Проверка ДО paused-ветки: решение терминальное, откладывать нечего.
	if reason, stop := asyncIngressRevoked(node, env); stop {
		return p.dropNotAsync(ctx, raw, env, reason)
	}

	if node.Status == domain.NodeStatusPaused {
		return p.handlePaused(ctx, raw, env, msgHeaders)
	}

	// §51.9: старт обработки async-сообщения — привязка id↔узел на debug.
	p.logger.Debug("async: delivering message",
		p.logger.Str("id", env.ID),
		p.logger.Str("node_path", env.NodePath),
		p.logger.Int("body_len", len(env.Body)))

	in := buildSendInput(node, env)
	logRebuiltTarget(p.logger, "async", env, in.TargetURL)
	logRMQOrigin(p.logger, "async", env)
	out := p.send.Send(ctx, in)

	// §52: outcome (ok/degraded/down) — для бейджа узла; isErr («любой
	// не-2xx») — прежняя семантика для incomplete_total и решения ack/dlq.
	outcome := domain.OutcomeFromStatusCode(out.StatusCode)
	isErr := outcome.IsError()
	// §51.9: исход доставки (решение ack/dlq — ниже) раньше был виден только
	// в метриках/CH.
	p.logger.Debug("async: delivery finished",
		p.logger.Str("id", env.ID),
		p.logger.Str("node_path", env.NodePath),
		p.logger.Int("status", int(out.StatusCode)),
		p.logger.Int("attempts", int(out.Attempts)),
		p.logger.Int("duration_ms", int(out.DurationMs)),
		p.logger.Str("outcome", string(outcome)))
	// §46: персистентный исход в Redis (переживает рестарт; Noop без Redis).
	// Идёт ПЕРЕД гауджем намеренно (§52-доп): счётчик подряд идущих отказов
	// живёт там же, и только он знает эффективный исход — сырой down остаётся
	// degraded, пока порог не набран. Гаудж ниже выставляется по этому значению.
	// §81.2: контекст отвязан — запись делается ПОСЛЕ вызова и обязана пережить
	// смерть родителя (клиент шины ушёл, сервис останавливается). go-redis
	// отбрасывает команду с отменённым контекстом ещё в пуле, поэтому раньше на
	// обрыве бейдж узла молча не обновлялся, а в лог сыпался ложный warn.
	effOutcome := outcome
	if p.nodeStatus != nil && !out.ParentGone {
		statusCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), nodeStatusWriteTimeout)
		effOutcome = p.nodeStatus.SetLastOutcome(statusCtx, env.NodePath, outcome)
		cancel()
	}
	if p.metrics != nil {
		p.metrics.RequestsTotal.
			WithLabelValues("requestAsync", env.NodePath, strconv.FormatInt(int64(out.StatusCode), 10)).Inc()
		p.metrics.RequestDuration.
			WithLabelValues("requestAsync", env.NodePath).Observe(float64(out.DurationMs) / 1000.0)
		if isErr {
			p.metrics.RequestsIncompleteTotal.WithLabelValues("requestAsync", env.NodePath).Inc()
		}
		// §41/§52: исход последнего вызова узла (in-memory гаудж).
		// §81.2: обрыв вызывающей стороной (здесь — остановка сервиса) исходом
		// узла не считается.
		if !out.ParentGone {
			p.metrics.SetNodeLastRequestOutcome(env.NodePath, effOutcome)
		}
	}

	if out.StatusCode >= 200 && out.StatusCode < 300 {
		return HandleAck
	}

	// Retry исчерпан → DLQ с метаданными.
	if err := p.publishDLQ(ctx, raw, env, out); err != nil {
		p.logger.ErrorWithOp("dlq publish failed", err, "async.publishDLQ",
			p.logger.Str("id", env.ID))
		return HandleRetry
	}
	// §51.9: успешный уход в DLQ раньше молчал — сообщение «исчезало» из
	// основного потока без следа в служебных логах.
	p.logger.Debug("async: message sent to dlq",
		p.logger.Str("id", env.ID),
		p.logger.Str("node_path", env.NodePath),
		p.logger.Str("dlq_topic", p.dlqTopic),
		p.logger.Int("status", int(out.StatusCode)),
		p.logger.Int("attempts", int(out.Attempts)))
	return HandleDLQed
}

// handlePaused решает судьбу сообщения узла на паузе (§3.6).
//
// При заданном pausedTopic сообщение переезжает в delay-топик, а offset
// основного топика коммитится: партиция освобождается, и соседние узлы (по
// хешу node_path в неё попадают ДРУГИЕ узлы) не ждут снятия паузы. Для самого
// sweeper'а delay-топика это же означает «вернуть в хвост очереди».
//
// Без pausedTopic — устаревшее поведение: подождать и переобработать на месте.
// Коммитить сообщение в этом режиме нельзя (иначе оно потеряется), поэтому вся
// партиция стоит до снятия паузы.
func (p *AsyncProcessor) handlePaused(ctx context.Context, raw []byte, env Envelope, msgHeaders map[string]string) HandleResult {
	if p.pausedTopic == "" {
		// Ожидание прерываемо ctx: при shutdown воркер не должен висеть до
		// pausedRetryAfter на каждом сообщении.
		select {
		case <-ctx.Done():
		case <-time.After(p.pausedRetryAfter):
		}
		return HandleRetry
	}

	hdrs := p.pausedHeaders(env, msgHeaders)
	if err := p.dlq.Produce(ctx, p.pausedTopic, env.NodePath, raw, hdrs); err != nil {
		// Не смогли переложить — НЕ коммитим: сообщение обработается снова
		// (retry-in-place в адаптере), потеря исключена.
		p.logger.ErrorWithOp("paused requeue failed", err, "async.handlePaused",
			p.logger.Str("id", env.ID),
			p.logger.Str("node_path", env.NodePath))
		return HandleRetry
	}
	// §51.9: перенос в отложенные — единственный след того, что сообщение
	// покинуло основной топик, не будучи доставленным.
	p.logger.Debug("async: message requeued to paused topic",
		p.logger.Str("id", env.ID),
		p.logger.Str("node_path", env.NodePath),
		p.logger.Str("paused_topic", p.pausedTopic),
		p.logger.Str("paused_since", hdrs["paused_since"]))
	if p.metrics != nil {
		p.metrics.RequestsTotal.WithLabelValues("requestAsync", env.NodePath, "paused").Inc()
	}
	return HandleRequeued
}

// pausedHeaders собирает заголовки копии в delay-топике. paused_since — время
// ПЕРВОГО откладывания: сообщение циркулирует в топике, пока узел на паузе, и
// затирать метку на каждом круге нельзя (по ней видно возраст бэклога).
func (p *AsyncProcessor) pausedHeaders(env Envelope, in map[string]string) map[string]string {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	since := in["paused_since"]
	if since == "" {
		since = now
	}
	return map[string]string{
		"id":              env.ID,
		"node_path":       env.NodePath,
		"orig_topic":      headerOr(in, "orig_topic", "nexus.async"),
		"paused_since":    since,
		"last_requeue_at": now,
	}
}

func (p *AsyncProcessor) publishDLQ(ctx context.Context, raw []byte, env Envelope, out SendOutput) error {
	hdrs := map[string]string{
		"id":         env.ID,
		"node_path":  env.NodePath,
		"orig_topic": "nexus.async",
		"reason": fmt.Sprintf("status=%d attempts=%d duration_ms=%d err=%s",
			out.StatusCode, out.Attempts, out.DurationMs, out.Error),
		"last_attempt_at": time.Now().UTC().Format(time.RFC3339Nano),
	}
	return p.dlq.Produce(ctx, p.dlqTopic, env.NodePath, raw, hdrs)
}
