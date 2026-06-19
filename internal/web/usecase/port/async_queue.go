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
	// PeekList — первые limit метаданных сообщений node_path (без тела).
	PeekList(ctx context.Context, group, topic, nodePath string, limit, cap int) (PeekListResult, error)
	// PeekBody — тело одного сообщения по физической координате (partition, offset).
	PeekBody(ctx context.Context, topic string, partition int, offset int64) (QueueMessageBody, error)
	// ScanIDs — ID сообщений node_path с фильтром по периоду ReceivedAt ∈ [from,to]
	// (нулевые границы = без фильтра). Для purge (§34.4).
	ScanIDs(ctx context.Context, group, topic, nodePath string, from, to time.Time, cap int) (ScanIDsResult, error)
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

// FailedLogsPurger — очистка «Неудачных доставок» узла из ClickHouse-логов
// (§35/§36). Реализуется adapter/out/clickhouse.LogReaderCH. Опционален: nil при
// отсутствии ClickHouse. table — CH-таблица узла (db.table).
//
// Очистка неудачных = отмена (qcancel) их ID, чтобы DLQ-репроцессор перестал их
// повторять (FailedIDs → QueueCancelWriter.Cancel), + lightweight DELETE записей
// done=0 за окно (чтобы они исчезли из вида). Окно — (sinceMs, untilMs]; нулевые
// границы = всё.
type FailedLogsPurger interface {
	// FailedIDs — уникальные ID записей done=0 за окно (до cap; capped=true, если
	// есть ещё). Для отмены повторной доставки этих сообщений в DLQ.
	FailedIDs(ctx context.Context, table string, sinceMs, untilMs int64, cap int) ([]string, bool, error)
	// DeleteFailed — удалить записи done=0 за окно; возвращает число удалённых.
	DeleteFailed(ctx context.Context, table string, sinceMs, untilMs int64) (uint64, error)
}
