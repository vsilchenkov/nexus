package kafka

import (
	"context"
	"fmt"
	"time"

	kafka "github.com/segmentio/kafka-go"

	"nexus/internal/platform/config"
)

// Consumer — обёртка над *kafka.Reader с настройками §5.3 ТЗ.
//
// enable.auto.commit=false: коммитим offset ВРУЧНУЮ после успешной
// обработки сообщения. Это даёт at-least-once при сбое в середине
// обработки (сообщение обработается повторно).
type Consumer struct {
	r *kafka.Reader
}

// NewConsumer создаёт consumer для одного топика под основной consumer-group
// (cfg.Kafka.ConsumerGroup). При множественных инстансах партиции
// распределяются автоматически.
func NewConsumer(cfg *config.Config, topic string) *Consumer {
	return NewConsumerWithGroup(cfg, topic, cfg.Kafka.ConsumerGroup)
}

// NewConsumerWithGroup — как NewConsumer, но с произвольной consumer-group.
// Используется DLQ-репроцессором (§36.2): отдельная группа
// "<group>-dlq-reprocess" читает nexus.async.dlq независимо и не конкурирует с
// основным async-consumer'ом. Пустой groupID → дефолтная группа.
func NewConsumerWithGroup(cfg *config.Config, topic, groupID string) *Consumer {
	if groupID == "" {
		groupID = cfg.Kafka.ConsumerGroup
	}
	brokers := splitBrokers(cfg.Kafka.Brokers)
	r := kafka.NewReader(kafka.ReaderConfig{
		Brokers:           brokers,
		Topic:             topic,
		GroupID:           groupID,
		MinBytes:          cfg.Kafka.Consumer.FetchMinBytes,
		MaxBytes:          cfg.Kafka.Consumer.FetchMaxBytes,
		MaxWait:           500 * time.Millisecond,
		HeartbeatInterval: time.Duration(cfg.Kafka.Consumer.HeartbeatIntervalMs) * time.Millisecond,
		SessionTimeout:    time.Duration(cfg.Kafka.Consumer.SessionTimeoutMs) * time.Millisecond,
		// CommitInterval=0 -> ручной commit через CommitMessages.
		CommitInterval: 0,
		StartOffset:    kafka.FirstOffset,
	})
	return &Consumer{r: r}
}

// FetchMessage блокирует до получения следующего сообщения или отмены ctx.
// Возвращает kafka.Message — вызывающий обрабатывает его и затем
// должен вызвать Commit.
func (c *Consumer) FetchMessage(ctx context.Context) (kafka.Message, error) {
	return c.r.FetchMessage(ctx)
}

// Commit отмечает сообщение обработанным.
func (c *Consumer) Commit(ctx context.Context, msg kafka.Message) error {
	if err := c.r.CommitMessages(ctx, msg); err != nil {
		return fmt.Errorf("commit offset: %w", err)
	}
	return nil
}

// Close корректно завершает consumer.
func (c *Consumer) Close() error {
	return c.r.Close()
}

// Stats возвращает kafka-go ReaderStats — Lag, Topic, Partition.
// Используется для публикации в Prometheus (nexus_kafka_lag).
func (c *Consumer) Stats() kafka.ReaderStats {
	return c.r.Stats()
}
