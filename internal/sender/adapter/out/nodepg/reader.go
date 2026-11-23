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

// Reader реализует usecase.NodeReader Sender'а: узел читается сразу из
// Postgres, вместе со слогом его команды.
type Reader struct {
	pg     *pgxpool.Pool
	cipher *crypto.Cipher
	logger logging.Logger
}

func New(pg *pgxpool.Pool, cipher *crypto.Cipher, logger logging.Logger) *Reader {
	return &Reader{pg: pg, cipher: cipher, logger: logger}
}

// selectNodes — колонки узла и слог его команды (§101.4). Порядок колонок
// держит scanNode: расхождение SELECT и Scan видно только в рантайме.
const selectNodes = `
SELECT
	n.id, n.path, n.root_method,
	n.url_mode, n.target_url, n.url_param_name, n.url_allowed_hosts,
	n.auth_type, n.auth_credentials,
	n.auth_dynamic_source, n.auth_dynamic_field, n.auth_dynamic_strip_prefix,
	n.incoming_auth_type, n.incoming_auth_credentials,
	n.webhook_signature_header, n.webhook_signature_prefix,
	n.forward_headers, n.timeout_ms, n.retry_count, n.retry_backoff_ms,
	n.clickhouse_table, n.status, n.team_id, t.slug,
	n.log_request_body, n.log_response_body, n.log_headers,
	n.logging_enabled, n.max_body_size_enabled, n.max_body_size,
	n.created_at, n.updated_at,
	n.incoming_method, n.outgoing_method,
	n.dlq_ttl_seconds, n.dlq_retry_delay_seconds,
	n.circuit_breaker_threshold, n.circuit_breaker_cooldown_sec,
	n.breaker_ignore_timeouts
FROM nodes n
JOIN teams t ON t.id = n.team_id`

// GetByID возвращает актуальный конфиг узла по id (§101.5). Sender вызывает его
// перед обработкой каждого Kafka-сообщения, чтобы видеть свежий status
// (enabled/disabled/paused) и retry-параметры.
//
// id обязан быть UUID: проверку делает usecase. Иначе PostgreSQL ответил бы
// ошибкой синтаксиса, неотличимой от сбоя БД, и сообщение повторялось бы вечно.
func (r *Reader) GetByID(ctx context.Context, id string) (*domain.Node, error) {
	n, err := r.scanNode(r.pg.QueryRow(ctx, selectNodes+` WHERE n.id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNodeNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get node %s: %w", id, err)
	}
	return n, nil
}

// ListByPath возвращает узлы с путём path во ВСЕХ командах (§101.5): путь
// уникален лишь внутри команды. Нужен конвертам, записанным до §101, — у них
// нет node_id. Порядок по слогу команды — чтобы логи были воспроизводимы.
//
// Креды расшифровываются, только если узел один: несколько узлов конверт не
// исполняют, и у них поля кред пусты. Расшифровка всех строк превращала бы
// испорченные креды узла чужой команды в ошибку чтения, а неоднозначный
// конверт — в бесконечный повтор вместо DLQ.
func (r *Reader) ListByPath(ctx context.Context, path string) ([]*domain.Node, error) {
	rows, err := r.pg.Query(ctx, selectNodes+` WHERE n.path = $1 ORDER BY t.slug`, path)
	if err != nil {
		return nil, fmt.Errorf("list nodes by path %s: %w", path, err)
	}
	defer rows.Close()

	var out []*domain.Node
	var creds []encryptedCreds
	for rows.Next() {
		n, enc, err := scanNodeRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scan node %s: %w", path, err)
		}
		out = append(out, n)
		creds = append(creds, enc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list nodes by path %s: %w", path, err)
	}
	if len(out) == 1 {
		if err := r.decryptCreds(out[0], creds[0]); err != nil {
			return nil, fmt.Errorf("list nodes by path %s: %w", path, err)
		}
	} else if len(out) > 1 {
		// §51.9: креды пропущены намеренно — видно при разборе «почему пусто».
		r.logger.Debug("nodepg: several nodes share the path, credentials not decrypted",
			r.logger.Str("node_path", path),
			r.logger.Int("nodes", len(out)))
	}
	return out, nil
}

// encryptedCreds — зашифрованные креды строки узла, ещё не расшифрованные.
type encryptedCreds struct{ auth, incoming string }

// scanNode читает одну строку selectNodes и расшифровывает креды. Ошибку Scan
// (включая pgx.ErrNoRows) возвращает как есть — контекст добавляет вызывающий.
func (r *Reader) scanNode(row pgx.Row) (*domain.Node, error) {
	n, enc, err := scanNodeRow(row)
	if err != nil {
		return nil, err
	}
	if err := r.decryptCreds(n, enc); err != nil {
		return nil, err
	}
	return n, nil
}

// decryptCreds расшифровывает креды узла в domain.Node (там всегда plaintext).
func (r *Reader) decryptCreds(n *domain.Node, enc encryptedCreds) error {
	var err error
	n.AuthCredentials, err = r.cipher.Decrypt(enc.auth)
	if err != nil {
		return fmt.Errorf("decrypt auth: %w", err)
	}
	n.IncomingAuthCredentials, err = r.cipher.Decrypt(enc.incoming)
	if err != nil {
		return fmt.Errorf("decrypt incoming: %w", err)
	}
	return nil
}

// scanNodeRow читает одну строку selectNodes без расшифровки кред.
func scanNodeRow(row pgx.Row) (*domain.Node, encryptedCreds, error) {
	var n domain.Node
	var rootMethod, urlMode, authType, authDynSrc, incomingAuth, status string
	var incomingMethod, outgoingMethod string
	var enc encryptedCreds
	var created, updated time.Time
	// §81.3: NULL = «политика из конфигурации», поэтому указатели, а не int32.
	var cbThreshold, cbCooldown *int32

	err := row.Scan(
		&n.ID, &n.Path, &rootMethod,
		&urlMode, &n.TargetURL, &n.URLParamName, &n.URLAllowedHosts,
		&authType, &enc.auth,
		&authDynSrc, &n.AuthDynamicField, &n.AuthDynamicStripPrefix,
		&incomingAuth, &enc.incoming,
		&n.WebhookSignatureHeader, &n.WebhookSignaturePrefix,
		&n.ForwardHeaders, &n.TimeoutMs, &n.RetryCount, &n.RetryBackoffMs,
		&n.ClickHouseTable, &status, &n.TeamID, &n.TeamSlug,
		&n.LogRequestBody, &n.LogResponseBody, &n.LogHeaders,
		&n.LoggingEnabled, &n.MaxBodySizeEnabled, &n.MaxBodySize,
		&created, &updated,
		&incomingMethod, &outgoingMethod,
		&n.DLQTTLSeconds, &n.DLQRetryDelaySeconds,
		&cbThreshold, &cbCooldown,
		&n.BreakerIgnoreTimeouts,
	)
	if err != nil {
		return nil, encryptedCreds{}, err
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

	if n.URLAllowedHosts == nil {
		n.URLAllowedHosts = []string{}
	}
	if n.ForwardHeaders == nil {
		n.ForwardHeaders = []string{}
	}
	return &n, enc, nil
}

// ListClickHouseTables (§37) — уникальные имена CH-таблиц логов всех узлов с
// логированием (без фильтра по retention, в отличие от ListForHousekeeping).
// Для стартовой миграции схемы (добавление колонки node_id во все таблицы).
//
// §64: внешние таблицы исключены — их схему ведёт оператор, ALTER'ы Nexus'а по
// ним недопустимы. Фильтр на уровне узла, а не таблицы: имя остаётся в выборке,
// если на него ссылается хотя бы один НЕ-внешний узел (тогда таблицей всё равно
// управляет Nexus и мигрировать её нужно).
func (r *Reader) ListClickHouseTables(ctx context.Context) ([]string, error) {
	rows, err := r.pg.Query(ctx, `SELECT DISTINCT clickhouse_table FROM nodes WHERE clickhouse_table <> '' AND NOT external_table`)
	if err != nil {
		return nil, fmt.Errorf("list ch tables: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, fmt.Errorf("scan ch table: %w", err)
		}
		// Черновики узлов с выключенным логированием (Validate гейтит формат
		// по LoggingEnabled) — не таблицы, стартовым ALTER'ам не подлежат.
		if !domain.IsValidCHTableName(t) {
			continue
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListForHousekeeping — все узлы с заданной CH-таблицей и retention > 0.
// Используется CHHousekeeping (§4.3 ТЗ). Чувствительные поля не нужны,
// поэтому скан без crypto.Decrypt.
//
// §64: внешние таблицы исключены. Housekeeping дропает партицию ЦЕЛИКОМ, а не
// строки узла, — на таблице постороннего писателя это снесло бы чужие данные.
// Retention такой таблицы обеспечивает её владелец.
func (r *Reader) ListForHousekeeping(ctx context.Context) ([]*domain.Node, error) {
	rows, err := r.pg.Query(ctx, `
SELECT id, path, clickhouse_table, clickhouse_retention_days
FROM nodes
WHERE clickhouse_table <> '' AND clickhouse_retention_days > 0 AND NOT external_table`)
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
		// Черновик кривого имени (узел с выключенным логированием) — дропать
		// нечего, DROP PARTITION по нему только шумел бы ошибками.
		if !domain.IsValidCHTableName(n.ClickHouseTable) {
			continue
		}
		out = append(out, &n)
	}
	return out, nil
}

// nodesSnapshotSQL — число узлов на паузе и отпечаток таблицы nodes (§113.3).
// Синтаксис старше минимальной версии (PostgreSQL 16, §116): FILTER и
// string_agg … ORDER BY есть с 9.4.
const nodesSnapshotSQL = `
SELECT count(*) FILTER (WHERE status = $1),
       coalesce(md5(string_agg(id::text || ':' || xmin::text, ',' ORDER BY id)), '')
FROM nodes`

// NodesSnapshot возвращает число узлов на паузе и отпечаток таблицы nodes —
// триггер раунда прохода очереди паузы (§113.3).
//
// Отпечаток — md5 от пар id:xmin всех строк. xmin меняется при ЛЮБОМ UPDATE
// строки, кем бы он ни был сделан (Web, raw SQL, keyrotate, восстановление из
// резервной копии), поэтому ловится и смена статуса туда-обратно между двумя
// тиками (paused → enabled → paused), которую сравнение статусов пропустило бы.
// INSERT и DELETE меняют сам набор пар. Заморозка (VACUUM FREEZE) xmin не
// переписывает — с PG 9.4 она ставит бит в infomask, — лишних раундов от
// обслуживания нет. Периодических UPDATE по nodes в Nexus нет: отпечаток
// меняется только от действий людей.
func (r *Reader) NodesSnapshot(ctx context.Context) (paused int, fingerprint string, err error) {
	if err := r.pg.QueryRow(ctx, nodesSnapshotSQL, string(domain.NodeStatusPaused)).Scan(&paused, &fingerprint); err != nil {
		return 0, "", fmt.Errorf("nodes snapshot: %w", err)
	}
	return paused, fingerprint, nil
}
