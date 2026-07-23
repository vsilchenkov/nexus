// Package postgres — реализации port-интерфейсов поверх pgxpool.
//
// Здесь же — единственное место, где knows про шифр чувствительных полей
// (§5.5 ТЗ). Доменная Node всегда хранит plaintext-креды; mapper Encrypts
// перед записью и Decrypts перед возвратом.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"nexus/internal/domain"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// NodeRepoPg реализует port.NodeRepo. Зависит от DBTX (см. db.go), что
// позволяет UnitOfWorkPg создавать транзакционные клоны репозитория.
type NodeRepoPg struct {
	db     DBTX
	cipher *crypto.Cipher
	logger logging.Logger
}

// Compile-time check, что интерфейс реализован полностью.
var (
	_ port.NodeRepo       = (*NodeRepoPg)(nil)
	_ port.NodeTableUsage = (*NodeRepoPg)(nil)
)

func NewNodeRepoPg(db DBTX, cipher *crypto.Cipher, logger logging.Logger) *NodeRepoPg {
	return &NodeRepoPg{db: db, cipher: cipher, logger: logger}
}

const nodeColumns = `
	id, path, root_method,
	url_mode, target_url, url_param_name, url_allowed_hosts,
	auth_type, auth_credentials,
	auth_dynamic_source, auth_dynamic_field, auth_dynamic_strip_prefix,
	incoming_auth_type, incoming_auth_credentials,
	webhook_signature_header, webhook_signature_prefix,
	forward_headers, timeout_ms, retry_count, retry_backoff_ms,
	clickhouse_table, clickhouse_retention_days, status, team_id,
	log_request_body, log_response_body, log_headers,
	logging_enabled, max_body_size_enabled, max_body_size,
	created_at, updated_at, clickhouse_template_id,
	rmq_host, rmq_port, rmq_vhost, rmq_user, rmq_password, rmq_queue, rmq_use_tls,
	pull_interval_sec, pull_batch_size, pull_prefetch,
	incoming_method, outgoing_method, comment, dlq_ttl_seconds, dlq_retry_delay_seconds,
	path_passthrough,
	incoming_auth_dynamic_source, incoming_auth_dynamic_field,
	created_by, updated_by, external_table`

func (r *NodeRepoPg) Get(ctx context.Context, id string) (*domain.Node, error) {
	row := r.db.QueryRow(ctx, `SELECT `+nodeColumns+` FROM nodes WHERE id = $1`, id)
	return r.scan(row)
}

func (r *NodeRepoPg) GetByPath(ctx context.Context, path string) (*domain.Node, error) {
	row := r.db.QueryRow(ctx, `SELECT `+nodeColumns+` FROM nodes WHERE path = $1`, path)
	return r.scan(row)
}

// ListClickHouseTables — уникальные имена CH-таблиц логов ВСЕХ узлов, без
// фильтра по команде. Для стартовой миграции схемы (§37/§39/§42-доп): таблица
// хранится полным именем `db.table`, поэтому одного соединения хватает на любую
// БД команды.
//
// Умышленно без team-фильтра и симметрично Sender'у
// (nodepg.Reader.ListClickHouseTables): раньше Web брал таблицы через
// List(TeamID: defaultTeamID) и не альтерил БД не-default команд вовсе — на
// мультикомандном бою SELECT новых колонок падал с CH code 47 (Sentry 158619),
// пока таблицу не доальтерит рестарт Sender'а. Два сервиса — один источник
// списка, дрейф исключён.
//
// §64: внешние таблицы исключены (симметрично Sender'у) — их схему ведёт
// оператор. Фильтр на уровне узла: имя остаётся в выборке, если на него
// ссылается хотя бы один НЕ-внешний узел.
func (r *NodeRepoPg) ListClickHouseTables(ctx context.Context) ([]string, error) {
	rows, err := r.db.Query(ctx, `SELECT DISTINCT clickhouse_table FROM nodes WHERE clickhouse_table <> '' AND NOT external_table`)
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

func (r *NodeRepoPg) List(ctx context.Context, f port.ListNodesFilter) ([]*domain.Node, error) {
	// §62: непустой TeamIDs → кросс-командный поиск (team_id = ANY),
	// иначе обычный однокомандный листинг (team_id = $1).
	var q string
	var args []any
	if len(f.TeamIDs) > 0 {
		q = `SELECT ` + nodeColumns + ` FROM nodes WHERE team_id = ANY($1)`
		args = []any{f.TeamIDs}
	} else {
		q = `SELECT ` + nodeColumns + ` FROM nodes WHERE team_id = $1`
		args = []any{f.TeamID}
	}
	if f.RootMethod != "" {
		q += fmt.Sprintf(" AND root_method = $%d", len(args)+1)
		args = append(args, f.RootMethod)
	}
	if f.Search != "" {
		q += fmt.Sprintf(" AND (path ILIKE $%d OR target_url ILIKE $%d)", len(args)+1, len(args)+2)
		like := "%" + f.Search + "%"
		args = append(args, like, like)
	}
	q += " ORDER BY path"
	if f.Limit > 0 {
		q += fmt.Sprintf(" LIMIT $%d", len(args)+1)
		args = append(args, f.Limit)
	}
	if f.Offset > 0 {
		q += fmt.Sprintf(" OFFSET $%d", len(args)+1)
		args = append(args, f.Offset)
	}

	rows, err := r.db.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	defer rows.Close()

	var nodes []*domain.Node
	for rows.Next() {
		n, err := r.scan(rows)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

func (r *NodeRepoPg) Count(ctx context.Context, teamID string) (int, error) {
	var n int
	err := r.db.QueryRow(ctx, `SELECT count(*) FROM nodes WHERE team_id = $1`, teamID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count nodes: %w", err)
	}
	return n, nil
}

// CountByCHTable — реализация port.NodeTableUsage: сколько ДРУГИХ узлов
// ссылаются на ту же таблицу логов. Без фильтра по команде: общая таблица
// как раз и опасна тем, что её делят узлы разных команд.
func (r *NodeRepoPg) CountByCHTable(ctx context.Context, table, excludeNodeID string) (int, error) {
	if table == "" {
		return 0, nil
	}
	var n int
	err := r.db.QueryRow(ctx,
		`SELECT count(*) FROM nodes WHERE clickhouse_table = $1 AND ($2 = '' OR id <> $2::uuid)`,
		table, excludeNodeID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count nodes by ch table: %w", err)
	}
	return n, nil
}

// CountsByCHTable — реализация port.NodeTableUsage: сколько узлов приходится
// на каждую таблицу логов. Один запрос на всю инсталляцию (см. интерфейс).
func (r *NodeRepoPg) CountsByCHTable(ctx context.Context) (map[string]int, error) {
	rows, err := r.db.Query(ctx,
		`SELECT clickhouse_table, count(*) FROM nodes WHERE clickhouse_table <> '' GROUP BY 1`)
	if err != nil {
		return nil, fmt.Errorf("counts by ch table: %w", err)
	}
	defer rows.Close()

	out := make(map[string]int)
	for rows.Next() {
		var table string
		var n int
		if err := rows.Scan(&table, &n); err != nil {
			return nil, fmt.Errorf("scan counts by ch table: %w", err)
		}
		out[table] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate counts by ch table: %w", err)
	}
	return out, nil
}

func (r *NodeRepoPg) Create(ctx context.Context, n *domain.Node) error {
	encAuth, err := r.cipher.Encrypt(n.AuthCredentials)
	if err != nil {
		return fmt.Errorf("encrypt auth: %w", err)
	}
	encInc, err := r.cipher.Encrypt(n.IncomingAuthCredentials)
	if err != nil {
		return fmt.Errorf("encrypt incoming: %w", err)
	}
	encRMQ, err := r.cipher.Encrypt(n.RMQPassword)
	if err != nil {
		return fmt.Errorf("encrypt rmq: %w", err)
	}
	rmq := rmqArgs(n)

	const q = `
INSERT INTO nodes (
	path, root_method,
	url_mode, target_url, url_param_name, url_allowed_hosts,
	auth_type, auth_credentials,
	auth_dynamic_source, auth_dynamic_field, auth_dynamic_strip_prefix,
	incoming_auth_type, incoming_auth_credentials,
	webhook_signature_header, webhook_signature_prefix,
	forward_headers, timeout_ms, retry_count, retry_backoff_ms,
	clickhouse_table, clickhouse_retention_days, status, team_id,
	log_request_body, log_response_body, log_headers,
	logging_enabled, max_body_size_enabled, max_body_size,
	clickhouse_template_id,
	rmq_host, rmq_port, rmq_vhost, rmq_user, rmq_password, rmq_queue, rmq_use_tls,
	pull_interval_sec, pull_batch_size, pull_prefetch,
	incoming_method, outgoing_method, comment, dlq_ttl_seconds, dlq_retry_delay_seconds,
	path_passthrough,
	incoming_auth_dynamic_source, incoming_auth_dynamic_field,
	created_by, updated_by, external_table
) VALUES (
	$1, $2,
	$3, $4, $5, $6,
	$7, $8,
	$9, $10, $11,
	$12, $13,
	$14, $15,
	$16, $17, $18, $19,
	$20, $21, $22, $23,
	$24, $25, $26,
	$27, $28, $29,
	$30,
	$31, $32, $33, $34, $35, $36, $37,
	$38, $39, $40,
	$41, $42, $43, $44, $45,
	$46,
	$47, $48,
	$49, $50, $51
) RETURNING id, created_at, updated_at`

	err = r.db.QueryRow(ctx, q,
		n.Path, string(n.RootMethod),
		string(n.URLMode), n.TargetURL, n.URLParamName, nullSafe(n.URLAllowedHosts),
		string(n.AuthType), encAuth,
		string(n.AuthDynamicSource), n.AuthDynamicField, n.AuthDynamicStripPrefix,
		string(n.IncomingAuthType), encInc,
		n.WebhookSignatureHeader, n.WebhookSignaturePrefix,
		nullSafe(n.ForwardHeaders), n.TimeoutMs, n.RetryCount, n.RetryBackoffMs,
		n.ClickHouseTable, n.ClickHouseRetentionDays, string(n.Status), n.TeamID,
		n.LogRequestBody, n.LogResponseBody, n.LogHeaders,
		n.LoggingEnabled, n.MaxBodySizeEnabled, n.MaxBodySize,
		nullUUID(n.ClickHouseTemplateID),
		rmq.host, rmq.port, rmq.vhost, rmq.user, encRMQ, rmq.queue, n.RMQUseTLS,
		rmq.interval, rmq.batch, rmq.prefetch,
		methodOrDefault(n.IncomingMethod), methodOrDefault(n.OutgoingMethod), n.Comment,
		n.DLQTTLSeconds, n.DLQRetryDelaySeconds,
		n.PathPassthrough,
		incomingAuthDynSrc(n), incomingAuthDynField(n),
		n.CreatedBy, n.UpdatedBy, n.ExternalTable,
	).Scan(&n.ID, &n.CreatedAt, &n.UpdatedAt)

	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return domain.ErrNodeAlreadyExists
		}
		return fmt.Errorf("create node: %w", err)
	}
	return nil
}

func (r *NodeRepoPg) Update(ctx context.Context, n *domain.Node) error {
	encAuth, err := r.cipher.Encrypt(n.AuthCredentials)
	if err != nil {
		return fmt.Errorf("encrypt auth: %w", err)
	}
	encInc, err := r.cipher.Encrypt(n.IncomingAuthCredentials)
	if err != nil {
		return fmt.Errorf("encrypt incoming: %w", err)
	}
	encRMQ, err := r.cipher.Encrypt(n.RMQPassword)
	if err != nil {
		return fmt.Errorf("encrypt rmq: %w", err)
	}
	rmq := rmqArgs(n)

	const q = `
UPDATE nodes SET
	path = $2, root_method = $3,
	url_mode = $4, target_url = $5, url_param_name = $6, url_allowed_hosts = $7,
	auth_type = $8, auth_credentials = $9,
	auth_dynamic_source = $10, auth_dynamic_field = $11, auth_dynamic_strip_prefix = $12,
	incoming_auth_type = $13, incoming_auth_credentials = $14,
	webhook_signature_header = $15, webhook_signature_prefix = $16,
	forward_headers = $17, timeout_ms = $18, retry_count = $19, retry_backoff_ms = $20,
	clickhouse_table = $21, clickhouse_retention_days = $22, status = $23, team_id = $24,
	log_request_body = $25, log_response_body = $26, log_headers = $27,
	logging_enabled = $28, max_body_size_enabled = $29, max_body_size = $30,
	clickhouse_template_id = $31,
	rmq_host = $32, rmq_port = $33, rmq_vhost = $34, rmq_user = $35,
	rmq_password = $36, rmq_queue = $37, rmq_use_tls = $38,
	pull_interval_sec = $39, pull_batch_size = $40, pull_prefetch = $41,
	incoming_method = $42, outgoing_method = $43, comment = $44, dlq_ttl_seconds = $45,
	dlq_retry_delay_seconds = $46, path_passthrough = $47,
	incoming_auth_dynamic_source = $48, incoming_auth_dynamic_field = $49,
	updated_by = $50, external_table = $51,
	updated_at = now()
WHERE id = $1
RETURNING updated_at`

	err = r.db.QueryRow(ctx, q,
		n.ID,
		n.Path, string(n.RootMethod),
		string(n.URLMode), n.TargetURL, n.URLParamName, nullSafe(n.URLAllowedHosts),
		string(n.AuthType), encAuth,
		string(n.AuthDynamicSource), n.AuthDynamicField, n.AuthDynamicStripPrefix,
		string(n.IncomingAuthType), encInc,
		n.WebhookSignatureHeader, n.WebhookSignaturePrefix,
		nullSafe(n.ForwardHeaders), n.TimeoutMs, n.RetryCount, n.RetryBackoffMs,
		n.ClickHouseTable, n.ClickHouseRetentionDays, string(n.Status), n.TeamID,
		n.LogRequestBody, n.LogResponseBody, n.LogHeaders,
		n.LoggingEnabled, n.MaxBodySizeEnabled, n.MaxBodySize,
		nullUUID(n.ClickHouseTemplateID),
		rmq.host, rmq.port, rmq.vhost, rmq.user, encRMQ, rmq.queue, n.RMQUseTLS,
		rmq.interval, rmq.batch, rmq.prefetch,
		methodOrDefault(n.IncomingMethod), methodOrDefault(n.OutgoingMethod), n.Comment,
		n.DLQTTLSeconds, n.DLQRetryDelaySeconds,
		n.PathPassthrough,
		incomingAuthDynSrc(n), incomingAuthDynField(n),
		n.UpdatedBy, n.ExternalTable,
	).Scan(&n.UpdatedAt)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNodeNotFound
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return domain.ErrNodeAlreadyExists
		}
		return fmt.Errorf("update node: %w", err)
	}
	return nil
}

func (r *NodeRepoPg) Delete(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM nodes WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete node: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNodeNotFound
	}
	return nil
}

// UpdateAllowedHostsSnapshot переписывает только колонку url_allowed_hosts
// (денормализованный снимок паттернов из каталога, §23). Не трогает креды и
// остальные поля — поэтому дешевле и безопаснее полного Update.
func (r *NodeRepoPg) UpdateAllowedHostsSnapshot(ctx context.Context, nodeID string, patterns []string, updatedBy string) error {
	tag, err := r.db.Exec(ctx,
		`UPDATE nodes SET url_allowed_hosts = $2, updated_by = $3, updated_at = now() WHERE id = $1`,
		nodeID, nullSafe(patterns), updatedBy)
	if err != nil {
		return fmt.Errorf("update node allowed_hosts snapshot: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNodeNotFound
	}
	return nil
}

// rmqValues — аргументы §27-полей для INSERT/UPDATE. Для не-pull узлов все
// поля nil → SQL NULL (колонки nullable, chk_rmq_fields не применяется). Это
// держит строки request/requestAsync чистыми, а не забитыми нулями.
type rmqValues struct {
	host, vhost, user, queue        any
	port, interval, batch, prefetch any
}

// methodOrDefault страхует от пустого метода при прямой записи через репозиторий
// (минуя usecase.SetDefaults): колонки incoming_method/outgoing_method —
// NOT NULL с CHECK IN (...), пустая строка нарушила бы constraint. Пустое = POST.
func methodOrDefault(m domain.HTTPMethod) string {
	if m == "" {
		return string(domain.HTTPMethodPOST)
	}
	return string(m)
}

// incomingAuthDynSrc / incomingAuthDynField (§41) страхуют прямую запись через
// репозиторий (минуя usecase.SetDefaults): колонки incoming_auth_dynamic_* —
// NOT NULL с CHECK (source IN ('header','query'); field — формат имени), пустые
// значения нарушили бы constraint. Пусто = дефолт header/Authorization
// (прежнее поведение).
func incomingAuthDynSrc(n *domain.Node) string {
	if n.IncomingAuthDynamicSource == "" {
		return string(domain.IncomingAuthSourceHeader)
	}
	return string(n.IncomingAuthDynamicSource)
}

func incomingAuthDynField(n *domain.Node) string {
	if n.IncomingAuthDynamicField == "" {
		return "Authorization"
	}
	return n.IncomingAuthDynamicField
}

func rmqArgs(n *domain.Node) rmqValues {
	if !n.RootMethod.IsPull() {
		return rmqValues{}
	}
	return rmqValues{
		host:     n.RMQHost,
		vhost:    n.RMQVHost,
		user:     n.RMQUser,
		queue:    n.RMQQueue,
		port:     n.RMQPort,
		interval: n.PullIntervalSec,
		batch:    n.PullBatchSize,
		prefetch: n.PullPrefetch,
	}
}

// rowScanner — общий интерфейс между *pgx.Row и pgx.Rows для scan().
type rowScanner interface {
	Scan(dest ...any) error
}

func (r *NodeRepoPg) scan(row rowScanner) (*domain.Node, error) {
	var n domain.Node
	var rootMethod, urlMode, authType, authDynSrc, incomingAuth, status string
	var incAuthDynSrc string
	var incomingMethod, outgoingMethod string
	var encAuth, encInc string
	var created, updated time.Time
	var templateID *string
	var rmqHost, rmqVHost, rmqUser, encRMQ, rmqQueue *string
	var rmqPort, pullInterval, pullBatch, pullPrefetch *int32

	err := row.Scan(
		&n.ID, &n.Path, &rootMethod,
		&urlMode, &n.TargetURL, &n.URLParamName, &n.URLAllowedHosts,
		&authType, &encAuth,
		&authDynSrc, &n.AuthDynamicField, &n.AuthDynamicStripPrefix,
		&incomingAuth, &encInc,
		&n.WebhookSignatureHeader, &n.WebhookSignaturePrefix,
		&n.ForwardHeaders, &n.TimeoutMs, &n.RetryCount, &n.RetryBackoffMs,
		&n.ClickHouseTable, &n.ClickHouseRetentionDays, &status, &n.TeamID,
		&n.LogRequestBody, &n.LogResponseBody, &n.LogHeaders,
		&n.LoggingEnabled, &n.MaxBodySizeEnabled, &n.MaxBodySize,
		&created, &updated, &templateID,
		&rmqHost, &rmqPort, &rmqVHost, &rmqUser, &encRMQ, &rmqQueue, &n.RMQUseTLS,
		&pullInterval, &pullBatch, &pullPrefetch,
		&incomingMethod, &outgoingMethod, &n.Comment, &n.DLQTTLSeconds, &n.DLQRetryDelaySeconds,
		&n.PathPassthrough,
		&incAuthDynSrc, &n.IncomingAuthDynamicField,
		&n.CreatedBy, &n.UpdatedBy, &n.ExternalTable,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || isInvalidUUID(err) {
			return nil, domain.ErrNodeNotFound
		}
		return nil, fmt.Errorf("scan node: %w", err)
	}

	n.RootMethod = domain.RootMethod(rootMethod)
	n.IncomingMethod = domain.HTTPMethod(incomingMethod)
	n.OutgoingMethod = domain.HTTPMethod(outgoingMethod)
	n.URLMode = domain.URLMode(urlMode)
	n.AuthType = domain.AuthType(authType)
	n.AuthDynamicSource = domain.AuthDynSource(authDynSrc)
	n.IncomingAuthType = domain.IncomingAuthType(incomingAuth)
	n.IncomingAuthDynamicSource = domain.IncomingAuthSource(incAuthDynSrc)
	n.Status = domain.NodeStatus(status)
	n.CreatedAt = created
	n.UpdatedAt = updated
	if templateID != nil {
		n.ClickHouseTemplateID = *templateID
	}

	n.AuthCredentials, err = r.cipher.Decrypt(encAuth)
	if err != nil {
		return nil, fmt.Errorf("decrypt auth: %w", err)
	}
	n.IncomingAuthCredentials, err = r.cipher.Decrypt(encInc)
	if err != nil {
		return nil, fmt.Errorf("decrypt incoming: %w", err)
	}

	// §27: RabbitMQAsync-поля. Для request/requestAsync колонки NULL — оставляем
	// zero-значения. rmq_password дешифруется по той же схеме, что auth.
	n.RMQHost = derefStr(rmqHost)
	n.RMQPort = derefInt32(rmqPort)
	n.RMQVHost = derefStr(rmqVHost)
	n.RMQUser = derefStr(rmqUser)
	n.RMQQueue = derefStr(rmqQueue)
	n.PullIntervalSec = derefInt32(pullInterval)
	n.PullBatchSize = derefInt32(pullBatch)
	n.PullPrefetch = derefInt32(pullPrefetch)
	if encRMQ != nil {
		n.RMQPassword, err = r.cipher.Decrypt(*encRMQ)
		if err != nil {
			return nil, fmt.Errorf("decrypt rmq: %w", err)
		}
	}

	// Нормализация: pgx может вернуть nil-slice; работаем как с пустым.
	if n.URLAllowedHosts == nil {
		n.URLAllowedHosts = []string{}
	}
	if n.ForwardHeaders == nil {
		n.ForwardHeaders = []string{}
	}
	_ = strings.TrimSpace // зарезервировано под валидацию строк (TODO Phase 1.5)
	return &n, nil
}
