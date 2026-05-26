// Package nodepg — read-only NodeReader для Sender поверх PostgreSQL.
//
// Sender не зависит от Redis: async-обработка не критична к латентности,
// PG-чтение per-message приемлемо (consumer ограничен throughput'ом
// HTTP-вызовов внешних узлов, а не БД).
package nodepg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"nexus/internal/domain"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
)

// Reader реализует тот же контракт port.NodeReader, что и в Receiver,
// но идёт сразу в Postgres.
type Reader struct {
	pg     *pgxpool.Pool
	cipher *crypto.Cipher
	logger logging.Logger
}

func New(pg *pgxpool.Pool, cipher *crypto.Cipher, logger logging.Logger) *Reader {
	return &Reader{pg: pg, cipher: cipher, logger: logger}
}

const selectByPath = `
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

// GetByPath возвращает актуальный конфиг узла. Использует sender'ом
// перед обработкой каждого Kafka-сообщения, чтобы видеть свежий
// status (enabled/disabled/paused) и retry-параметры.
func (r *Reader) GetByPath(ctx context.Context, path string) (*domain.Node, error) {
	row := r.pg.QueryRow(ctx, selectByPath, path)
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
		return nil, fmt.Errorf("scan node %s: %w", path, err)
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

// ListForHousekeeping — все узлы с заданной CH-таблицей и retention > 0.
// Используется CHHousekeeping (§4.3 ТЗ). Чувствительные поля не нужны,
// поэтому скан без crypto.Decrypt.
func (r *Reader) ListForHousekeeping(ctx context.Context) ([]*domain.Node, error) {
	rows, err := r.pg.Query(ctx, `
SELECT id, path, clickhouse_table, clickhouse_retention_days
FROM nodes
WHERE clickhouse_table <> '' AND clickhouse_retention_days > 0`)
	if err != nil {
		return nil, fmt.Errorf("list housekeeping: %w", err)
	}
	defer rows.Close()

	var out []*domain.Node
	for rows.Next() {
		var n domain.Node
		if err := rows.Scan(&n.ID, &n.Path, &n.ClickHouseTable, &n.ClickHouseRetentionDays); err != nil {
			return nil, fmt.Errorf("scan housekeeping row: %w", err)
		}
		out = append(out, &n)
	}
	return out, nil
}
