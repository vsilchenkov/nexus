// Package chlogretry — out-адаптер: отправляет проваленный ClickHouse-батч
// в durable-топик Kafka nexus.logs.retry (§38). Реализует chlog.BatchRetrier.
//
// Вызывается ТОЛЬКО из буферного flusher'а chlog.Writer при сбое INSERT в CH
// (CH недоступен). Дренаж обратно в CH делает отдельный consumer-group — здесь
// его нет, поэтому цикла produce↔consume не возникает.
package chlogretry

import (
	"context"
	"fmt"
	"strconv"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/sender/clogwire"
)

// Producer — узкий интерфейс Kafka-продьюсера (consumer-side, CLAUDE.md §3).
// Структурно совпадает с *platform/kafka.Producer.
type Producer interface {
	Produce(ctx context.Context, topic, key string, value []byte, headers map[string]string) error
}

// Retrier продьюсит проваленные батчи в Kafka. maxBytes — порог резки
// под-батчей под Kafka max.message.bytes (0 → без резки).
type Retrier struct {
	producer Producer
	topic    string
	maxBytes int
	logger   logging.Logger
}

// New создаёт Retrier. maxBytes обычно = cfg.Kafka.Topic.MaxMessageBytes с
// небольшим запасом.
func New(producer Producer, topic string, maxBytes int, logger logging.Logger) *Retrier {
	return &Retrier{producer: producer, topic: topic, maxBytes: maxBytes, logger: logger}
}

// Retry отправляет батч в топик retry. Большой батч режется на под-батчи
// (Split), каждый уходит отдельным сообщением с ключом = имя таблицы (порядок
// в пределах таблицы сохраняется — один партишн на ключ). Ошибка любого
// под-батча возвращается вызывающему (chlog.Writer залогирует и учтёт потерю).
func (r *Retrier) Retry(ctx context.Context, table string, batch []*domain.LogRecord) error {
	if len(batch) == 0 {
		return nil
	}
	chunks := clogwire.Split(batch, r.maxBytes)
	for i, chunk := range chunks {
		value, err := clogwire.Marshal(table, chunk)
		if err != nil {
			return fmt.Errorf("retry marshal chunk %d/%d: %w", i+1, len(chunks), err)
		}
		hdrs := map[string]string{"table": table, "rows": strconv.Itoa(len(chunk))}
		if err := r.producer.Produce(ctx, r.topic, table, value, hdrs); err != nil {
			return fmt.Errorf("retry produce chunk %d/%d (%s): %w", i+1, len(chunks), table, err)
		}
	}
	return nil
}
