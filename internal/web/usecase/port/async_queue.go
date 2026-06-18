package port

import (
	"context"
	"time"
)

// AsyncQueuePeeker — read-only peek в топик nexus.async для экрана управления
// очередью узла (§34.4). Реализуется adapter/out/kafkaadmin транзиентным
// kafka.Reader без GroupID (от committed-offset группы Sender до high-watermark,
// фильтр по key=node_path). Опционален: nil при пустом cfg.Kafka.Brokers —
// usecase тогда отдаёт деградацию kafka_available=false.
//
// Интерфейс определён на стороне потребителя (usecase) и содержит только то,
// что usecase реально вызывает (ISP). Все методы ограничены cap'ом сообщений —
// при переполнении возвращается флаг Capped (число/список — нижняя оценка).
type AsyncQueuePeeker interface {
	// PeekDepth — число неконсюмированных сообщений node_path в очереди.
	PeekDepth(ctx context.Context, group, topic, nodePath string, cap int) (PeekDepthResult, error)
	// PeekList — первые limit метаданных сообщений node_path (без тела).
	PeekList(ctx context.Context, group, topic, nodePath string, limit, cap int) (PeekListResult, error)
	// PeekBody — тело одного сообщения по физической координате (partition, offset).
	PeekBody(ctx context.Context, topic string, partition int, offset int64) (QueueMessageBody, error)
	// ScanIDs — ID сообщений node_path с фильтром по периоду ReceivedAt ∈ [from,to]
	// (нулевые границы = без фильтра). Для purge (§34.4).
	ScanIDs(ctx context.Context, group, topic, nodePath string, from, to time.Time, cap int) (ScanIDsResult, error)

	// PeekDLQDepth — число сообщений node_path в DLQ-топике (§34.6). У DLQ нет
	// consumer-группы: читаем последние cap сообщений (от high-cap до high).
	PeekDLQDepth(ctx context.Context, dlqTopic, nodePath string, cap int) (PeekDepthResult, error)
	// PeekDLQList — последние limit неудачных сообщений node_path из DLQ
	// (с reason/last_attempt из Kafka-headers). Тело тянется через PeekBody.
	PeekDLQList(ctx context.Context, dlqTopic, nodePath string, limit, cap int) (DLQListResult, error)
}

// DLQMessageMeta — метаданные одного сообщения DLQ: то же, что у живой очереди,
// плюс причина и время последней попытки (из Kafka-headers, §34.6).
type DLQMessageMeta struct {
	QueueMessageMeta
	Reason        string `json:"reason"`
	LastAttemptAt string `json:"last_attempt_at"`
}

// DLQListResult — последние N неудачных сообщений + признак переполнения cap'а.
type DLQListResult struct {
	Items  []DLQMessageMeta
	Capped bool
}

// PeekDepthResult — глубина очереди узла. Capped=true → Count это нижняя оценка
// (упёрлись в cap, очередь больше).
type PeekDepthResult struct {
	Count  int64
	Capped bool
}

// QueueMessageMeta — метаданные одного сообщения очереди (без тела).
type QueueMessageMeta struct {
	ID         string
	Partition  int
	Offset     int64
	Method     string
	TargetURL  string
	ReceivedAt time.Time
	BodySize   int
}

// PeekListResult — первые N сообщений + признак переполнения cap'а.
type PeekListResult struct {
	Items  []QueueMessageMeta
	Capped bool
}

// QueueMessageBody — тело одного сообщения для ленивой подгрузки.
type QueueMessageBody struct {
	ID        string
	Method    string
	TargetURL string
	Headers   map[string]string
	Body      []byte
}

// ScanIDsResult — ID для purge + признак переполнения cap'а.
type ScanIDsResult struct {
	IDs    []string
	Capped bool
}

// QueueCancelWriter — пишет tombstone'ы отменённых async-сообщений (§34.4).
// Реализуется internal/platform/queuecancel (Redis). Опционален: nil при
// отсутствии Redis. ttl = retention топика (tombstone истекает вместе с
// физическим устареванием сообщения).
type QueueCancelWriter interface {
	Cancel(ctx context.Context, ids []string, ttl time.Duration) (int, error)
}
