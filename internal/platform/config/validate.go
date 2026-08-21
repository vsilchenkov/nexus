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

	// §52-доп: порог статуса down. Верхняя граница — здравый смысл: при пороге
	// в сотни отказов бейдж перестаёт что-либо значить.
	if c.Sender.NodeDownThreshold < 1 || c.Sender.NodeDownThreshold > 1_000 {
		return fmt.Errorf("sender.node_down_threshold=%d: must be 1..1000", c.Sender.NodeDownThreshold)
	}

	// §93.5: остановка. Верхние границы — не вкусовщина: пока идёт дренаж и
	// доигрывание, реплика уже выведена из ротации, и вся нагрузка лежит на
	// соседней. Час такого «выката» — это не graceful, это отказ.
	if c.Shutdown.TimeoutSec < 1 || c.Shutdown.TimeoutSec > 3_600 {
		return fmt.Errorf("shutdown.timeout_sec=%d: must be 1..3600", c.Shutdown.TimeoutSec)
	}
	if c.Shutdown.DrainSec > 600 {
		return fmt.Errorf("shutdown.drain_sec=%d: must be <= 600 (negative disables draining)", c.Shutdown.DrainSec)
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

	// §82.1: enforcement-политика keepalive обязана быть НЕ СТРОЖЕ темпа, с
	// которым штатно пингуют клиенты. Иначе gRPC-сервер засчитает их ping'и
	// нарушением и на третьем оборвёт соединение (GOAWAY «too_many_pings»)
	// вместе с идущим по нему sync-вызовом. Симптом — обрыв на 4×keepalive_time
	// с текстом `client_canceled`, то есть выглядит как уход клиента; вычислить
	// его по логам почти невозможно, поэтому ловим на старте.
	//
	// Ноль у клиента означает «не пинговать вообще» (grpc-go подставляет
	// бесконечность), такой клиент нарушителем стать не может — пропускаем.
	if c.Sender.GRPCKeepalive.EnforcementMinTimeSec < 1 || c.Sender.GRPCKeepalive.EnforcementMinTimeSec > 3_600 {
		return fmt.Errorf("sender.grpc_keepalive.enforcement_min_time_sec=%d: must be 1..3600",
			c.Sender.GRPCKeepalive.EnforcementMinTimeSec)
	}
	for _, client := range []struct {
		key  string
		time int
	}{
		{"receiver.sender_grpc.keepalive_time_sec", c.Receiver.SenderGRPC.KeepaliveTimeSec},
		{"web.sender_grpc.keepalive_time_sec", c.Web.SenderGRPC.KeepaliveTimeSec},
	} {
		if client.time > 0 && c.Sender.GRPCKeepalive.EnforcementMinTimeSec > client.time {
			return fmt.Errorf(
				"sender.grpc_keepalive.enforcement_min_time_sec=%d: must not exceed %s=%d",
				c.Sender.GRPCKeepalive.EnforcementMinTimeSec, client.key, client.time)
		}
	}

	// §64: границы те же, что у domain.Node.MaxBodySize — иначе форма подставит
	// значение, которое не пройдёт валидацию при сохранении узла.
	if c.Web.NodeDefaultMaxBodySize < 1 || c.Web.NodeDefaultMaxBodySize > 10_000_000 {
		return fmt.Errorf("web.node_default_max_body_size=%d: must be 1..10000000", c.Web.NodeDefaultMaxBodySize)
	}

	return nil
}
