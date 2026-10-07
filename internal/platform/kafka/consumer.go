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

// ConsumerOption настраивает reader.
type ConsumerOption func(*kafka.ReaderConfig)

// WithClientID задаёт client.id соединений reader'а (§113.6). Брокер
// показывает его у участника группы (DescribeGroups), и сторож зависшего
// consumer'а находит по нему партиции своего reader'а. Без опции все reader'ы
// процесса получают общий client.id kafka-go ("<программа>@<хост>").
func WithClientID(id string) ConsumerOption {
	return func(rc *kafka.ReaderConfig) {
		if id != "" {
			rc.Dialer = &kafka.Dialer{ClientID: id, Timeout: 10 * time.Second, DualStack: true}
		}
	}
}

// NewConsumer создаёт consumer для одного топика под основной consumer-group
// (cfg.Kafka.ConsumerGroup). При множественных инстансах партиции
// распределяются автоматически.
func NewConsumer(cfg *config.Config, topic string, opts ...ConsumerOption) *Consumer {
	return NewConsumerWithGroup(cfg, topic, cfg.Kafka.ConsumerGroup, opts...)
}

// NewConsumerWithGroup — как NewConsumer, но с произвольной consumer-group.
// Используется DLQ-репроцессором (§36.2): отдельная группа
// "<group>-dlq-reprocess" читает nexus.async.dlq независимо и не конкурирует с
// основным async-consumer'ом. Пустой groupID → дефолтная группа.
func NewConsumerWithGroup(cfg *config.Config, topic, groupID string, opts ...ConsumerOption) *Consumer {
	if groupID == "" {
		groupID = cfg.Kafka.ConsumerGroup
	}
	brokers := splitBrokers(cfg.Kafka.Brokers)
	rc := kafka.ReaderConfig{
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
	}
	for _, o := range opts {
		o(&rc)
	}
	return &Consumer{r: kafka.NewReader(rc)}
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
