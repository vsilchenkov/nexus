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
var _ port.NodeRepo = (*NodeRepoPg)(nil)

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
	created_at, updated_at, clickhouse_template_id`

func (r *NodeRepoPg) Get(ctx context.Context, id string) (*domain.Node, error) {
	row := r.db.QueryRow(ctx, `SELECT `+nodeColumns+` FROM nodes WHERE id = $1`, id)
	return r.scan(row)
}

func (r *NodeRepoPg) GetByPath(ctx context.Context, path string) (*domain.Node, error) {
	row := r.db.QueryRow(ctx, `SELECT `+nodeColumns+` FROM nodes WHERE path = $1`, path)
	return r.scan(row)
}

func (r *NodeRepoPg) List(ctx context.Context, f port.ListNodesFilter) ([]*domain.Node, error) {
	q := `SELECT ` + nodeColumns + ` FROM nodes WHERE team_id = $1`
	args := []any{f.TeamID}
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

func (r *NodeRepoPg) Create(ctx context.Context, n *domain.Node) error {
	encAuth, err := r.cipher.Encrypt(n.AuthCredentials)
	if err != nil {
		return fmt.Errorf("encrypt auth: %w", err)
	}
	encInc, err := r.cipher.Encrypt(n.IncomingAuthCredentials)
	if err != nil {
		return fmt.Errorf("encrypt incoming: %w", err)
	}

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
	clickhouse_template_id
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
	$27
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
		nullUUID(n.ClickHouseTemplateID),
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
	clickhouse_template_id = $28,
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
		nullUUID(n.ClickHouseTemplateID),
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

// rowScanner — общий интерфейс между *pgx.Row и pgx.Rows для scan().
type rowScanner interface {
	Scan(dest ...any) error
}

func (r *NodeRepoPg) scan(row rowScanner) (*domain.Node, error) {
	var n domain.Node
	var rootMethod, urlMode, authType, authDynSrc, incomingAuth, status string
	var encAuth, encInc string
	var created, updated time.Time
	var templateID *string

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
		&created, &updated, &templateID,
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
