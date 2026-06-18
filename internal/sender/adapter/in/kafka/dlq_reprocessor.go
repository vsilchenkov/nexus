package kafka

import (
	"context"
	"time"

	"nexus/internal/platform/config"
	kafkapf "nexus/internal/platform/kafka"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/metrics"
	"nexus/internal/sender/usecase"
)

// DLQReprocessor — периодический sweeper над nexus.async.dlq (§36.2).
//
// Отдельная consumer-группа "<group>-dlq-reprocess" читает DLQ независимо и не
// конкурирует с основным async-consumer'ом и §35-peek. Раз в interval делает
// один проход: читает до maxScan сообщений (bounded-ожидание fetchTimeout —
// пустой топик не блокирует проход навсегда), для каждого вызывает
// usecase.DLQReprocessor.ProcessMessage и коммитит offset обработанных.
//
// Тик интервала = базовый backoff: повторно опубликованные в хвост копии
// (republish) обрабатываются на СЛЕДУЮЩЕМ проходе, а не сразу — в одном проходе
// каждый id трогаем один раз (§36.4).
type DLQReprocessor struct {
	consumer     *kafkapf.Consumer
	processor    *usecase.DLQReprocessor
	interval     time.Duration
	maxScan      int
	fetchTimeout time.Duration
	metrics      *metrics.Metrics // nil-safe
	logger       logging.Logger
	topic        string
}

// NewDLQReprocessor создаёт sweeper. interval/maxScan ≤ 0 → дефолты (5 мин / 1000).
func NewDLQReprocessor(
	cfg *config.Config,
	processor *usecase.DLQReprocessor,
	interval time.Duration,
	maxScan int,
	m *metrics.Metrics,
	logger logging.Logger,
) *DLQReprocessor {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	if maxScan <= 0 {
		maxScan = 1000
	}
	group := cfg.Kafka.ConsumerGroup + "-dlq-reprocess"
	return &DLQReprocessor{
		consumer:     kafkapf.NewConsumerWithGroup(cfg, cfg.Kafka.DLQTopic, group),
		processor:    processor,
		interval:     interval,
		maxScan:      maxScan,
		fetchTimeout: 2 * time.Second,
		metrics:      m,
		logger:       logger,
		topic:        cfg.Kafka.DLQTopic,
	}
}

// Run — блокирующий периодический цикл (§36.2): первый проход сразу, далее раз
// в interval. Завершается при отмене ctx. Запускается через safego.Go в app.go.
func (r *DLQReprocessor) Run(ctx context.Context) {
	r.logger.Info("dlq reprocessor started",
		r.logger.Str("topic", r.topic),
		r.logger.Str("interval", r.interval.String()),
		r.logger.Int("max_scan", r.maxScan))

	tick := time.NewTicker(r.interval)
	defer tick.Stop()

	r.sweepOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			r.sweepOnce(ctx)
		}
	}
}

// sweepOnce — один проход: дренируем доступные DLQ-сообщения (до maxScan),
// обрабатываем и коммитим. Останавливаемся, встретив сообщение, уже виденное в
// ЭТОМ проходе (догнали собственный republish-хвост → ждём следующего прохода).
func (r *DLQReprocessor) sweepOnce(ctx context.Context) {
	start := time.Now()
	seen := make(map[string]bool)
	scanned, delivered := 0, 0

	for scanned < r.maxScan {
		if ctx.Err() != nil {
			return
		}
		// bounded-ожидание: пустой топик/конец бэклога → таймаут → конец прохода.
		fetchCtx, cancel := context.WithTimeout(ctx, r.fetchTimeout)
		msg, err := r.consumer.FetchMessage(fetchCtx)
		cancel()
		if err != nil {
			break // shutdown (родительский ctx) либо нет сообщений — проход завершён
		}

		hdrs := headersToMap(msg.Headers)
		// §36.4: повторно опубликованные в хвост копии не трогаем в ТЕКУЩЕМ
		// проходе. Встретили уже виденный id → догнали republish-хвост: НЕ
		// коммитим (копия обработается на следующем проходе) и завершаем проход.
		if id := hdrs["id"]; id != "" {
			if seen[id] {
				break
			}
			seen[id] = true
		}
		scanned++

		res := r.processor.ProcessMessage(ctx, msg.Value, hdrs)
		if res == usecase.ReprocessRetry {
			// Транзиентный сбой — не коммитим и прерываем проход, чтобы не
			// закоммитить offset ПОЗА этим сообщением (commit «до и включительно»).
			break
		}
		if err := r.consumer.Commit(ctx, msg); err != nil {
			r.logger.ErrorWithOp("dlq reprocess commit failed", err, "dlq.reprocess.commit",
				r.logger.Str("topic", r.topic))
		}
		delivered++
	}

	if r.metrics != nil {
		r.metrics.DLQReprocessDuration.Observe(time.Since(start).Seconds())
	}
	if delivered > 0 {
		r.logger.Info("dlq reprocess pass done",
			r.logger.Str("topic", r.topic),
			r.logger.Int("processed", delivered))
	}
}

// Stop корректно завершает consumer DLQ-sweeper'а.
func (r *DLQReprocessor) Stop() {
	if r.consumer != nil {
		_ = r.consumer.Close()
	}
}
