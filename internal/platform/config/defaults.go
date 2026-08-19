package config

// defaultGRPCMaxMessageBytes — дефолтный лимит размера одного gRPC-сообщения
// Receiver↔Sender (оба направления), 64 МиБ. Дефолт gRPC (4 МиБ) мал для больших
// тел: ответ апстрима на десятки МБ → ResourceExhausted. Применяется и к клиенту
// Receiver (`sender_grpc.max_message_bytes`), и к серверу Sender
// (`grpc_max_message_bytes`).
const defaultGRPCMaxMessageBytes = 64 * 1024 * 1024

// defaultMaxAsyncBodyBytes — дефолтный потолок тела для async-приёма (32 МиБ).
// Согласован с kafka.topic.max_message_bytes 64 МиБ: 32 МиБ × 1.33 (base64
// async-конверта) ≈ 42.6 МиБ + запас на заголовки конверта.
const defaultMaxAsyncBodyBytes = 32 * 1024 * 1024

// defaultTrustedProxies — дефолтный список сетей, чьи X-Forwarded-For
// принимаются на веру (Phase AUD.5): loopback + приватные диапазоны
// (RFC1918 / IPv6 ULA). Покрывает docker-compose (Web-proxy → Receiver),
// но отсекает подделку IP внешними клиентами из интернета.
func defaultTrustedProxies() []string {
	return []string{
		"127.0.0.0/8", "::1/128",
		"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7",
	}
}

// applyDefaults заполняет нулевые значения дефолтами из §8.3 ТЗ.
// Применяется после yaml.Unmarshal — пустые поля в YAML получают эти значения.
//
// Длина функции осознанна: это линейный список «поле → значение по умолчанию»
// без единого ветвления по данным. Разбиение по секциям добавило бы уровней
// косвенности, а забытый дефолт стало бы искать труднее — сейчас весь набор
// виден одним поиском по файлу.
func applyDefaults(c *Config) {
	if c.Logging.Level == 0 {
		c.Logging.Level = 4 // info
	}
	if c.Sentry.Level == 0 {
		c.Sentry.Level = 2 // error
	}
	if c.Sentry.TracesSampleRate == 0 {
		c.Sentry.TracesSampleRate = 0.1
	}

	if c.Postgres.Port == 0 {
		c.Postgres.Port = 5432
	}
	if c.Postgres.MaxOpenConns == 0 {
		c.Postgres.MaxOpenConns = 25
	}
	if c.Postgres.MaxIdleConns == 0 {
		c.Postgres.MaxIdleConns = 5
	}
	if c.Postgres.ConnMaxLifetimeMin == 0 {
		c.Postgres.ConnMaxLifetimeMin = 30
	}

	if c.Redis.Port == 0 {
		c.Redis.Port = 6379
	}
	if c.Redis.PoolSize == 0 {
		c.Redis.PoolSize = 50
	}
	if c.Redis.MinIdleConns == 0 {
		c.Redis.MinIdleConns = 10
	}
	if c.Redis.DialTimeoutMs == 0 {
		c.Redis.DialTimeoutMs = 2000
	}
	if c.Redis.ReadTimeoutMs == 0 {
		c.Redis.ReadTimeoutMs = 500
	}
	if c.Redis.WriteTimeoutMs == 0 {
		c.Redis.WriteTimeoutMs = 500
	}
	if c.Redis.NodeTTLSec == 0 {
		c.Redis.NodeTTLSec = 300
	}
	if c.Redis.SessionTTLSec == 0 {
		c.Redis.SessionTTLSec = 86400
	}
	if c.Redis.RatelimitWindowSec == 0 {
		c.Redis.RatelimitWindowSec = 60
	}

	if c.ClickHouse.Port == 0 {
		c.ClickHouse.Port = 9000
	}
	if c.ClickHouse.BatchSize == 0 {
		c.ClickHouse.BatchSize = 500
	}
	if c.ClickHouse.FlushIntervalSec == 0 {
		c.ClickHouse.FlushIntervalSec = 5
	}
	if c.ClickHouse.BufferMaxSize == 0 {
		c.ClickHouse.BufferMaxSize = 100000
	}
	if c.ClickHouse.Workers == 0 {
		c.ClickHouse.Workers = 2
	}

	if c.Prometheus.TimeoutMs == 0 {
		c.Prometheus.TimeoutMs = 5000
	}

	if c.Kafka.AsyncTopic == "" {
		c.Kafka.AsyncTopic = "nexus.async"
	}
	if c.Kafka.DLQTopic == "" {
		c.Kafka.DLQTopic = "nexus.async.dlq"
	}
	if c.Kafka.RetryTopic == "" {
		c.Kafka.RetryTopic = "nexus.logs.retry"
	}
	if c.Kafka.PausedTopic == "" {
		c.Kafka.PausedTopic = "nexus.async.paused"
	}
	if c.Kafka.ConsumerGroup == "" {
		c.Kafka.ConsumerGroup = "nexus-sender"
	}

	if c.Receiver.HTTPAddr == "" {
		c.Receiver.HTTPAddr = ":8080"
	}
	if c.Receiver.ReadTimeoutMs == 0 {
		c.Receiver.ReadTimeoutMs = 10000
	}
	if c.Receiver.WriteTimeoutMs == 0 {
		// Должен покрывать максимальный timeout_ms узла (600с) + запас: net/http
		// WriteTimeout отсчитывается от чтения заголовков запроса и включает всё
		// время работы handler'а. Меньше — и долгий sync-запрос рвётся самим
		// Receiver'ом до записи ответа (Web-прокси видит EOF, клиент — 502).
		c.Receiver.WriteTimeoutMs = 610000
	}
	if c.Receiver.IdleTimeoutSec == 0 {
		c.Receiver.IdleTimeoutSec = 120
	}
	if c.Receiver.MaxBodyBytes == 0 {
		c.Receiver.MaxBodyBytes = 5_242_880
	}
	// §43-rev/§68: async-приём ограничен отдельно — его тело едет в Kafka в
	// base64 (+33%), и потолок задаёт брокер, а не память Receiver'а. min с
	// MaxBodyBytes обязателен: у конфига без этого ключа и с меньшим общим
	// лимитом async не должен вдруг принимать БОЛЬШЕ, чем sync.
	if c.Receiver.MaxAsyncBodyBytes == 0 {
		c.Receiver.MaxAsyncBodyBytes = min(defaultMaxAsyncBodyBytes, c.Receiver.MaxBodyBytes)
	}
	if c.Receiver.MaxHeaderBytes == 0 {
		c.Receiver.MaxHeaderBytes = 1_048_576
	}
	// §32: hop-лимит против зацикливания. 0 (не задан) → дефолт 5; < 0 → выкл.
	if c.Receiver.MaxHops == 0 {
		c.Receiver.MaxHops = 5
	}
	// §93.5: остановка сервиса. Дефолты подобраны так, чтобы одиночная
	// установка вела себя как раньше (30 с на доигрывание), а профиль двух
	// реплик получал паузу дренажа без правки конфига.
	if c.Shutdown.DrainSec == 0 {
		c.Shutdown.DrainSec = 5
	}
	if c.Shutdown.TimeoutSec == 0 {
		c.Shutdown.TimeoutSec = 30
	}

	if c.Receiver.SenderGRPC.Addr == "" {
		c.Receiver.SenderGRPC.Addr = "sender:9190"
	}
	if c.Receiver.SenderGRPC.PoolSize == 0 {
		c.Receiver.SenderGRPC.PoolSize = 8
	}
	// Мёртвый параметр (см. godoc ReceiverSenderGRPCConfig.TimeoutMs): дедлайн
	// gRPC-вызову никто не ставит. Дефолт сохранён, чтобы значение в конфиге и в
	// прогретой структуре не расходилось; на поведение не влияет.
	if c.Receiver.SenderGRPC.TimeoutMs == 0 {
		c.Receiver.SenderGRPC.TimeoutMs = 30000
	}
	if c.Receiver.SenderGRPC.MaxMessageBytes == 0 {
		c.Receiver.SenderGRPC.MaxMessageBytes = defaultGRPCMaxMessageBytes
	}

	if c.Receiver.L2Cache.Size == 0 {
		c.Receiver.L2Cache.Size = 1000
	}
	if c.Receiver.L2Cache.TTLMs == 0 {
		c.Receiver.L2Cache.TTLMs = 2000
	}
	if c.Receiver.L2Cache.StaleTTLMs == 0 {
		c.Receiver.L2Cache.StaleTTLMs = 60000
	}

	// §27: Puller включён по умолчанию; узлов RabbitMQAsync может не быть —
	// тогда reconcile просто ничего не поднимает.
	if c.Receiver.Puller.ReconcileSec == 0 {
		c.Receiver.Puller.ReconcileSec = 15
	}

	if c.Sender.GRPCAddr == "" {
		c.Sender.GRPCAddr = ":9190"
	}
	if c.Sender.AdminHTTPAddr == "" {
		c.Sender.AdminHTTPAddr = ":9091"
	}
	if c.Sender.GRPCMaxConcurrentStreams == 0 {
		c.Sender.GRPCMaxConcurrentStreams = 1000
	}
	if c.Sender.GRPCMaxMessageBytes == 0 {
		c.Sender.GRPCMaxMessageBytes = defaultGRPCMaxMessageBytes
	}
	if c.Sender.HTTPClient.TimeoutMs == 0 {
		c.Sender.HTTPClient.TimeoutMs = 30000
	}
	if c.Sender.HTTPClient.MaxIdleConns == 0 {
		c.Sender.HTTPClient.MaxIdleConns = 100
	}
	if c.Sender.HTTPClient.MaxIdleConnsPerHost == 0 {
		c.Sender.HTTPClient.MaxIdleConnsPerHost = 20
	}
	if c.Sender.HTTPClient.IdleConnTimeoutSec == 0 {
		c.Sender.HTTPClient.IdleConnTimeoutSec = 90
	}
	if c.Sender.Workers == 0 {
		c.Sender.Workers = 50
	}
	// §36: авто-репроцессор DLQ включён по умолчанию (Disabled=false).
	if c.Sender.Reprocessor.IntervalSec == 0 {
		// 60с — проход раз в минуту: позволяет per-node dlq_retry_delay_seconds
		// опускать вплоть до ~1 мин (эффективная пауза = max(interval, delay)).
		c.Sender.Reprocessor.IntervalSec = 60
	}
	if c.Sender.Reprocessor.MaxScan == 0 {
		c.Sender.Reprocessor.MaxScan = 1000
	}
	// §3.6: sweeper delay-топика paused-узлов включён по умолчанию.
	if c.Sender.PausedSweep.IntervalSec == 0 {
		// 30с — прежняя выдержка pausedRetryAfter: столько же ждёт сообщение
		// после снятия паузы, прежде чем sweeper перечитает статус узла.
		c.Sender.PausedSweep.IntervalSec = 30
	}
	if c.Sender.PausedSweep.MaxScan == 0 {
		c.Sender.PausedSweep.MaxScan = 1000
	}
	// §67: reverse-DNS резолв client_host включён по умолчанию (Disabled=false).
	if c.Sender.RDNS.TimeoutMs == 0 {
		c.Sender.RDNS.TimeoutMs = 2000
	}
	if c.Sender.RDNS.CacheTTLSec == 0 {
		c.Sender.RDNS.CacheTTLSec = 3600
	}
	if c.Sender.RDNS.NegativeTTLSec == 0 {
		c.Sender.RDNS.NegativeTTLSec = 600
	}
	// §81.3: политика circuit breaker'а. Значения те же, что были литералами в
	// коде до §81 — обновление конфигурацию не меняет.
	if c.Sender.CircuitBreaker.Threshold == 0 {
		c.Sender.CircuitBreaker.Threshold = 5
	}
	if c.Sender.CircuitBreaker.CooldownSec == 0 {
		c.Sender.CircuitBreaker.CooldownSec = 30
	}
	// §82.1: enforcement-политика keepalive gRPC-сервера. 5 секунд с запасом
	// меньше клиентских 30 (и меньше минимума grpc-go в 10 с, ниже которого
	// клиент пинговать не станет), так что штатные ping'и нарушением не считаются.
	// DenyPingWithoutStream дефолта не имеет намеренно: нужное значение —
	// «разрешать» = zero value (см. godoc SenderGRPCKeepaliveConfig).
	if c.Sender.GRPCKeepalive.EnforcementMinTimeSec == 0 {
		c.Sender.GRPCKeepalive.EnforcementMinTimeSec = 5
	}

	if c.Web.HTTPAddr == "" {
		c.Web.HTTPAddr = ":8000"
	}
	// §55: адрес Sender'а для dry-run в реальном режиме. Addr НЕ подставляем по
	// умолчанию: пустой = реальный режим выключен, и это правильный дефолт —
	// dry-run обязан ходить в тот же Sender, что обслуживает боевой трафик, а
	// угадать его за оператора нельзя (в dev Web часто живёт без Sender'а). Пул
	// маленький: dry-run — ручное действие, не горячий путь.
	if c.Web.SenderGRPC.PoolSize == 0 {
		c.Web.SenderGRPC.PoolSize = 2
	}
	// Тоже мёртвый (тот же тип и тот же grpcsender без дедлайна).
	if c.Web.SenderGRPC.TimeoutMs == 0 {
		c.Web.SenderGRPC.TimeoutMs = 30000
	}
	if c.Web.SenderGRPC.MaxMessageBytes == 0 {
		c.Web.SenderGRPC.MaxMessageBytes = defaultGRPCMaxMessageBytes
	}
	if c.Web.IdleTimeoutSec == 0 {
		c.Web.IdleTimeoutSec = 120
	}
	if c.Web.SessionCookieName == "" {
		c.Web.SessionCookieName = "nexus_session"
	}
	if c.Web.SessionCookieSamesite == "" {
		c.Web.SessionCookieSamesite = "strict"
	}
	if c.Web.AuditRetentionDays == 0 {
		c.Web.AuditRetentionDays = 365
	}
	if c.Web.ReplayRateLimitPerUserPerMin == 0 {
		c.Web.ReplayRateLimitPerUserPerMin = 10
	}
	if c.Web.ReplayPeriodRateLimitPerUserPerMin == 0 {
		c.Web.ReplayPeriodRateLimitPerUserPerMin = 60
	}
	if c.Web.NodesSoftLimit == 0 {
		c.Web.NodesSoftLimit = 10000
	}
	if c.Web.NodesHardLimit == 0 {
		c.Web.NodesHardLimit = 50000
	}
	if c.Web.NodeDefaultMaxBodySize == 0 {
		c.Web.NodeDefaultMaxBodySize = 50000 // §64
	}
	if c.Web.APITokenRateLimitPerMin == 0 {
		c.Web.APITokenRateLimitPerMin = 100
	}
	if c.Web.RMQTestRateLimitPerMin == 0 {
		c.Web.RMQTestRateLimitPerMin = 10 // §27.8: POST /api/nodes/test-rmq
	}
	if c.Web.InstanceProbeTimeoutMs == 0 {
		c.Web.InstanceProbeTimeoutMs = 3000 // §73: проба соседнего инстанса
	}
	if c.Web.InstanceProbeRateLimitPerMin == 0 {
		c.Web.InstanceProbeRateLimitPerMin = 30 // §73: POST /api/instances/*
	}
	if c.Web.LoginRateLimitPerMin == 0 {
		c.Web.LoginRateLimitPerMin = 10 // Phase AUD.4: анти-брутфорс /api/auth/login
	}
	if c.Web.PasswordResetRateLimitPerMin == 0 {
		c.Web.PasswordResetRateLimitPerMin = 5 // §88.4.4
	}
	if len(c.Receiver.TrustedProxies) == 0 {
		c.Receiver.TrustedProxies = defaultTrustedProxies()
	}
	if len(c.Web.TrustedProxies) == 0 {
		c.Web.TrustedProxies = defaultTrustedProxies()
	}
	if c.Web.ReceiverURL == "" {
		c.Web.ReceiverURL = "http://receiver:8080"
	}
	if c.Web.KafkaMonitorRateLimitPerMin == 0 {
		c.Web.KafkaMonitorRateLimitPerMin = 60 // §9 spec: лимит /api/kafka/* на пользователя
	}
	applyKafkaAlertsDefaults(&c.Web.KafkaAlerts)
}

// applyKafkaAlertsDefaults — пороги индикации экрана Kafka-мониторинга (§6 spec).
// Дефолты совпадают с таблицей порогов ТЗ; нулевое значение поля → дефолт.
func applyKafkaAlertsDefaults(k *KafkaAlertsSection) {
	if k.LagWarning == 0 {
		k.LagWarning = 100
	}
	if k.LagCritical == 0 {
		k.LagCritical = 1000
	}
	if k.LagGrowthCriticalPerSec == 0 {
		k.LagGrowthCriticalPerSec = 50
	}
	if k.ErrorRateWarning == 0 {
		k.ErrorRateWarning = 0.001 // 0.1%
	}
	if k.ErrorRateCritical == 0 {
		k.ErrorRateCritical = 0.01 // 1%
	}
	if k.ProduceLatencyP95WarningMs == 0 {
		k.ProduceLatencyP95WarningMs = 100
	}
	if k.ProduceLatencyP95CriticalMs == 0 {
		k.ProduceLatencyP95CriticalMs = 500
	}
}
