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
	ID        string
	Partition int
	Offset    int64
	// Topic — где физически лежит сообщение: основной топик или delay-топик
	// paused-узлов (§3.6). Нужен, чтобы последующий PeekBody читал из того же
	// топика: (partition, offset) уникальны только внутри топика.
	Topic      string
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

// FailedLogsCleaner — удаление истории неудачных прогонов конкретных записей из
// CH-таблицы узла (§79.2). Отдельный узкий интерфейс, потому что его потребитель
// — не только очистка очереди, но и «Повторить все сейчас» (replay): после
// успешной реинъекции строки done=0 оригинала убираются, чтобы запись исчезла из
// «Неудачных доставок» сразу.
type FailedLogsCleaner interface {
	// DeleteFailedRows — удалить строки done=0 записей ids в окне q. Возвращает
	// число ЗАПИСЕЙ, чьи строки удалены (строк уходит больше: у записи столько
	// строк, сколько было прогонов доставки). Пустой ids → (0, nil).
	//
	// Строки done=1 тех же записей НЕ трогаются: история успешной доставки
	// остаётся в журнале логов.
	//
	// Границы: гейт владения таблицей §70.4 (чужая/внешняя таблица →
	// domain.ErrCHForeignDatabase) и СТРОГИЙ фильтр по node_id — послабление
	// §61 (`OR node_id=''`) в разрушающей операции означало бы удаление строк
	// постороннего писателя.
	DeleteFailedRows(ctx context.Context, q LogQuery, ids []string) (uint64, error)
}

// FailedLogsPurger — очистка «Неудачных доставок» узла из ClickHouse-логов
// (§35/§36.10). Реализуется adapter/out/clickhouse.LogReaderCH. Опционален: nil
// при отсутствии ClickHouse.
//
// Очистка неудачных = отмена (qcancel) ID этих записей, чтобы DLQ-репроцессор
// перестал их повторять (FailedIDs → QueueCancelWriter.Cancel), + удаление их
// строк done=0 (чтобы записи исчезли из вида).
//
// §79.2: оба шага работают с ОДНИМ набором ID, поэтому «отменено N» и
// «очищено N» сходятся конструктивно (раньше это были два независимых прохода —
// боевой аудит показывал cancelled=5 при deleted=10).
type FailedLogsPurger interface {
	// FailedIDs — ID недоставленных записей под фильтрами q (§79.1: у записи нет
	// ни одного прогона done=1), до cap; capped=true, если кандидатов было больше.
	FailedIDs(ctx context.Context, q LogQuery, cap int) (ids []string, capped bool, err error)
	FailedLogsCleaner
}
