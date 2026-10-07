package port

import "context"

// KafkaAdmin — read-only метаданные Kafka-кластера для экрана мониторинга
// (§4.3/§4.5 spec): топики, состояние брокеров, ping. Реализуется адаптером
// adapter/out/kafkaadmin поверх segmentio/kafka-go. Опционально: при пустом
// cfg.Kafka.Brokers в app.go провайдер не создаётся (nil), и usecase отдаёт
// деградацию (kafka_available=false), не падая.
//
// Интерфейс определён на стороне потребителя (usecase) и содержит только то,
// что usecase реально вызывает (ISP).
type KafkaAdmin interface {
	// Topics — список топиков с партициями, RF, оценкой числа сообщений,
	// consumer-группами и их lag, состоянием реплик (§4.3 spec).
	Topics(ctx context.Context) ([]TopicInfo, error)
	// BrokerHealth — агрегат состояния кластера для health-banner (§4.1 spec):
	// число брокеров online/всего, under-replicated и offline партиции.
	BrokerHealth(ctx context.Context) (BrokerHealth, error)
	// Ping — отклик каждого брокера на ApiVersions-запрос (§4.5 spec, §114.4).
	Ping(ctx context.Context) ([]BrokerPing, error)
}

// KafkaGroupLagReader — суммарный lag consumer-группы по топику прямо из
// брокера (§113.7): committed-офсеты группы против watermarks партиций.
//
// Отдельно от KafkaAdmin (ISP): карточке lag нужно одно число по одной группе,
// а не обход всех топиков и групп кластера. Prometheus-метрика lag остаётся
// запасным путём — она запаздывает на интервал публикации Sender'а и пропадает
// вместе с ним.
type KafkaGroupLagReader interface {
	// GroupLag — Σ lag группы по партициям топика. Некоммиченная партиция
	// считается от low watermark: так её и прочитают reader'ы Nexus.
	GroupLag(ctx context.Context, group, topic string) (int64, error)
}

// Роли consumer-групп Nexus на экране Kafka (§113.7). Lag основной группы и
// группы повтора записи логов — отставание обработки; lag paused- и DLQ-групп —
// сообщения, которые ждут по замыслу (пауза узла, повтор по расписанию), и
// тревогой не являются.
const (
	KafkaGroupRoleMain   = "main"
	KafkaGroupRolePaused = "paused"
	KafkaGroupRoleDLQ    = "dlq"
	KafkaGroupRoleRetry  = "retry"
)

// TopicConsumerGroup — consumer-группа топика с суммарным lag и числом
// участников (§4.3 spec). Role (§113.7) — роль группы Nexus (KafkaGroupRole*)
// или пусто у посторонней группы; заполняет usecase по именам из конфига.
type TopicConsumerGroup struct {
	Group    string
	LagTotal int64
	Members  int
	Role     string
}

// TopicInfo — метаданные одного топика (§4.3 spec). MessagesEstimate —
// Σ(high-low watermark) по партициям (точное число Kafka не возвращает).
//
// SizeBytes KafkaAdmin НЕ заполняет: высокоуровневый segmentio/kafka-go не
// отдаёт DescribeLogDirs. Размер приходит отдельным источником — PromMetrics.
// KafkaTopicSizes (JMX-агент брокера, §75), и usecase домешивает его в
// структуру уже после admin-запроса. Без этого источника поле остаётся 0, и UI
// показывает «—».
type TopicInfo struct {
	Name              string
	Partitions        int
	ReplicationFactor int
	SizeBytes         int64
	MessagesEstimate  int64
	RetentionMs       int64
	ConsumerGroups    []TopicConsumerGroup
	UnderReplicated   int
	OfflinePartitions int
}

// BrokerHealth — сводное состояние кластера (§4.1 spec, поле broker_health).
type BrokerHealth struct {
	BrokersTotal              int
	BrokersOnline             int
	UnderReplicatedPartitions int
	OfflinePartitions         int
}

// BrokerPing — результат ping одного брокера (§4.5 spec). Warn непустой при
// повышенной задержке (или иной деградации), OK=false при ошибке подключения.
type BrokerPing struct {
	Addr      string
	ElapsedMs int64
	OK        bool
	Warn      string
}

// KafkaCache — кеш ответов Kafka Admin (топики/брокеры) в Redis с TTL 30с
// (§3.2 spec): экран автообновляется раз в 10с, без кеша это лишняя нагрузка
// на брокеры. Реализуется adapter/out/redis. Get снимает значение в dst
// (указатель); found=false при отсутствии/просрочке ключа. Best-effort: при
// недоступности Redis usecase идёт напрямую в Kafka.
type KafkaCache interface {
	Get(ctx context.Context, key string, dst any) (found bool, err error)
	Set(ctx context.Context, key string, val any) error
}
