package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"nexus/internal/domain"
	"nexus/internal/platform/clock"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/metrics"
	"nexus/internal/sender/usecase/port"
)

// BreakerInspector — read-only проверка circuit breaker узла (§36.3 шаг5).
// Объявлена на стороне consumer'а (CLAUDE.md §3). Реализация —
// circuitbreaker.Breaker.IsOpen (без побочных эффектов, не расходует half-open
// пробу). nil → проверка выключена (breaker считается закрытым).
type BreakerInspector interface {
	IsOpen(ctx context.Context, key string) (bool, error)
}

// Метки результата для nexus_dlq_reprocess_total{result}.
const (
	reprocessSucceeded  = "succeeded"   // доставлено 2xx
	reprocessFailed     = "failed"      // не-2xx → republish в хвост
	reprocessTTLDropped = "ttl_dropped" // истёк TTL → финальный лог, drop
	reprocessSkipped    = "skipped"     // paused/breaker-open → republish без попытки
	reprocessDropped    = "dropped"     // узел удалён/disabled/отменён → терминальный drop
)

// ReprocessResult — что адаптер-sweeper должен сделать с offset DLQ-сообщения.
type ReprocessResult int

const (
	// ReprocessCommit — сообщение обработано (доставлено/republish-нуто/drop):
	// коммитим offset, «живая» копия под этим offset'ом больше не циркулирует.
	ReprocessCommit ReprocessResult = iota
	// ReprocessRetry — транзиентный сбой (republish не записался / временная
	// ошибка чтения узла): НЕ коммитим, чтобы не потерять сообщение.
	ReprocessRetry
)

// DLQReprocessor — обработчик ОДНОГО DLQ-сообщения авто-репроцессора (§36).
// Периодический проход (Kafka fetch/commit) живёт в адаптере; здесь — чистая
// логика принятия решения по сообщению (как AsyncProcessor.Handle для async.go).
//
// Порядок проверок (§36.3): парс+резолв узла → tombstone (§34.4) → TTL →
// статус (disabled/paused) → circuit breaker → попытка доставки через SendUsecase.
type DLQReprocessor struct {
	nodes    NodeReader
	send     *SendUsecase
	dlq      DLQProducer
	logw     port.LogWriter
	cancel   CancelSet        // §34.4: может быть nil (нет Redis)
	breaker  BreakerInspector // §36.3 шаг5: может быть nil
	dlqTopic string
	metrics  *metrics.Metrics // nil-safe
	logger   logging.Logger
	host     string

	// clock — источник времени (§4 CLAUDE.md): от него зависят TTL сообщения
	// и момент следующей попытки. Подменяется в тестах.
	clock clock.Clock
}

// NewDLQReprocessor создаёт обработчик DLQ-сообщений. cancel/breaker/m могут
// быть nil. logw используется только для финальной записи ttl_expired в CH.
func NewDLQReprocessor(
	nodes NodeReader,
	send *SendUsecase,
	dlq DLQProducer,
	logw port.LogWriter,
	cancel CancelSet,
	breaker BreakerInspector,
	dlqTopic string,
	m *metrics.Metrics,
	logger logging.Logger,
) *DLQReprocessor {
	host, _ := os.Hostname()
	return &DLQReprocessor{
		nodes:    nodes,
		send:     send,
		dlq:      dlq,
		logw:     logw,
		cancel:   cancel,
		breaker:  breaker,
		dlqTopic: dlqTopic,
		metrics:  m,
		logger:   logger,
		host:     host,
		clock:    clock.System(),
	}
}

// ProcessMessage обрабатывает одно DLQ-сообщение и возвращает решение по offset.
// raw — сырой envelope (JSON), headers — Kafka-заголовки DLQ-сообщения
// (id/node_path/orig_topic/reason/last_attempt_at/attempts, §34.6 + §36.2).
func (r *DLQReprocessor) ProcessMessage(ctx context.Context, raw []byte, headers map[string]string) ReprocessResult {
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		// Битое сообщение — повторять бессмысленно, дропаем.
		r.logger.ErrorWithOp("dlq envelope unmarshal failed", err, "dlq.reprocess")
		r.record("", reprocessDropped)
		return ReprocessCommit
	}

	// §36.3 шаг1: резолв узла.
	node, err := r.nodes.GetByPath(ctx, env.NodePath)
	if err != nil {
		if errors.Is(err, domain.ErrNodeNotFound) {
			r.logger.Info("dlq drop: unknown node",
				r.logger.Str("node_path", env.NodePath),
				r.logger.Str("id", env.ID))
			r.record(env.NodePath, reprocessDropped)
			return ReprocessCommit
		}
		// Транзиентная ошибка чтения — не коммитим, попробуем в другой раз.
		r.logger.ErrorWithOp("dlq node read failed", err, "dlq.reprocess",
			r.logger.Str("node_path", env.NodePath))
		return ReprocessRetry
	}

	// §36.3 шаг2: tombstone (оператор отменил сообщение через UI, §34.4).
	// Ошибка Redis → fail-open: продолжаем обработку (доставку не блокируем, §9.4).
	if r.cancel != nil {
		cancelled, err := r.cancel.IsCancelled(ctx, env.ID)
		if err != nil {
			r.logger.Warn("dlq cancel-set check failed; continuing",
				r.logger.Str("id", env.ID), r.logger.Err(err))
		} else if cancelled {
			r.logger.Info("dlq drop: cancelled by operator",
				r.logger.Str("node_path", env.NodePath),
				r.logger.Str("id", env.ID))
			r.record(env.NodePath, reprocessDropped)
			return ReprocessCommit
		}
	}

	// §36.3 шаг3: TTL от received_at (исходного приёма). Предсказуемый суммарный
	// срок жизни независимо от числа проходов (§36.9). ReceivedAt отсутствует
	// (старое сообщение без поля) → TTL не применяем, обрабатываем дальше.
	ttl := time.Duration(node.DLQTTLSeconds) * time.Second
	if !env.ReceivedAt.IsZero() && r.clock.Now().Sub(env.ReceivedAt) > ttl {
		r.logTTLExpired(ctx, node, env)
		r.logger.Info("dlq drop: ttl expired",
			r.logger.Str("node_path", env.NodePath),
			r.logger.Str("id", env.ID),
			r.logger.Int("ttl_seconds", int(node.DLQTTLSeconds)))
		r.record(env.NodePath, reprocessTTLDropped)
		return ReprocessCommit
	}

	// §36.3 шаг4: статус узла.
	switch node.Status {
	case domain.NodeStatusDisabled:
		r.logger.Info("dlq drop: node disabled",
			r.logger.Str("node_path", env.NodePath),
			r.logger.Str("id", env.ID))
		r.record(env.NodePath, reprocessDropped)
		return ReprocessCommit
	case domain.NodeStatusPaused:
		// Узел на паузе — вернёмся, когда снова активен. Republish без попытки.
		return r.republish(ctx, raw, env, headers, republishParams{
			reason: "node_paused", result: reprocessSkipped, incAttempts: true,
		})
	}

	// §36.4: per-message backoff. Ошибочная отправка переотправляется с
	// next_attempt_at = now + dlq_retry_delay_seconds; до наступления этого
	// момента доставку не пытаемся — republish, сохраняя next_attempt_at и не
	// инкрементируя attempts (реальной попытки не было).
	if next := parseNextAttemptAt(headers); !next.IsZero() && r.clock.Now().Before(next) {
		return r.republish(ctx, raw, env, headers, republishParams{
			reason: "retry_backoff", result: reprocessSkipped, nextAttemptAt: next,
		})
	}

	// §36.3 шаг5: circuit breaker открыт (адрес ещё мёртв) → не пытаемся,
	// иначе fast-fail-churn. Republish в хвост, backoff = интервал прохода.
	// Ошибка проверки → fail-open: пытаемся доставить.
	if r.breaker != nil {
		open, err := r.breaker.IsOpen(ctx, env.NodePath)
		if err != nil {
			r.logger.Warn("dlq breaker check failed; attempting delivery",
				r.logger.Str("node_path", env.NodePath), r.logger.Err(err))
		} else if open {
			return r.republish(ctx, raw, env, headers, republishParams{
				reason: domain.ReasonCircuitBreakerOpen, result: reprocessSkipped, incAttempts: true,
			})
		}
	}

	// §36.3 шаг6: попытка доставки тем же путём, что и основной consumer
	// (retry/breaker/логирование в CH живут внутри SendUsecase.Send).
	in := buildSendInput(node, env)
	logRebuiltTarget(r.logger, "dlq", env, in.TargetURL)
	logRMQOrigin(r.logger, "dlq", env)
	out := r.send.Send(ctx, in)
	if out.StatusCode >= 200 && out.StatusCode < 300 {
		// Успех: «восстановлено». В CH — запись done=true (см. SendUsecase).
		r.logger.Info("dlq reprocess delivered",
			r.logger.Str("node_path", env.NodePath),
			r.logger.Str("id", env.ID),
			r.logger.Int("status", int(out.StatusCode)))
		r.record(env.NodePath, reprocessSucceeded)
		return ReprocessCommit
	}
	// §36.4: неудачная доставка — откладываем следующую попытку на
	// dlq_retry_delay_seconds (минимальный per-message backoff).
	reason := fmt.Sprintf("status=%d attempts=%d err=%s", out.StatusCode, out.Attempts, out.Error)
	delay := time.Duration(node.DLQRetryDelaySeconds) * time.Second
	return r.republish(ctx, raw, env, headers, republishParams{
		reason: reason, result: reprocessFailed, incAttempts: true,
		nextAttemptAt: r.clock.Now().Add(delay),
	})
}

// republishParams — параметры переотправки в хвост DLQ.
type republishParams struct {
	reason        string
	result        string    // метка метрики
	incAttempts   bool      // инкрементировать счётчик проходов (реальная попытка/дефер)
	nextAttemptAt time.Time // zero → заголовок next_attempt_at не ставим
}

// republish переотправляет сообщение в хвост DLQ с обновлёнными заголовками
// (last_attempt_at, attempts, опц. next_attempt_at). На ошибке produce —
// ReprocessRetry (не коммитим, не теряем сообщение).
func (r *DLQReprocessor) republish(ctx context.Context, raw []byte, env Envelope, inHeaders map[string]string, p republishParams) ReprocessResult {
	attempts := parseAttempts(inHeaders)
	if p.incAttempts {
		attempts++
	}
	hdrs := map[string]string{
		"id":              env.ID,
		"node_path":       env.NodePath,
		"orig_topic":      headerOr(inHeaders, "orig_topic", "nexus.async"),
		"reason":          p.reason,
		"last_attempt_at": r.clock.Now().UTC().Format(time.RFC3339Nano),
		"attempts":        strconv.Itoa(attempts),
	}
	if !p.nextAttemptAt.IsZero() {
		hdrs["next_attempt_at"] = p.nextAttemptAt.UTC().Format(time.RFC3339Nano)
	}
	if err := r.dlq.Produce(ctx, r.dlqTopic, env.NodePath, raw, hdrs); err != nil {
		r.logger.ErrorWithOp("dlq republish failed", err, "dlq.reprocess",
			r.logger.Str("node_path", env.NodePath), r.logger.Str("id", env.ID))
		return ReprocessRetry
	}
	r.record(env.NodePath, p.result)
	return ReprocessCommit
}

// logTTLExpired пишет финальную запись в CH-таблицу узла (done=false,
// reason="ttl_expired"), если логирование узла включено. TTL отсчитывается от
// received_at, поэтому даты записи проставляем из исходного приёма.
func (r *DLQReprocessor) logTTLExpired(ctx context.Context, node *domain.Node, env Envelope) {
	if !node.LoggingEnabled {
		return
	}
	rec := &domain.LogRecord{
		ID:   env.ID,
		Type: domain.RootMethodRequestAsync,
		URL:  env.TargetURL,
		// §39: HTTP-глагол → http_method; подпуть passthrough → method.
		HTTPMethod:   env.Method,
		Method:       env.RequestPath,
		Parameters:   extractQuery(env.TargetURL),
		DateCreate:   env.ReceivedAt,
		DateRequest:  env.ReceivedAt,
		DateResponse: r.clock.Now(),
		Status:       0,
		Done:         false,
		Reason:       "ttl_expired",
		Host:         r.host,
		IP:           env.ClientIP,
		// §67: тот же резолвер, что у обычной доставки (send всегда non-nil).
		ClientHost: r.send.hosts.Lookup(env.ClientIP),
		NodeID:     node.ID,
	}
	r.logw.Write(ctx, node.ClickHouseTable, rec)
}

func (r *DLQReprocessor) record(node, result string) {
	if r.metrics != nil {
		r.metrics.DLQReprocessTotal.WithLabelValues(node, result).Inc()
	}
}

// parseAttempts читает счётчик проходов из DLQ-заголовка attempts (0 при
// отсутствии/мусоре/отрицательном значении).
func parseAttempts(h map[string]string) int {
	if h == nil {
		return 0
	}
	n, err := strconv.Atoi(h["attempts"])
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// parseNextAttemptAt читает время следующей попытки из DLQ-заголовка
// next_attempt_at (RFC3339Nano). Пусто/мусор → нулевое время (гейт выключен).
func parseNextAttemptAt(h map[string]string) time.Time {
	if h == nil {
		return time.Time{}
	}
	v := h["next_attempt_at"]
	if v == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return time.Time{}
	}
	return t
}

// headerOr возвращает значение заголовка k или def, если он пуст/отсутствует.
func headerOr(h map[string]string, k, def string) string {
	if h != nil {
		if v := h[k]; v != "" {
			return v
		}
	}
	return def
}
