package config

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"nexus/internal/domain"
)

// dnsSuffixPattern — DNS-суффикс NetBIOS-этапа (§106.5): метки из букв, цифр и
// дефиса через точку. Суффикс дописывается к имени клиента и уходит в
// client_host и в ALTER TABLE … UPDATE дозаполнения, поэтому ни кавычек, ни
// пробелов в нём быть не может. Неизменяемое значение, как envVarPattern.
var dnsSuffixPattern = regexp.MustCompile(`^[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*$`)

// maxNetBIOSSuffixLen — предел длины суффикса: полное имя (15 символов
// NetBIOS-имени + точка + суффикс) обязано уложиться в 253 символа DNS-имени
// (RFC 1035 §2.3.4). Длиннее дозаполнение §106.3 такое имя отбросило бы молча.
const maxNetBIOSSuffixLen = 253 - 16

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

	if err := validateRDNS(&c.Sender.RDNS); err != nil {
		return err
	}
	if err := validatePausedSweep(&c.Sender.PausedSweep, c.Kafka.Topic.RetentionMs); err != nil {
		return err
	}
	if err := validateStallAfter(c.Kafka.Consumer); err != nil {
		return err
	}

	// §64: границы те же, что у domain.Node.MaxBodySize — иначе форма подставит
	// значение, которое не пройдёт валидацию при сохранении узла.
	if c.Web.NodeDefaultMaxBodySize < 1 || c.Web.NodeDefaultMaxBodySize > 10_000_000 {
		return fmt.Errorf("web.node_default_max_body_size=%d: must be 1..10000000", c.Web.NodeDefaultMaxBodySize)
	}

	return nil
}

// validateRDNS — sender.rdns (§67) и его NetBIOS-этап (§106.5). Нули сюда не
// доходят: их заменяют дефолты. Отрицательное значение попадает только из
// явного конфига и означало бы нулевой таймаут или кеш, который истекает сразу.
func validateRDNS(r *SenderRDNSConfig) error {
	for _, f := range []struct {
		key string
		val int
	}{
		{"sender.rdns.timeout_ms", r.TimeoutMs},
		{"sender.rdns.cache_ttl_sec", r.CacheTTLSec},
		{"sender.rdns.negative_ttl_sec", r.NegativeTTLSec},
	} {
		if f.val < 1 {
			return fmt.Errorf("%s=%d: must be >= 1", f.key, f.val)
		}
	}
	nb := r.NetBIOS
	if nb.Port < 1 || nb.Port > 65_535 {
		return fmt.Errorf("sender.rdns.netbios.port=%d: must be 1..65535", nb.Port)
	}
	if nb.TimeoutMs < 1 || nb.TimeoutMs > 60_000 {
		return fmt.Errorf("sender.rdns.netbios.timeout_ms=%d: must be 1..60000", nb.TimeoutMs)
	}
	// Точки по краям допустимы ("vz78.vozovoz.ru." — FQDN-форма): резолвер их
	// обрезает, проверяется то, что реально попадёт в имя.
	if sfx := strings.Trim(nb.Suffix, "."); sfx != "" &&
		(len(sfx) > maxNetBIOSSuffixLen || !dnsSuffixPattern.MatchString(sfx)) {
		return fmt.Errorf("sender.rdns.netbios.suffix=%q: must be a DNS suffix (labels of A-Z, a-z, 0-9, '-' joined by '.'), <= %d chars",
			nb.Suffix, maxNetBIOSSuffixLen)
	}
	return nil
}

// validatePausedSweep — sender.paused_sweep (§113.3): interval ≤ backstop ≤
// retention/4. Backstop меньше тика бессмыслен (раунд на каждом тике — это
// continuous), а больше четверти retention опасен: сообщение на паузе, которое
// переписывается раз в backstop, может протухнуть раньше, чем узел снимут с
// паузы. retention_ms ≤ 0 (без ограничения по времени) верхнюю границу снимает.
// Реальный retention топика может отличаться от конфига — его планировщик
// читает из брокера и ограничивает backstop на лету.
func validatePausedSweep(p *SenderPausedSweepConfig, retentionMs int64) error {
	if p.IntervalSec < 1 {
		return fmt.Errorf("sender.paused_sweep.interval_sec=%d: must be >= 1", p.IntervalSec)
	}
	if p.BackstopSec < p.IntervalSec {
		return fmt.Errorf("sender.paused_sweep.backstop_sec=%d: must be >= interval_sec=%d",
			p.BackstopSec, p.IntervalSec)
	}
	if limit := retentionMs / 4 / 1000; retentionMs > 0 && int64(p.BackstopSec) > limit {
		return fmt.Errorf("sender.paused_sweep.backstop_sec=%d: must be <= kafka.topic.retention_ms/4 (%d s)",
			p.BackstopSec, limit)
	}
	return nil
}

// validateStallAfter — порог сторожа зависшего reader'а (§113.6): 0 выключает
// сторожа, отрицательное значение — опечатка.
func validateStallAfter(k KafkaConsumerSection) error {
	if k.StallAfterSec != nil && *k.StallAfterSec < 0 {
		return fmt.Errorf("kafka.consumer.stall_after_sec=%d: must be >= 0 (0 disables the watchdog)", *k.StallAfterSec)
	}
	return nil
}
