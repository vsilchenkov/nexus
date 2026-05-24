package config

import (
	"errors"
	"fmt"
)

// Validate проверяет обязательные поля и допустимые значения.
// Возвращает первую найденную ошибку; для production-сценариев
// сбор всех ошибок не нужен — проще починить и перезапустить.
func Validate(c *Config) error {
	if c == nil {
		return errors.New("nil config")
	}

	if c.Postgres.Host == "" {
		return errors.New("postgres.host is required")
	}
	if c.Postgres.Database == "" {
		return errors.New("postgres.database is required")
	}

	if c.Redis.Host == "" {
		return errors.New("redis.host is required")
	}

	if c.ClickHouse.Host == "" {
		return errors.New("clickhouse.host is required")
	}
	if c.ClickHouse.Database == "" {
		return errors.New("clickhouse.database is required")
	}

	if c.Kafka.Brokers == "" {
		return errors.New("kafka.brokers is required")
	}

	switch c.Web.SessionCookieSamesite {
	case "strict", "lax", "none":
	default:
		return fmt.Errorf("web.session_cookie_samesite=%q: must be one of strict|lax|none", c.Web.SessionCookieSamesite)
	}

	if c.Sentry.Use && c.Sentry.Dsn == "" {
		return errors.New("sentry.use=true but sentry.dsn is empty")
	}

	return nil
}
