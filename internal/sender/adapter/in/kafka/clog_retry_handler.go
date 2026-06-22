package kafka

import (
	"context"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/metrics"
	"nexus/internal/sender/clogwire"
	"nexus/internal/sender/usecase"
)

// ChLogInserter — прямой синхронный INSERT батча в ClickHouse (§38).
// Реализуется chlog.WriterManager.InsertBatch. Определён на стороне consumer'а.
type ChLogInserter interface {
	InsertBatch(ctx context.Context, table string, batch []*domain.LogRecord) error
}

// ChLogRetryHandler — обработчик сообщений nexus.logs.retry (§38): дренит
// проваленные батчи обратно в ClickHouse. Реализует MessageHandler.
//
// Семантика commit'а (см. ConsumerGroup.runOne):
//   - битое/пустое сообщение → HandleAck (commit, не зацикливаемся на яде);
//   - успешный INSERT → HandleAck (commit);
//   - INSERT упал (CH всё ещё лежит) → HandleRetry (НЕ commit) → Kafka
//     передоставит батч позже, когда CH поднимется.
//
// Прямой INSERT (в обход буфера Writer'а) НЕ вызывает retrier при ошибке —
// поэтому цикла produce↔consume не возникает.
type ChLogRetryHandler struct {
	inserter ChLogInserter
	metrics  *metrics.Metrics
	logger   logging.Logger
}

func NewChLogRetryHandler(inserter ChLogInserter, m *metrics.Metrics, logger logging.Logger) *ChLogRetryHandler {
	return &ChLogRetryHandler{inserter: inserter, metrics: m, logger: logger}
}

func (h *ChLogRetryHandler) Handle(ctx context.Context, value []byte, _ map[string]string) usecase.HandleResult {
	env, err := clogwire.Unmarshal(value)
	if err != nil {
		// Битое сообщение — не наша запись/повреждение. Коммитим, чтобы не
		// зациклиться: повтор не поможет.
		h.logger.ErrorWithOp("clog retry: bad message, dropping", err, "kafka.clogRetry.decode")
		return usecase.HandleAck
	}
	if env.Table == "" || len(env.Logs) == 0 {
		return usecase.HandleAck
	}
	if err := h.inserter.InsertBatch(ctx, env.Table, env.Logs); err != nil {
		// CH всё ещё недоступен — не коммитим, Kafka передоставит позже.
		h.logger.Warn("clog retry: insert failed, will redeliver",
			h.logger.Str("table", env.Table),
			h.logger.Int("rows", len(env.Logs)),
			h.logger.Err(err))
		return usecase.HandleRetry
	}
	if h.metrics != nil {
		h.metrics.CHFallbackTotal.WithLabelValues(env.Table, "restored").Add(float64(len(env.Logs)))
	}
	return usecase.HandleAck
}
