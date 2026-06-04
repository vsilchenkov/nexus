package kafka

import (
	"context"
	"fmt"
	"time"

	kafka "github.com/segmentio/kafka-go"
	"github.com/segmentio/kafka-go/compress"

	"nexus/internal/platform/config"
	"nexus/internal/platform/metrics"
)

// Producer — обёртка над *kafka.Writer с параметрами из §5.3 ТЗ.
type Producer struct {
	w       *kafka.Writer
	metrics *metrics.Metrics // §31: nil-safe; nil → produce-латентность не пишется
}

// ProducerOption — функциональная опция конструктора Producer.
type ProducerOption func(*Producer)

// WithMetrics включает метрику длительности публикации
// (nexus_kafka_produce_duration_seconds по топику, §31).
func WithMetrics(m *metrics.Metrics) ProducerOption {
	return func(p *Producer) { p.metrics = m }
}

// NewProducer создаёт producer без привязки к конкретному топику —
// топик передаётся в каждом сообщении. Это удобно для отправки в
// nexus.async и nexus.async.dlq из одного инстанса.
//
// Параметры:
//
//	acks=all (RequiredAcks=-1) — ждём подтверждения от всех ISR (надёжность).
//	idempotent producer для exactly-once на retry'ах.
//	compression: lz4.
//	max.in.flight=5, retries=MaxInt.
func NewProducer(cfg *config.Config, opts ...ProducerOption) *Producer {
	brokers := splitBrokers(cfg.Kafka.Brokers)
	w := &kafka.Writer{
		Addr:                   kafka.TCP(brokers...),
		Balancer:               &kafka.Hash{}, // key-based partitioning
		RequiredAcks:           kafka.RequireAll,
		Async:                  false,
		Compression:            compress.Lz4,
		BatchSize:              cfg.Kafka.Producer.BatchSize / 1024, // примерно сообщений на батч
		BatchBytes:             int64(cfg.Kafka.Producer.BatchSize),
		BatchTimeout:           time.Duration(cfg.Kafka.Producer.LingerMs) * time.Millisecond,
		WriteTimeout:           time.Duration(cfg.Kafka.Producer.RequestTimeoutMs) * time.Millisecond,
		ReadTimeout:            time.Duration(cfg.Kafka.Producer.RequestTimeoutMs) * time.Millisecond,
		AllowAutoTopicCreation: false,
	}
	p := &Producer{w: w}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Produce отправляет одно сообщение в указанный топик.
// Возвращает ошибку, если producer не смог доставить за
// delivery_timeout_ms.
func (p *Producer) Produce(ctx context.Context, topic, key string, value []byte, headers map[string]string) error {
	hdrs := make([]kafka.Header, 0, len(headers))
	for k, v := range headers {
		hdrs = append(hdrs, kafka.Header{Key: k, Value: []byte(v)})
	}
	msg := kafka.Message{
		Topic:   topic,
		Key:     []byte(key),
		Value:   value,
		Headers: hdrs,
	}
	start := time.Now()
	if err := p.w.WriteMessages(ctx, msg); err != nil {
		return fmt.Errorf("write to %s: %w", topic, err)
	}
	// §31: длительность успешной публикации (на ошибках не пишем — иначе p95
	// смешивает время до таймаута с реальной латентностью).
	if p.metrics != nil {
		p.metrics.KafkaProduceDuration.WithLabelValues(topic).Observe(time.Since(start).Seconds())
	}
	return nil
}

// Close корректно завершает producer (flush + close connections).
func (p *Producer) Close() error {
	return p.w.Close()
}
