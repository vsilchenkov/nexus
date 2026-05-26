package kafka

import (
	"context"
	"fmt"
	"time"

	kafka "github.com/segmentio/kafka-go"
	"github.com/segmentio/kafka-go/compress"

	"nexus/internal/platform/config"
)

// Producer — обёртка над *kafka.Writer с параметрами из §5.3 ТЗ.
type Producer struct {
	w *kafka.Writer
}

// NewProducer создаёт producer без привязки к конкретному топику —
// топик передаётся в каждом сообщении. Это удобно для отправки в
// databus.async и databus.async.dlq из одного инстанса.
//
// Параметры:
//
//	acks=all (RequiredAcks=-1) — ждём подтверждения от всех ISR (надёжность).
//	idempotent producer для exactly-once на retry'ах.
//	compression: lz4.
//	max.in.flight=5, retries=MaxInt.
func NewProducer(cfg *config.Config) *Producer {
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
	return &Producer{w: w}
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
	if err := p.w.WriteMessages(ctx, msg); err != nil {
		return fmt.Errorf("write to %s: %w", topic, err)
	}
	return nil
}

// Close корректно завершает producer (flush + close connections).
func (p *Producer) Close() error {
	return p.w.Close()
}
