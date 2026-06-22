// Package clogwire — формат сообщения durable-retry проваленных CH-батчей
// (§38). Нейтральный leaf-пакет: и продьюсер (adapter/out/chlogretry), и
// consumer-handler (adapter/in/kafka) зависят только от него и от domain —
// без межслойных импортов адаптеров друг в друга.
//
// Одно Kafka-сообщение = один (под)батч одной таблицы. Большой батч режется
// на под-батчи под лимит max.message.bytes (Split).
package clogwire

import (
	"encoding/json"
	"fmt"

	"nexus/internal/domain"
)

// Envelope — содержимое одного сообщения nexus.logs.retry: имя таблицы и
// записи лога, которые не удалось вставить в ClickHouse.
type Envelope struct {
	Table string              `json:"table"`
	Logs  []*domain.LogRecord `json:"logs"`
}

// Marshal сериализует конверт в JSON для отправки в Kafka.
func Marshal(table string, logs []*domain.LogRecord) ([]byte, error) {
	b, err := json.Marshal(Envelope{Table: table, Logs: logs})
	if err != nil {
		return nil, fmt.Errorf("clogwire marshal: %w", err)
	}
	return b, nil
}

// Unmarshal разбирает сообщение nexus.logs.retry обратно в конверт.
func Unmarshal(data []byte) (Envelope, error) {
	var e Envelope
	if err := json.Unmarshal(data, &e); err != nil {
		return Envelope{}, fmt.Errorf("clogwire unmarshal: %w", err)
	}
	return e, nil
}

// Split режет батч на под-батчи, каждый из которых при сериализации
// укладывается примерно в maxBytes (лимит Kafka max.message.bytes). Это нужно,
// т.к. батч с логируемыми телами request/response может превысить лимит топика.
//
// Гарантии: ни одна запись не теряется; запись, которая сама по себе больше
// maxBytes, всё равно уходит отдельным под-батчем (Kafka отвергнет такое
// сообщение — это лучше, чем молчаливая потеря всего батча; на практике тела
// ограничены max_body_size узла). maxBytes<=0 → один под-батч (без резки).
func Split(logs []*domain.LogRecord, maxBytes int) [][]*domain.LogRecord {
	if len(logs) == 0 {
		return nil
	}
	if maxBytes <= 0 {
		return [][]*domain.LogRecord{logs}
	}
	var (
		out     [][]*domain.LogRecord
		cur     []*domain.LogRecord
		curSize int
	)
	const overhead = 64 // запас на JSON-обвязку конверта/запятые
	for _, r := range logs {
		sz := recordSize(r)
		if len(cur) > 0 && curSize+sz+overhead > maxBytes {
			out = append(out, cur)
			cur, curSize = nil, 0
		}
		cur = append(cur, r)
		curSize += sz
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// recordSize — приблизительный размер записи в сериализованном виде.
func recordSize(r *domain.LogRecord) int {
	b, err := json.Marshal(r)
	if err != nil {
		return 1024 // не смогли оценить — берём консервативную оценку
	}
	return len(b)
}
