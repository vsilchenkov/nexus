// Package nodecache — реализация receiver.port.NodeReader.
//
// Трёхуровневое чтение по §9.2 ТЗ: L2 (in-memory LRU) → Redis → PostgreSQL.
// L2 включается опционально через cfg.Receiver.L2Cache (Phase 7.2),
// см. [L2Reader] в [l2.go] и LRU-структуру в [lru.go].
package nodecache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"

	"nexus/internal/domain"
	"nexus/internal/domain/ackspec"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/safego"
	"nexus/internal/receiver/usecase/port"
)

// Reader реализует port.NodeReader.
type Reader struct {
	redis  *goredis.Client
	pg     *pgxpool.Pool
	cipher *crypto.Cipher
	ttl    time.Duration
	logger logging.Logger

	// inflight дедуплицирует фоновые write-back'и в Redis по ключу узла:
	// при медленном Redis каждый cache-miss иначе порождал бы новую горутину
	// (на 500ms таймаута) — при 500 rps это тысячи горутин.
	inflight sync.Map
}

var _ port.NodeReader = (*Reader)(nil)

func New(redis *goredis.Client, pg *pgxpool.Pool, cipher *crypto.Cipher, ttl time.Duration, logger logging.Logger) *Reader {
	return &Reader{redis: redis, pg: pg, cipher: cipher, ttl: ttl, logger: logger}
}

// nodeKey — ключ Redis: "node:<team_slug>:<path>". Включает team_slug,
// потому что после Phase 10.1 path не глобально уникален.
func nodeKey(teamSlug, path string) string { return "node:" + teamSlug + ":" + path }

// Invalidator — выселение узла из кеша по событию инвалидации (§57). Реализуют
// и Reader (Redis DEL), и L2Reader (LRU + делегирование внутрь).
type Invalidator interface {
	Invalidate(ctx context.Context, teamSlug, path string) error
}

var _ Invalidator = (*Reader)(nil)

// Invalidate удаляет ключ узла из Redis, чтобы следующий Get перечитал свежий
// конфиг из PG (авторитетный источник) и сделал write-back (§57). Пустой Redis
// или пустой teamSlug обрабатываются без падения.
func (r *Reader) Invalidate(ctx context.Context, teamSlug, path string) error {
	if r.redis == nil {
		return nil
	}
	if teamSlug == "" {
		teamSlug = domain.DefaultTeamSlug
	}
	return r.redis.Del(ctx, nodeKey(teamSlug, path)).Err()
}

// Get: сначала Redis, при miss — Postgres. Записывает обратно в Redis
// после fallback. При недоступности Redis — сразу в Postgres, без падения.
func (r *Reader) Get(ctx context.Context, teamSlug, path string) (*domain.Node, error) {
	if teamSlug == "" {
		teamSlug = domain.DefaultTeamSlug
	}
	if n, err := r.getFromRedis(ctx, teamSlug, path); err == nil {
		return n, nil
	} else if !errors.Is(err, domain.ErrNodeNotFound) && !isRedisUnavailable(err) {
		r.logger.Warn("redis read failed, falling back to postgres",
			r.logger.Str("team", teamSlug),
			r.logger.Str("path", path), r.logger.Err(err))
	} else {
		// §51.9: miss/decrypt-fail раньше молча превращались в поход в PG —
		// главное слепое пятно кеша (в т.ч. «старый формат после ротации ключа»).
		r.logger.Debug("node cache: redis miss, reading postgres",
			r.logger.Str("team", teamSlug),
			r.logger.Str("path", path),
			r.logger.Err(err))
	}

	pgStart := time.Now()
	n, err := r.getFromPg(ctx, teamSlug, path)
	if err != nil {
		return nil, err
	}
	r.logger.Debug("node cache: loaded from postgres",
		r.logger.Str("team", teamSlug),
		r.logger.Str("path", path),
		r.logger.Int("duration_ms", int(time.Since(pgStart).Milliseconds())))
	// Best-effort write-back: ошибка не блокирует обработку запроса.
	// Дедуп по ключу: пока один write-back в полёте, повторные cache-miss'ы
	// того же узла не плодят горутины (§30.2: recover обязателен).
	key := nodeKey(teamSlug, path)
	if _, busy := r.inflight.LoadOrStore(key, struct{}{}); !busy {
		// WithoutCancel, а не Background: write-back переживает завершение
		// запроса (иначе отменялся бы вместе с ним), но не теряет его значения —
		// request-id в логах и Sentry-hub (§6 CLAUDE.md).
		wbCtx := context.WithoutCancel(ctx)
		go func() {
			defer r.inflight.Delete(key)
			defer safego.Recover(r.logger, "nodecache.writeBack")
			r.setToRedis(wbCtx, teamSlug, n)
		}()
	}
	return n, nil
}

// Креды в Redis-кеше шифруются тем же AES-256-GCM, что и в PostgreSQL
// (Phase AUD.5): до этого расшифрованный Node маршалился в Redis целиком,
// и plaintext-креды узлов лежали в кеше открытыми. domain.Node за пределами
// adapter'а по-прежнему всегда содержит plaintext (carved-in правило).
func (r *Reader) getFromRedis(ctx context.Context, teamSlug, path string) (*domain.Node, error) {
	data, err := r.redis.Get(ctx, nodeKey(teamSlug, path)).Bytes()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return nil, domain.ErrNodeNotFound
		}
		return nil, err
	}
	var n domain.Node
	if err := json.Unmarshal(data, &n); err != nil {
		return nil, fmt.Errorf("unmarshal node: %w", err)
	}
	// Записи старого формата (plaintext-креды) не пройдут Decrypt — это
	// трактуется как cache-miss с перечитыванием из PG (TTL короткий).
	if n.AuthCredentials, err = r.cipher.Decrypt(n.AuthCredentials); err != nil {
		return nil, fmt.Errorf("decrypt cached auth: %w", err)
	}
	if n.IncomingAuthCredentials, err = r.cipher.Decrypt(n.IncomingAuthCredentials); err != nil {
		return nil, fmt.Errorf("decrypt cached incoming: %w", err)
	}
	return &n, nil
}

func (r *Reader) setToRedis(ctx context.Context, teamSlug string, n *domain.Node) {
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	cp := *n
	var err error
	if cp.AuthCredentials, err = r.cipher.Encrypt(cp.AuthCredentials); err != nil {
		r.logger.Debug("node cache: write-back skipped (encrypt auth)",
			r.logger.Str("team", teamSlug), r.logger.Str("path", cp.Path), r.logger.Err(err))
		return
	}
	if cp.IncomingAuthCredentials, err = r.cipher.Encrypt(cp.IncomingAuthCredentials); err != nil {
		r.logger.Debug("node cache: write-back skipped (encrypt incoming)",
			r.logger.Str("team", teamSlug), r.logger.Str("path", cp.Path), r.logger.Err(err))
		return
	}
	data, err := json.Marshal(&cp)
	if err != nil {
		r.logger.Debug("node cache: write-back skipped (marshal)",
			r.logger.Str("team", teamSlug), r.logger.Str("path", cp.Path), r.logger.Err(err))
		return
	}
	// §51.9: ошибки write-back раньше проглатывались полностью — «узел вечно
	// ходит в PG» было невидимо.
	if err := r.redis.Set(ctx, nodeKey(teamSlug, cp.Path), data, r.ttl).Err(); err != nil {
		r.logger.Debug("node cache: write-back failed",
			r.logger.Str("team", teamSlug), r.logger.Str("path", cp.Path), r.logger.Err(err))
	}
}

// pgSelectNodeByTeamSlugAndPath — резолв через JOIN с teams. Phase 10.1
// поставила UNIQUE(team_id, path); до этого было UNIQUE(path), теперь
// без указания команды узел не найти однозначно.
const pgSelectNodeByTeamSlugAndPath = `
SELECT
	n.id, n.path, n.root_method,
	n.url_mode, n.target_url, n.url_param_name, n.url_allowed_hosts,
	n.auth_type, n.auth_credentials,
	n.auth_dynamic_source, n.auth_dynamic_field, n.auth_dynamic_strip_prefix,
	n.incoming_auth_type, n.incoming_auth_credentials,
	n.webhook_signature_header, n.webhook_signature_prefix,
	n.forward_headers, n.timeout_ms, n.retry_count, n.retry_backoff_ms,
	n.clickhouse_table, n.status, n.team_id,
	n.log_request_body, n.log_response_body, n.log_headers,
	n.logging_enabled, n.max_body_size_enabled, n.max_body_size,
	n.path_passthrough,
	n.created_at, n.updated_at,
	n.incoming_method, n.outgoing_method,
	n.circuit_breaker_threshold, n.circuit_breaker_cooldown_sec,
	n.async_ack_spec
FROM nodes n
JOIN teams t ON t.id = n.team_id
WHERE t.slug = $1 AND n.path = $2`

func (r *Reader) getFromPg(ctx context.Context, teamSlug, path string) (*domain.Node, error) {
	row := r.pg.QueryRow(ctx, pgSelectNodeByTeamSlugAndPath, teamSlug, path)

	var n domain.Node
	var rootMethod, urlMode, authType, authDynSrc, incomingAuth, status string
	var incomingMethod, outgoingMethod string
	var encAuth, encInc string
	var created, updated time.Time
	// §81.3: NULL = «политика из конфигурации», поэтому указатели.
	var cbThreshold, cbCooldown *int32
	// §83: NULL = «отвечать как раньше».
	var ackRaw []byte

	err := row.Scan(
		&n.ID, &n.Path, &rootMethod,
		&urlMode, &n.TargetURL, &n.URLParamName, &n.URLAllowedHosts,
		&authType, &encAuth,
		&authDynSrc, &n.AuthDynamicField, &n.AuthDynamicStripPrefix,
		&incomingAuth, &encInc,
		&n.WebhookSignatureHeader, &n.WebhookSignaturePrefix,
		&n.ForwardHeaders, &n.TimeoutMs, &n.RetryCount, &n.RetryBackoffMs,
		&n.ClickHouseTable, &status, &n.TeamID,
		&n.LogRequestBody, &n.LogResponseBody, &n.LogHeaders,
		&n.LoggingEnabled, &n.MaxBodySizeEnabled, &n.MaxBodySize,
		&n.PathPassthrough,
		&created, &updated,
		&incomingMethod, &outgoingMethod,
		&cbThreshold, &cbCooldown,
		&ackRaw,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNodeNotFound
		}
		return nil, fmt.Errorf("scan node: %w", err)
	}

	// §83: битая спека в колонке НЕ роняет резолв узла — иначе один узел с
	// кривым JSON (ручная правка SQL на бою) остановил бы весь свой трафик.
	// Деградируем до прежнего ответа {"result":true,"id":…} и пишем warn: это
	// повод чинить настройку, а не наблюдать. В Web та же ситуация — ошибка
	// чтения: там её видит оператор в интерфейсе.
	if len(ackRaw) > 0 {
		var spec ackspec.Spec
		if uerr := json.Unmarshal(ackRaw, &spec); uerr != nil {
			r.logger.Warn("node async_ack_spec is not valid json, ignoring",
				r.logger.Str("op", "nodecache.getFromPg"),
				r.logger.Str("team", teamSlug),
				r.logger.Str("path", path),
				r.logger.Err(uerr))
		} else {
			n.AsyncAck = &spec
		}
	}

	// §81.3: NULL в БД → 0 в домене → «использовать глобальную политику».
	if cbThreshold != nil {
		n.CircuitBreakerThreshold = *cbThreshold
	}
	if cbCooldown != nil {
		n.CircuitBreakerCooldownSec = *cbCooldown
	}

	n.RootMethod = domain.RootMethod(rootMethod)
	n.IncomingMethod = domain.HTTPMethod(incomingMethod)
	n.OutgoingMethod = domain.HTTPMethod(outgoingMethod)
	n.URLMode = domain.URLMode(urlMode)
	n.AuthType = domain.AuthType(authType)
	n.AuthDynamicSource = domain.AuthDynSource(authDynSrc)
	n.IncomingAuthType = domain.IncomingAuthType(incomingAuth)
	n.Status = domain.NodeStatus(status)
	n.CreatedAt = created
	n.UpdatedAt = updated

	n.AuthCredentials, err = r.cipher.Decrypt(encAuth)
	if err != nil {
		return nil, fmt.Errorf("decrypt auth: %w", err)
	}
	n.IncomingAuthCredentials, err = r.cipher.Decrypt(encInc)
	if err != nil {
		return nil, fmt.Errorf("decrypt incoming: %w", err)
	}

	if n.URLAllowedHosts == nil {
		n.URLAllowedHosts = []string{}
	}
	if n.ForwardHeaders == nil {
		n.ForwardHeaders = []string{}
	}
	return &n, nil
}

// isRedisUnavailable — все случаи, которые сводятся к «Redis сейчас лежит,
// нужно идти в Postgres напрямую» (см. §9.4 ТЗ): сетевые ошибки, таймауты,
// закрытый клиент. Остальное (например, битый JSON в кеше) — НЕ
// «недоступность», такие ошибки логируются warn'ом в Get.
func isRedisUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, goredis.ErrClosed) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}
