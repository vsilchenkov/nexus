// Package config — единый конфиг для трёх сервисов (Receiver, Sender, Web).
//
// Структура соответствует §8.3 ТЗ. Каждый бинарь читает общие секции
// (logging, sentry, postgres, redis, clickhouse, kafka) и свою специфичную
// (receiver / sender / web).
package config

// Config — корневая структура конфига.
type Config struct {
	Build      BuildSection      `yaml:"build"`
	Logging    LoggingSection    `yaml:"logging"`
	Sentry     SentrySection     `yaml:"sentry"`
	Otel       OtelSection       `yaml:"otel"`
	Postgres   PostgresSection   `yaml:"postgres"`
	Redis      RedisSection      `yaml:"redis"`
	ClickHouse ClickHouseSection `yaml:"clickhouse"`
	Kafka      KafkaSection      `yaml:"kafka"`
	Prometheus PrometheusSection `yaml:"prometheus"`
	Receiver   ReceiverSection   `yaml:"receiver"`
	Sender     SenderSection     `yaml:"sender"`
	Web        WebSection        `yaml:"web"`
}

// PrometheusSection — адрес Prometheus-сервера, который Web-сервис опрашивает
// для дашбордов метрик панели (§21, §6). Это НЕ scrape-эндпоинт /metrics
// самих сервисов, а источник для query/query_range.
//
// URL пуст → API метрик деградирует: глобальные KPI (входящие/исходящие за
// 24ч), очередь Kafka и per-node throughput недоступны; per-node счётчики и
// перцентили на странице узла продолжают считаться из ClickHouse.
type PrometheusSection struct {
	URL       string `yaml:"url"`
	TimeoutMs int    `yaml:"timeout_ms"`
}

// OtelSection — параметры OpenTelemetry distributed tracing (§16 ТЗ, Phase 8.2).
// При Enable=false весь tracing — no-op (глобальный TracerProvider не
// устанавливается, span'ы не создаются). При Enable=true span'ы
// экспортируются OTLP/HTTP в OtlpEndpoint (по умолчанию http://otel-collector:4318
// для docker-compose; localhost:4318 — для разработчика).
//
// SampleRate=0 → drop all; 1.0 → отправлять каждый. Между — head-based
// ratio-sampler.
type OtelSection struct {
	Enable       bool    `yaml:"enable"`
	OtlpEndpoint string  `yaml:"otlp_endpoint"`
	OtlpInsecure bool    `yaml:"otlp_insecure"`
	SampleRate   float64 `yaml:"sample_rate"`
	ServiceName  string  `yaml:"service_name"`
	Environment  string  `yaml:"environment"`
}

type BuildSection struct {
	ProjectName string `yaml:"project_name"`
	Version     string `yaml:"version"`
}

// LoggingSection — параметры логгера. Поля совпадают с
// github.com/vsilchenkov/logging.Config (см. §14.1 ТЗ).
type LoggingSection struct {
	Level        int    `yaml:"level"`
	OutputInFile bool   `yaml:"output_in_file"`
	Dir          string `yaml:"dir"`
	Debug        bool   `yaml:"debug"`
}

// SentrySection — параметры Sentry. Поля совпадают с
// github.com/vsilchenkov/logging.SentryConfig (см. §14.2 ТЗ).
type SentrySection struct {
	Use              bool    `yaml:"use"`
	Dsn              string  `yaml:"dsn"`
	Environment      string  `yaml:"environment"`
	Level            int     `yaml:"level"`
	AttachStacktrace bool    `yaml:"attach_stacktrace"`
	EnableTracing    bool    `yaml:"enable_tracing"`
	TracesSampleRate float64 `yaml:"traces_sample_rate"`
	Debug            bool    `yaml:"debug"`
}

type PostgresSection struct {
	Host               string `yaml:"host"`
	Port               int    `yaml:"port"`
	Database           string `yaml:"database"`
	User               string `yaml:"user"`
	Password           string `yaml:"password"`
	MaxOpenConns       int    `yaml:"max_open_conns"`
	MaxIdleConns       int    `yaml:"max_idle_conns"`
	ConnMaxLifetimeMin int    `yaml:"conn_max_lifetime_min"`
}

type RedisSection struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
	DB   int    `yaml:"db"`
	// Username — Redis ACL-пользователь (Redis 6+). Пустая строка =
	// дефолтный пользователь (обратная совместимость). Нужен, когда
	// сервер поднят с ACL и default-юзер выключен.
	Username           string `yaml:"username"`
	Password           string `yaml:"password"`
	PoolSize           int    `yaml:"pool_size"`
	MinIdleConns       int    `yaml:"min_idle_conns"`
	DialTimeoutMs      int    `yaml:"dial_timeout_ms"`
	ReadTimeoutMs      int    `yaml:"read_timeout_ms"`
	WriteTimeoutMs     int    `yaml:"write_timeout_ms"`
	NodeTTLSec         int    `yaml:"node_ttl_sec"`
	SessionTTLSec      int    `yaml:"session_ttl_sec"`
	RatelimitWindowSec int    `yaml:"ratelimit_window_sec"`
}

type ClickHouseSection struct {
	Host             string `yaml:"host"`
	Port             int    `yaml:"port"`
	Database         string `yaml:"database"`
	User             string `yaml:"user"`
	Password         string `yaml:"password"`
	BatchSize        int    `yaml:"batch_size"`
	FlushIntervalSec int    `yaml:"flush_interval_sec"`
	BufferMaxSize    int    `yaml:"buffer_max_size"`
	Workers          int    `yaml:"workers"`
	// FallbackDir — каталог для NDJSON-fallback при недоступности CH (§9.4 ТЗ).
	// Пустое значение = fallback отключён, проваленные батчи теряются.
	FallbackDir string `yaml:"fallback_dir"`
}

type KafkaSection struct {
	Brokers       string               `yaml:"brokers"`
	AsyncTopic    string               `yaml:"async_topic"`
	DLQTopic      string               `yaml:"dlq_topic"`
	ConsumerGroup string               `yaml:"consumer_group"`
	Topic         KafkaTopicSection    `yaml:"topic"`
	Producer      KafkaProducerSection `yaml:"producer"`
	Consumer      KafkaConsumerSection `yaml:"consumer"`
}

type KafkaTopicSection struct {
	Partitions        int    `yaml:"partitions"`
	ReplicationFactor int    `yaml:"replication_factor"`
	MinInsyncReplicas int    `yaml:"min_insync_replicas"`
	RetentionMs       int64  `yaml:"retention_ms"`
	RetentionBytes    int64  `yaml:"retention_bytes"`
	SegmentMs         int64  `yaml:"segment_ms"`
	CleanupPolicy     string `yaml:"cleanup_policy"`
	CompressionType   string `yaml:"compression_type"`
	MaxMessageBytes   int    `yaml:"max_message_bytes"`
}

type KafkaProducerSection struct {
	Acks                string `yaml:"acks"`
	CompressionType     string `yaml:"compression_type"`
	LingerMs            int    `yaml:"linger_ms"`
	BatchSize           int    `yaml:"batch_size"`
	BufferMemory        int    `yaml:"buffer_memory"`
	Retries             int    `yaml:"retries"`
	DeliveryTimeoutMs   int    `yaml:"delivery_timeout_ms"`
	EnableIdempotence   bool   `yaml:"enable_idempotence"`
	MaxInFlightRequests int    `yaml:"max_in_flight_requests"`
	RequestTimeoutMs    int    `yaml:"request_timeout_ms"`
}

type KafkaConsumerSection struct {
	AutoOffsetReset        string `yaml:"auto_offset_reset"`
	EnableAutoCommit       bool   `yaml:"enable_auto_commit"`
	FetchMinBytes          int    `yaml:"fetch_min_bytes"`
	FetchMaxBytes          int    `yaml:"fetch_max_bytes"`
	MaxPartitionFetchBytes int    `yaml:"max_partition_fetch_bytes"`
	SessionTimeoutMs       int    `yaml:"session_timeout_ms"`
	HeartbeatIntervalMs    int    `yaml:"heartbeat_interval_ms"`
	MaxPollRecords         int    `yaml:"max_poll_records"`
	MaxPollIntervalMs      int    `yaml:"max_poll_interval_ms"`
	IsolationLevel         string `yaml:"isolation_level"`
	Instances              int    `yaml:"instances"`
}

type ReceiverSection struct {
	HTTPAddr         string `yaml:"http_addr"`
	ReadTimeoutMs    int    `yaml:"read_timeout_ms"`
	WriteTimeoutMs   int    `yaml:"write_timeout_ms"`
	IdleTimeoutSec   int    `yaml:"idle_timeout_sec"`
	MaxBodyBytes     int    `yaml:"max_body_bytes"`
	MaxHeaderBytes   int    `yaml:"max_header_bytes"`
	RateLimitPerNode int    `yaml:"rate_limit_per_node"`
	// MaxHops — лимит переходов запроса через шину (§32). Служебный заголовок
	// X-Nexus-Hops инкрементится на каждом проходе; при достижении лимита
	// запрос отклоняется (508 Loop Detected). 0 → дефолт 5; < 0 → защита выключена.
	MaxHops        int                      `yaml:"max_hops"`
	SenderGRPC     ReceiverSenderGRPCConfig `yaml:"sender_grpc"`
	SwaggerEnabled bool                     `yaml:"swagger_enabled"`
	L2Cache        ReceiverL2CacheConfig    `yaml:"l2_cache"`
	Puller         ReceiverPullerConfig     `yaml:"puller"`
}

// ReceiverPullerConfig — параметры Puller-воркеров RabbitMQAsync (§27.2).
// По умолчанию компонент включён (узлов RabbitMQAsync может не быть — тогда
// reconcile ничего не поднимает). Disabled=true полностью выключает поллинг.
// ReconcileSec — период сверки списка узлов с запущенными воркерами.
type ReceiverPullerConfig struct {
	Disabled     bool `yaml:"disabled"`
	ReconcileSec int  `yaml:"reconcile_sec"`
}

// ReceiverL2CacheConfig — параметры in-memory LRU L2-кеша поверх Redis+PG
// для конфига узлов (§9.2). При Enabled=false слой не подключается и
// чтение идёт напрямую через Redis/PG.
type ReceiverL2CacheConfig struct {
	Enabled    bool `yaml:"enabled"`
	Size       int  `yaml:"size"`
	TTLMs      int  `yaml:"ttl_ms"`
	StaleTTLMs int  `yaml:"stale_ttl_ms"`
}

type ReceiverSenderGRPCConfig struct {
	Addr                string `yaml:"addr"`
	PoolSize            int    `yaml:"pool_size"`
	TimeoutMs           int    `yaml:"timeout_ms"`
	KeepaliveTimeSec    int    `yaml:"keepalive_time_sec"`
	KeepaliveTimeoutSec int    `yaml:"keepalive_timeout_sec"`
}

type SenderSection struct {
	GRPCAddr                 string                 `yaml:"grpc_addr"`
	AdminHTTPAddr            string                 `yaml:"admin_http_addr"`
	GRPCMaxConcurrentStreams uint32                 `yaml:"grpc_max_concurrent_streams"`
	HTTPClient               SenderHTTPClientConfig `yaml:"http_client"`
	Workers                  int                    `yaml:"workers"`
}

type SenderHTTPClientConfig struct {
	TimeoutMs             int `yaml:"timeout_ms"`
	MaxIdleConns          int `yaml:"max_idle_conns"`
	MaxIdleConnsPerHost   int `yaml:"max_idle_conns_per_host"`
	IdleConnTimeoutSec    int `yaml:"idle_conn_timeout_sec"`
	DialTimeoutMs         int `yaml:"dial_timeout_ms"`
	TLSHandshakeTimeoutMs int `yaml:"tls_handshake_timeout_ms"`
}

type WebSection struct {
	HTTPAddr                     string `yaml:"http_addr"`
	SessionCookieName            string `yaml:"session_cookie_name"`
	SessionCookieSecure          bool   `yaml:"session_cookie_secure"`
	SessionCookieSamesite        string `yaml:"session_cookie_samesite"`
	AuditRetentionDays           int    `yaml:"audit_retention_days"`
	ReplayRateLimitPerUserPerMin int    `yaml:"replay_rate_limit_per_user_per_min"`
	NodesSoftLimit               int    `yaml:"nodes_soft_limit"`
	NodesHardLimit               int    `yaml:"nodes_hard_limit"`
	APITokenRateLimitPerMin      int    `yaml:"api_token_rate_limit_per_min"`
	RMQTestRateLimitPerMin       int    `yaml:"rmq_test_rate_limit_per_min"` // §27.8: лимит POST /api/nodes/test-rmq на пользователя
	// LoginRateLimitPerMin — анти-брутфорс /api/auth/login (Phase AUD.4):
	// столько попыток в минуту на IP и отдельно на login. -1 = выключить.
	LoginRateLimitPerMin int  `yaml:"login_rate_limit_per_min"`
	SwaggerEnabled       bool `yaml:"swagger_enabled"`
	// ReceiverURL — base URL Receiver Service (e.g. "http://receiver:8080").
	// Используется для replay-запросов (§7.4.1): Web отправляет реплай через
	// реальный pipeline Receiver, а не через bypass.
	ReceiverURL string `yaml:"receiver_url"`
	// SelfIngressHosts — список «своих» authority (host или host:port) для
	// self-reference валидации target_url узла (§32.2): запрет указывать в
	// target_url адрес собственного ingress шины. Пустой список → при старте
	// заполняется хостом из ReceiverURL; если и он пуст — проверка пропускается.
	SelfIngressHosts []string `yaml:"self_ingress_hosts"`
	// KafkaMonitorRateLimitPerMin — лимит запросов к /api/kafka/* на пользователя
	// (дашборд Kafka-мониторинга, §9 spec). Защита от dashboard-флуда при
	// автообновлении раз в 10с. 0 = без ограничений.
	KafkaMonitorRateLimitPerMin int `yaml:"kafka_monitor_rate_limit_per_min"`
	// KafkaAlerts — пороги жёлтый/красный для health-banner и KPI экрана
	// Kafka-мониторинга (§6 spec). Вычисляются на каждом /api/kafka/overview.
	KafkaAlerts KafkaAlertsSection `yaml:"kafka_alerts_thresholds"`
}

// KafkaAlertsSection — пороги индикации (жёлтый/красный) для экрана
// Kafka-мониторинга (§6 spec). Все значения настраиваемы; дефолты — в
// defaults.go. Используются usecase'ом для расчёта severity health-banner и
// цветовой подсветки KPI; в сам Kafka/Prometheus не пишутся.
type KafkaAlertsSection struct {
	// LagWarning / LagCritical — текущий consumer lag (сообщений).
	LagWarning  int64 `yaml:"lag_warning"`
	LagCritical int64 `yaml:"lag_critical"`
	// LagGrowthCriticalPerSec — прирост lag/сек, держащийся 5 минут → критично.
	LagGrowthCriticalPerSec float64 `yaml:"lag_growth_critical_per_sec"`
	// ErrorRateWarning / ErrorRateCritical — доля ошибок (produced+consumed)
	// за период, в долях единицы (0.001 = 0.1%).
	ErrorRateWarning  float64 `yaml:"error_rate_warning"`
	ErrorRateCritical float64 `yaml:"error_rate_critical"`
	// ProduceLatencyP95WarningMs / CriticalMs — p95 produce latency, мс.
	ProduceLatencyP95WarningMs  float64 `yaml:"produce_latency_p95_warning_ms"`
	ProduceLatencyP95CriticalMs float64 `yaml:"produce_latency_p95_critical_ms"`
}
