package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// HostAllowlistRepoPg — PG-реализация port.HostAllowlistRepo (§23). Принимает
// DBTX (см. db.go) ради совместимости с UnitOfWork (link/unlink + пересборка
// снимка узла идут одной транзакцией).
type HostAllowlistRepoPg struct {
	db     DBTX
	logger logging.Logger
}

var _ port.HostAllowlistRepo = (*HostAllowlistRepoPg)(nil)

func NewHostAllowlistRepoPg(db DBTX, logger logging.Logger) *HostAllowlistRepoPg {
	return &HostAllowlistRepoPg{db: db, logger: logger}
}

const hostAllowlistCols = `id, pattern, kind, description, usage_count, created_by, created_at, updated_at`

func (r *HostAllowlistRepoPg) scan(row rowScanner) (*domain.HostAllowlistEntry, error) {
	var (
		e    domain.HostAllowlistEntry
		kind string
	)
	err := row.Scan(&e.ID, &e.Pattern, &kind, &e.Description, &e.UsageCount, &e.CreatedBy, &e.CreatedAt, &e.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrHostNotFound
		}
		return nil, fmt.Errorf("scan host_allowlist: %w", err)
	}
	e.Kind = domain.HostKind(kind)
	return &e, nil
}

func (r *HostAllowlistRepoPg) Get(ctx context.Context, id string) (*domain.HostAllowlistEntry, error) {
	return r.scan(r.db.QueryRow(ctx,
		`SELECT `+hostAllowlistCols+` FROM host_allowlist WHERE id = $1::uuid`, id))
}

func (r *HostAllowlistRepoPg) GetByPattern(ctx context.Context, pattern string) (*domain.HostAllowlistEntry, error) {
	return r.scan(r.db.QueryRow(ctx,
		`SELECT `+hostAllowlistCols+` FROM host_allowlist WHERE lower(pattern) = lower($1)`, pattern))
}

func (r *HostAllowlistRepoPg) Search(ctx context.Context, q string, kind domain.HostKind, limit int) ([]*domain.HostAllowlistEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.Query(ctx, `
SELECT `+hostAllowlistCols+` FROM host_allowlist
WHERE ($1 = '' OR pattern ILIKE '%' || $1 || '%' OR description ILIKE '%' || $1 || '%')
  AND ($2 = '' OR kind = $2)
ORDER BY usage_count DESC, pattern
LIMIT $3`, q, string(kind), limit)
	if err != nil {
		return nil, fmt.Errorf("search host_allowlist: %w", err)
	}
	defer rows.Close()
	var out []*domain.HostAllowlistEntry
	for rows.Next() {
		e, err := r.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *HostAllowlistRepoPg) Create(ctx context.Context, e *domain.HostAllowlistEntry) error {
	err := r.db.QueryRow(ctx, `
INSERT INTO host_allowlist (pattern, kind, description, created_by)
VALUES ($1, $2, $3, $4)
RETURNING id, usage_count, created_at, updated_at`,
		e.Pattern, string(e.Kind), e.Description, e.CreatedBy,
	).Scan(&e.ID, &e.UsageCount, &e.CreatedAt, &e.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.ErrHostAlreadyExists
		}
		return fmt.Errorf("create host_allowlist: %w", err)
	}
	return nil
}

func (r *HostAllowlistRepoPg) UpdateDescription(ctx context.Context, id, description string) error {
	tag, err := r.db.Exec(ctx,
		`UPDATE host_allowlist SET description = $2, updated_at = now() WHERE id = $1::uuid`,
		id, description)
	if err != nil {
		return fmt.Errorf("update host description: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrHostNotFound
	}
	return nil
}

func (r *HostAllowlistRepoPg) UpdatePattern(ctx context.Context, id, pattern string, kind domain.HostKind) error {
	tag, err := r.db.Exec(ctx,
		`UPDATE host_allowlist SET pattern = $2, kind = $3, updated_at = now() WHERE id = $1::uuid`,
		id, pattern, string(kind))
	if err != nil {
		if isUniqueViolation(err) {
			return domain.ErrHostAlreadyExists
		}
		return fmt.Errorf("update host pattern: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrHostNotFound
	}
	return nil
}

func (r *HostAllowlistRepoPg) Delete(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM host_allowlist WHERE id = $1::uuid`, id)
	if err != nil {
		// FK RESTRICT из node_allowed_hosts — второй уровень защиты поверх
		// usage_count-guard в usecase.
		if isForeignKeyViolation(err) {
			return domain.ErrHostInUse
		}
		return fmt.Errorf("delete host_allowlist: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrHostNotFound
	}
	return nil
}

func (r *HostAllowlistRepoPg) Link(ctx context.Context, nodeID, hostID string) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO node_allowed_hosts (node_id, host_id) VALUES ($1::uuid, $2::uuid)
		 ON CONFLICT DO NOTHING`, nodeID, hostID)
	if err != nil {
		return fmt.Errorf("link host to node: %w", err)
	}
	return nil
}

func (r *HostAllowlistRepoPg) Unlink(ctx context.Context, nodeID, hostID string) error {
	_, err := r.db.Exec(ctx,
		`DELETE FROM node_allowed_hosts WHERE node_id = $1::uuid AND host_id = $2::uuid`,
		nodeID, hostID)
	if err != nil {
		return fmt.Errorf("unlink host from node: %w", err)
	}
	return nil
}

func (r *HostAllowlistRepoPg) ListByNode(ctx context.Context, nodeID string) ([]*domain.HostAllowlistEntry, error) {
	rows, err := r.db.Query(ctx, `
SELECT h.id, h.pattern, h.kind, h.description, h.usage_count, h.created_by, h.created_at, h.updated_at
FROM host_allowlist h
JOIN node_allowed_hosts nah ON nah.host_id = h.id
WHERE nah.node_id = $1::uuid
ORDER BY h.pattern`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("list hosts by node: %w", err)
	}
	defer rows.Close()
	var out []*domain.HostAllowlistEntry
	for rows.Next() {
		e, err := r.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
