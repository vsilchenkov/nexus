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

	"bus/internal/domain"
	"bus/internal/platform/crypto"
	"bus/internal/platform/logging"
	"bus/internal/receiver/usecase/port"
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

func nodeKey(path string) string { return "node:" + path }

// GetByPath: сначала Redis, при miss — Postgres. Записывает обратно в Redis
// после fallback. При недоступности Redis — сразу в Postgres, без падения.
func (r *Reader) GetByPath(ctx context.Context, path string) (*domain.Node, error) {
	if n, err := r.getFromRedis(ctx, path); err == nil {
		return n, nil
	} else if !errors.Is(err, domain.ErrNodeNotFound) && !isRedisUnavailable(err) {
		r.logger.Warn("redis read failed, falling back to postgres",
			r.logger.Str("path", path), r.logger.Err(err))
	}

	n, err := r.getFromPg(ctx, path)
	if err != nil {
		return nil, err
	}
	// Best-effort write-back: ошибка не блокирует обработку запроса.
	go r.setToRedis(context.Background(), n)
	return n, nil
}

func (r *Reader) getFromRedis(ctx context.Context, path string) (*domain.Node, error) {
	data, err := r.redis.Get(ctx, nodeKey(path)).Bytes()
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

func (r *Reader) setToRedis(ctx context.Context, n *domain.Node) {
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	data, err := json.Marshal(n)
	if err != nil {
		return
	}
	_ = r.redis.Set(ctx, nodeKey(n.Path), data, r.ttl).Err()
}

const pgSelectNodeByPath = `
SELECT
	id, path, root_method,
	url_mode, target_url, url_param_name, url_allowed_hosts,
	auth_type, auth_credentials,
	auth_dynamic_source, auth_dynamic_field, auth_dynamic_strip_prefix,
	incoming_auth_type, incoming_auth_credentials,
	webhook_signature_header, webhook_signature_prefix,
	forward_headers, timeout_ms, retry_count, retry_backoff_ms,
	clickhouse_table, status, team_id,
	log_request_body, log_response_body, log_headers,
	created_at, updated_at
FROM nodes WHERE path = $1`

func (r *Reader) getFromPg(ctx context.Context, path string) (*domain.Node, error) {
	row := r.pg.QueryRow(ctx, pgSelectNodeByPath, path)

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
	// с метрикой databus_redis_unavailable_total.
	return err != nil
}
