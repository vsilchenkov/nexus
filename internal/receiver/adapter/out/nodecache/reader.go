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
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"

	"nexus/internal/domain"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
	"nexus/internal/receiver/usecase/port"
)

// Reader реализует port.NodeReader.
type Reader struct {
	redis  *goredis.Client
	pg     *pgxpool.Pool
	cipher *crypto.Cipher
	ttl    time.Duration
	logger logging.Logger
}

var _ port.NodeReader = (*Reader)(nil)

func New(redis *goredis.Client, pg *pgxpool.Pool, cipher *crypto.Cipher, ttl time.Duration, logger logging.Logger) *Reader {
	return &Reader{redis: redis, pg: pg, cipher: cipher, ttl: ttl, logger: logger}
}

// nodeKey — ключ Redis: "node:<team_slug>:<path>". Включает team_slug,
// потому что после Phase 10.1 path не глобально уникален.
func nodeKey(teamSlug, path string) string { return "node:" + teamSlug + ":" + path }

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
	}

	n, err := r.getFromPg(ctx, teamSlug, path)
	if err != nil {
		return nil, err
	}
	// Best-effort write-back: ошибка не блокирует обработку запроса.
	go r.setToRedis(context.Background(), teamSlug, n)
	return n, nil
}

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
	return &n, nil
}

func (r *Reader) setToRedis(ctx context.Context, teamSlug string, n *domain.Node) {
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	data, err := json.Marshal(n)
	if err != nil {
		return
	}
	_ = r.redis.Set(ctx, nodeKey(teamSlug, n.Path), data, r.ttl).Err()
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
	n.created_at, n.updated_at
FROM nodes n
JOIN teams t ON t.id = n.team_id
WHERE t.slug = $1 AND n.path = $2`

func (r *Reader) getFromPg(ctx context.Context, teamSlug, path string) (*domain.Node, error) {
	row := r.pg.QueryRow(ctx, pgSelectNodeByTeamSlugAndPath, teamSlug, path)

	var n domain.Node
	var rootMethod, urlMode, authType, authDynSrc, incomingAuth, status string
	var encAuth, encInc string
	var created, updated time.Time

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
		&created, &updated,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNodeNotFound
		}
		return nil, fmt.Errorf("scan node: %w", err)
	}

	n.RootMethod = domain.RootMethod(rootMethod)
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
// нужно идти в Postgres напрямую» (см. §9.4 ТЗ).
func isRedisUnavailable(err error) bool {
	// Простая проверка по тексту — pkg-уровневая sentinel у go-redis нет
	// для disconnect/timeout. Phase 2: заменить на более точный детектор
	// с метрикой nexus_redis_unavailable_total.
	return err != nil
}
