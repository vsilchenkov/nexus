// Package kafka — Sender-сторона async-обработки. Запускает N consumer-
// горутин (= partitions), каждая блокирующе читает FetchMessage и
// делегирует Handle в usecase. Commit только после Ack/DLQed.
package kafka

import (
	"context"
	"errors"
	"sync"

	"nexus/internal/platform/config"
	kafkapf "nexus/internal/platform/kafka"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/metrics"
	"nexus/internal/platform/safego"
	"nexus/internal/sender/usecase"
)

// ConsumerGroup — пул из cfg.Kafka.Consumer.Instances consumer-горутин.
type ConsumerGroup struct {
	cfg       *config.Config
	processor *usecase.AsyncProcessor
	logger    logging.Logger
	topic     string
	metrics   *metrics.Metrics // §31: nil-safe; nil → in-flight не пишется

	consumers []*kafkapf.Consumer
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
	processor *usecase.AsyncProcessor,
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
func (g *ConsumerGroup) Start(ctx context.Context) {
	instances := g.cfg.Kafka.Consumer.Instances
	if instances <= 0 {
		instances = 1
	}
	for i := 0; i < instances; i++ {
		c := kafkapf.NewConsumer(g.cfg, g.topic)
		g.consumers = append(g.consumers, c)
		g.wg.Add(1)
		go g.runOne(ctx, c, i)
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

		// §31: сообщение прочитано, но ещё не закоммичено — «в полёте».
		g.incInFlight()

		// Извлекаем Kafka headers в map[string]string для OTel-propagator'а
		// (Phase 8.4). Несколько значений на ключ Kafka в принципе допускает,
		// но для propagator-keys это исключено — берём первое.
		hdrs := make(map[string]string, len(msg.Headers))
		for _, h := range msg.Headers {
			if _, exists := hdrs[h.Key]; !exists {
				hdrs[h.Key] = string(h.Value)
			}
		}
		res := g.processor.Handle(ctx, msg.Value, hdrs)
		g.decInFlight() // обработка завершена (committed/retry-left)
		switch res {
		case usecase.HandleAck, usecase.HandleDLQed:
			if err := c.Commit(ctx, msg); err != nil {
				g.logger.ErrorWithOp("commit offset failed", err, "kafka.consumer.commit",
					g.logger.Str("topic", g.topic),
					g.logger.Int("partition", msg.Partition))
			}
		case usecase.HandleRetry:
			// Не коммитим — сообщение будет прочитано снова при следующем
			// FetchMessage в этом инстансе либо при rebalance. Для paused
			// это нормальное поведение (§3.6).
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

// Stop корректно завершает все consumer-горутины.
func (g *ConsumerGroup) Stop() {
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
