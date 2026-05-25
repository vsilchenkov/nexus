package kafka

import (
	"context"
	"fmt"
	"time"

	kafka "github.com/segmentio/kafka-go"

	"bus/internal/platform/config"
)

// Consumer — обёртка над *kafka.Reader с настройками §5.3 ТЗ.
//
// enable.auto.commit=false: коммитим offset ВРУЧНУЮ после успешной
// обработки сообщения. Это даёт at-least-once при сбое в середине
// обработки (сообщение обработается повторно).
type Consumer struct {
	r       *kafka.Reader
	logger  func(string, ...any)
}

// NewConsumer создаёт consumer для одного топика и одной consumer-group.
// При множественных инстансах партиции распределяются автоматически.
func NewConsumer(cfg *config.Config, topic string) *Consumer {
	brokers := splitBrokers(cfg.Kafka.Brokers)
	r := kafka.NewReader(kafka.ReaderConfig{
		Brokers:           brokers,
		Topic:             topic,
		GroupID:           cfg.Kafka.ConsumerGroup,
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
