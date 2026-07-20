// Package kafka — Sender-сторона async-обработки. Запускает N consumer-
// горутин (= partitions), каждая блокирующе читает FetchMessage и
// делегирует Handle в usecase. Commit только после Ack/DLQed.
package kafka

import (
	"context"
	"errors"
	"sync"
	"time"

	kafka "github.com/segmentio/kafka-go"

	"nexus/internal/platform/config"
	kafkapf "nexus/internal/platform/kafka"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/metrics"
	"nexus/internal/platform/safego"
	"nexus/internal/sender/usecase"
)

// headersToMap сворачивает Kafka-заголовки в map[string]string. Kafka в
// принципе допускает несколько значений на один ключ, но для наших служебных
// заголовков (OTel-propagator, id/reason/attempts DLQ) это исключено — берём
// первое значение. Общий helper основного consumer'а и DLQ-sweeper'а.
func headersToMap(hs []kafka.Header) map[string]string {
	m := make(map[string]string, len(hs))
	for _, h := range hs {
		if _, exists := m[h.Key]; !exists {
			m[h.Key] = string(h.Value)
		}
	}
	return m
}

// asyncRetryPause — пауза между повторами обработки ОДНОГО сообщения при
// HandleRetry. Основную выдержку для paused-узлов задаёт usecase
// (pausedRetryAfter, §3.6); здесь — защита от busy-loop в остальных Retry-ветках
// (недоступен PostgreSQL при чтении узла, не записался DLQ).
const asyncRetryPause = time.Second

// messageCommitter — минимальный порт commit'а offset'а одного сообщения.
// Реализуется *kafkapf.Consumer. Выделен, чтобы per-message логика
// (handleMessage) тестировалась юнитами без реальной Kafka.
type messageCommitter interface {
	Commit(ctx context.Context, msg kafka.Message) error
}

// ConsumerGroup — пул из cfg.Kafka.Consumer.Instances consumer-горутин.
type ConsumerGroup struct {
	cfg       *config.Config
	processor messageProcessor
	logger    logging.Logger
	topic     string
	metrics   *metrics.Metrics // §31: nil-safe; nil → in-flight не пишется

	consumers []*kafkapf.Consumer
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

// ConsumerOption — функциональная опция конструктора ConsumerGroup.
type ConsumerOption func(*ConsumerGroup)

// WithMetrics включает метрику nexus_kafka_in_flight (§31).
func WithMetrics(m *metrics.Metrics) ConsumerOption {
	return func(g *ConsumerGroup) { g.metrics = m }
}

func NewConsumerGroup(
	cfg *config.Config,
	topic string,
	processor messageProcessor,
	logger logging.Logger,
	opts ...ConsumerOption,
) *ConsumerGroup {
	g := &ConsumerGroup{
		cfg:       cfg,
		processor: processor,
		logger:    logger,
		topic:     topic,
	}
	for _, o := range opts {
		o(g)
	}
	return g
}

// Start запускает горутины. Возвращается сразу — горутины крутятся в фоне.
// Останавливаются через Stop().
//
// Горутины завязаны на собственный производный контекст, который отменяет Stop.
// Иначе после Close() reader'а FetchMessage возвращает не context.Canceled, а
// «reader closed» — цикл уходит в continue и крутит busy-loop до отмены внешнего
// ctx (как у ChLogRetryConsumer, §38).
func (g *ConsumerGroup) Start(ctx context.Context) {
	cctx, cancel := context.WithCancel(ctx)
	g.cancel = cancel
	instances := g.cfg.Kafka.Consumer.Instances
	if instances <= 0 {
		instances = 1
	}
	for i := 0; i < instances; i++ {
		c := kafkapf.NewConsumer(g.cfg, g.topic)
		g.consumers = append(g.consumers, c)
		g.wg.Add(1)
		go g.runOne(cctx, c, i)
	}
}

func (g *ConsumerGroup) runOne(ctx context.Context, c *kafkapf.Consumer, idx int) {
	defer g.wg.Done()
	defer safego.Recover(g.logger, "sender.kafkaConsumer")
	g.logger.Info("async consumer started",
		g.logger.Str("topic", g.topic),
		g.logger.Int("idx", idx))

	for {
		if ctx.Err() != nil {
			return
		}
		msg, err := c.FetchMessage(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return
			}
			g.logger.Warn("fetch message failed",
				g.logger.Str("topic", g.topic),
				g.logger.Err(err))
			continue
		}

		g.handleMessage(ctx, c, msg)
	}
}

// handleMessage обрабатывает одно сообщение и решает судьбу offset'а.
// Выделен из runOne, чтобы маппинг HandleResult → Commit покрывался юнитами
// без реальной Kafka (fetch-цикл остаётся на integration-тестах).
func (g *ConsumerGroup) handleMessage(ctx context.Context, c messageCommitter, msg kafka.Message) {
	// §31: сообщение прочитано, но ещё не закоммичено — «в полёте».
	g.incInFlight()

	// Извлекаем Kafka headers в map[string]string для OTel-propagator'а
	// (Phase 8.4). Несколько значений на ключ Kafka в принципе допускает,
	// но для propagator-keys это исключено — берём первое.
	hdrs := headersToMap(msg.Headers)
	// §51.9: partition/offset доступны только здесь (usecase их не видит) —
	// на debug видна привязка сообщения к позиции в топике.
	g.logger.Debug("kafka: message fetched",
		g.logger.Str("topic", g.topic),
		g.logger.Int("partition", msg.Partition),
		g.logger.Any("offset", msg.Offset),
		g.logger.Str("key", string(msg.Key)),
		g.logger.Int("size", len(msg.Value)))
	done := g.processWithRetry(ctx, msg, hdrs)
	g.decInFlight() // обработка завершена (committed/retry-left)
	if !done {
		// Прерваны на shutdown — offset не коммитим: сообщение передоставится
		// после рестарта/ребаланса.
		return
	}
	if err := c.Commit(ctx, msg); err != nil {
		g.logger.ErrorWithOp("commit offset failed", err, "kafka.consumer.commit",
			g.logger.Str("topic", g.topic),
			g.logger.Int("partition", msg.Partition))
	}
}

// processWithRetry переобрабатывает ОДНО И ТО ЖЕ сообщение, пока Handle не
// вернёт не-Retry (Ack/DLQed/Requeued). Возвращает false, если прерван ctx.
//
// Почему не «пропустить и вернуться к сообщению позже» (как было до §3.6-fix):
// kafka-go FetchMessage без commit'а двигает внутренний курсор и НЕ
// передоставляет сообщение на том же reader'е — только при rebalance/restart.
// Пропуск означал, что сообщение paused-узла зависало до перезапуска Sender'а, а
// commit СЛЕДУЮЩЕГО сообщения той же партиции прокатывал committed offset мимо
// него — и сообщение терялось навсегда. Retry-in-place сохраняет и сообщение, и
// порядок партиции (ценой head-of-line blocking). Тот же приём, что у
// ChLogRetryConsumer (§38).
func (g *ConsumerGroup) processWithRetry(ctx context.Context, msg kafka.Message, hdrs map[string]string) bool {
	for {
		if g.processor.Handle(ctx, msg.Value, hdrs) != usecase.HandleRetry {
			return true
		}
		// §51.9: повторная обработка того же offset'а — иначе «залипание»
		// партиции на одном сообщении выглядит как молчание consumer'а.
		g.logger.Debug("kafka: retrying message in place",
			g.logger.Str("topic", g.topic),
			g.logger.Int("partition", msg.Partition),
			g.logger.Any("offset", msg.Offset))
		select {
		case <-ctx.Done():
			return false
		case <-time.After(asyncRetryPause):
		}
	}
}

// incInFlight/decInFlight — nil-safe изменение gauge сообщений «в полёте».
func (g *ConsumerGroup) incInFlight() {
	if g.metrics != nil {
		g.metrics.KafkaInFlight.WithLabelValues("sender").Inc()
	}
}

func (g *ConsumerGroup) decInFlight() {
	if g.metrics != nil {
		g.metrics.KafkaInFlight.WithLabelValues("sender").Dec()
	}
}

// Stop корректно завершает все consumer-горутины: отменяет производный
// контекст (чтобы FetchMessage и retry-in-place вышли немедленно), затем
// закрывает reader'ы и дожидается горутин.
func (g *ConsumerGroup) Stop() {
	if g.cancel != nil {
		g.cancel()
	}
	for _, c := range g.consumers {
		_ = c.Close()
	}
	g.wg.Wait()
}

// LagSnapshot — одна точка по консьюмеру (topic/partition → lag).
//
// Partition хранится строкой, потому что kafka-go's ReaderStats.Partition
// тоже строка ("0", "1" или "all" в зависимости от конфигурации).
type LagSnapshot struct {
	Topic     string
	Partition string
	Lag       int64
}

// Snapshot снимает текущее состояние lag со всех инстансов consumer'ов.
// Используется фоновым reporter'ом, который пушит данные в Prometheus.
func (g *ConsumerGroup) Snapshot() []LagSnapshot {
	out := make([]LagSnapshot, 0, len(g.consumers))
	for _, c := range g.consumers {
		s := c.Stats()
		out = append(out, LagSnapshot{
			Topic:     s.Topic,
			Partition: s.Partition,
			Lag:       s.Lag,
		})
	}
	return out
}

// Group возвращает имя consumer-group (для меток метрик).
func (g *ConsumerGroup) Group() string {
	return g.cfg.Kafka.ConsumerGroup
}
