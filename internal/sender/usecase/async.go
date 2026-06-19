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

// AsyncProcessor — обработчик одного Kafka-сообщения для Sender.
type AsyncProcessor struct {
	nodes    NodeReader
	send     *SendUsecase
	dlq      DLQProducer
	cancel   CancelSet // §34.4: может быть nil (Redis отсутствует)
	dlqTopic string
	logger   logging.Logger
	metrics  *metrics.Metrics

	pausedRetryAfter time.Duration
}

func NewAsyncProcessor(
	nodes NodeReader,
	send *SendUsecase,
	dlq DLQProducer,
	cancel CancelSet,
	dlqTopic string,
	m *metrics.Metrics,
	logger logging.Logger,
) *AsyncProcessor {
	return &AsyncProcessor{
		nodes:            nodes,
		send:             send,
		dlq:              dlq,
		cancel:           cancel,
		dlqTopic:         dlqTopic,
		logger:           logger,
		metrics:          m,
		pausedRetryAfter: 30 * time.Second,
	}
}

// HandleResult — что делать с сообщением.
type HandleResult int

const (
	HandleAck   HandleResult = iota // commit offset
	HandleRetry                     // не коммитить — обработать снова (для paused-узлов)
	HandleDLQed                     // отправлено в DLQ, commit offset
)

// Handle обрабатывает одно сообщение. Возвращает решение по offset.
//
// Логика:
//   - десериализация envelope;
//   - перечитываем актуальный узел; если disabled — ack (нет смысла
//     ретраить узел, который явно отключён);
//   - если paused — retry без commit, sleep pausedRetryAfter (§3.6:
//     «не коммитит offset и ждёт до следующего polling-цикла»);
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

	if node.Status == domain.NodeStatusPaused {
		// §3.6: «Sender-consumer пропускает сообщения для paused-узлов,
		// переоткладывает обработку через delayed-redelivery либо
		// не коммитит offset». Ожидание прерываемо ctx: при shutdown
		// воркер не должен висеть до pausedRetryAfter на каждом сообщении.
		select {
		case <-ctx.Done():
		case <-time.After(p.pausedRetryAfter):
		}
		return HandleRetry
	}

	out := p.send.Send(ctx, buildSendInput(node, env))

	if p.metrics != nil {
		p.metrics.RequestsTotal.
			WithLabelValues("requestAsync", env.NodePath, strconv.FormatInt(int64(out.StatusCode), 10)).Inc()
		p.metrics.RequestDuration.
			WithLabelValues("requestAsync", env.NodePath).Observe(float64(out.DurationMs) / 1000.0)
		if out.StatusCode < 200 || out.StatusCode >= 300 {
			p.metrics.RequestsIncompleteTotal.WithLabelValues("requestAsync", env.NodePath).Inc()
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
	return HandleDLQed
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
