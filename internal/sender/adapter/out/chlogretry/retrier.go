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

// Retrier продьюсит проваленные батчи в Kafka. maxBytes — Kafka
// max.message.bytes топика (0 → без резки).
type Retrier struct {
	producer Producer
	topic    string
	maxBytes int
	logger   logging.Logger
}

// New создаёт Retrier. maxBytes = cfg.Kafka.Topic.MaxMessageBytes (лимит топика);
// запас на обвязку Kafka резервируется внутри (chunkLimit), отдельно вычитать
// не нужно.
func New(producer Producer, topic string, maxBytes int, logger logging.Logger) *Retrier {
	return &Retrier{producer: producer, topic: topic, maxBytes: maxBytes, logger: logger}
}

// framingReserve — запас под обвязку Kafka (record-batch header, ключ-сообщения,
// заголовки), которую clogwire.Split НЕ учитывает: он меряет только размер
// JSON-конверта. Без запаса под-батч размером ровно в max.message.bytes после
// фрейминга превышает лимит и отвергается брокером ("Message Size Too Large").
const framingReserve = 128 * 1024

// chunkLimit — порог резки под-батчей: лимит топика за вычетом запаса на фрейминг.
// Для маленьких лимитов (<= запаса; в основном тесты) запас не вычитаем, чтобы не
// уйти в ноль/минус и не выключить резку. maxBytes<=0 пробрасывается как есть
// (Split трактует как «без резки»).
func chunkLimit(maxBytes int) int {
	if maxBytes > framingReserve {
		return maxBytes - framingReserve
	}
	return maxBytes
}

// Retry отправляет батч в топик retry. Большой батч режется на под-батчи
// (Split), каждый уходит отдельным сообщением с ключом = имя таблицы (порядок
// в пределах таблицы сохраняется — один партишн на ключ). Ошибка любого
// под-батча возвращается вызывающему (chlog.Writer залогирует и учтёт потерю).
func (r *Retrier) Retry(ctx context.Context, table string, batch []*domain.LogRecord) error {
	if len(batch) == 0 {
		return nil
	}
	chunks := clogwire.Split(batch, chunkLimit(r.maxBytes))
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
