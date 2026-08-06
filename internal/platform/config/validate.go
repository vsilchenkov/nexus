package config

import (
	"errors"
	"fmt"

	"nexus/internal/domain"
)

// Validate проверяет обязательные поля и допустимые значения.
// Возвращает первую найденную ошибку; для production-сценариев
// сбор всех ошибок не нужен — проще починить и перезапустить.
func Validate(c *Config) error {
	if c == nil {
		return errors.New("nil config")
	}

	// §70.1: идентификатор ноды. Пустой валиден (нода до §70); непустой обязан
	// пройти доменный формат — иначе имя БД ClickHouse не соберётся, а падение
	// случилось бы уже при создании команды, а не на старте.
	if err := domain.InstanceID(c.Instance.ID).Validate(); err != nil {
		return fmt.Errorf("instance.id=%q: %w", c.Instance.ID, err)
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

	// §81.3: границы те же, что у per-node переопределения (domain.Node), иначе
	// глобальная политика могла бы оказаться недостижимой для формы узла.
	// Ноль сюда не доходит — дефолты подставляют 5/30 (см. defaults.go).
	if c.Sender.CircuitBreaker.Threshold < 1 || c.Sender.CircuitBreaker.Threshold > 100 {
		return fmt.Errorf("sender.circuit_breaker.threshold=%d: must be 1..100", c.Sender.CircuitBreaker.Threshold)
	}
	if c.Sender.CircuitBreaker.CooldownSec < 1 || c.Sender.CircuitBreaker.CooldownSec > 3_600 {
		return fmt.Errorf("sender.circuit_breaker.cooldown_sec=%d: must be 1..3600", c.Sender.CircuitBreaker.CooldownSec)
	}

	// §64: границы те же, что у domain.Node.MaxBodySize — иначе форма подставит
	// значение, которое не пройдёт валидацию при сохранении узла.
	if c.Web.NodeDefaultMaxBodySize < 1 || c.Web.NodeDefaultMaxBodySize > 10_000_000 {
		return fmt.Errorf("web.node_default_max_body_size=%d: must be 1..10000000", c.Web.NodeDefaultMaxBodySize)
	}

	return nil
}
