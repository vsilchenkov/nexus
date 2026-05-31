package config

// applyDefaults заполняет нулевые значения дефолтами из §8.3 ТЗ.
// Применяется после yaml.Unmarshal — пустые поля в YAML получают эти значения.
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
	if c.ClickHouse.FallbackDir == "" {
		c.ClickHouse.FallbackDir = "logs/clickhouse-fallback"
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
		c.Receiver.WriteTimeoutMs = 10000
	}
	if c.Receiver.IdleTimeoutSec == 0 {
		c.Receiver.IdleTimeoutSec = 120
	}
	if c.Receiver.MaxBodyBytes == 0 {
		c.Receiver.MaxBodyBytes = 5_242_880
	}
	if c.Receiver.MaxHeaderBytes == 0 {
		c.Receiver.MaxHeaderBytes = 1_048_576
	}
	if c.Receiver.SenderGRPC.Addr == "" {
		c.Receiver.SenderGRPC.Addr = "sender:9090"
	}
	if c.Receiver.SenderGRPC.PoolSize == 0 {
		c.Receiver.SenderGRPC.PoolSize = 8
	}
	if c.Receiver.SenderGRPC.TimeoutMs == 0 {
		c.Receiver.SenderGRPC.TimeoutMs = 30000
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

	if c.Sender.GRPCAddr == "" {
		c.Sender.GRPCAddr = ":9090"
	}
	if c.Sender.AdminHTTPAddr == "" {
		c.Sender.AdminHTTPAddr = ":9091"
	}
	if c.Sender.GRPCMaxConcurrentStreams == 0 {
		c.Sender.GRPCMaxConcurrentStreams = 1000
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

	if c.Web.HTTPAddr == "" {
		c.Web.HTTPAddr = ":8000"
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
	if c.Web.NodesSoftLimit == 0 {
		c.Web.NodesSoftLimit = 10000
	}
	if c.Web.NodesHardLimit == 0 {
		c.Web.NodesHardLimit = 50000
	}
	if c.Web.APITokenRateLimitPerMin == 0 {
		c.Web.APITokenRateLimitPerMin = 100
	}
	if c.Web.RMQTestRateLimitPerMin == 0 {
		c.Web.RMQTestRateLimitPerMin = 10 // §27.8: POST /api/nodes/test-rmq
	}
	if c.Web.ReceiverURL == "" {
		c.Web.ReceiverURL = "http://receiver:8080"
	}
}
