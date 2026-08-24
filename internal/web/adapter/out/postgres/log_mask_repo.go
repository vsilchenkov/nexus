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

// LogMaskRepoPg — PG-реализация port.LogMaskRepo (§95). Таблица глобальная,
// без team-скоупа; строки читает и Web (управление), и Sender (применение,
// свой read-only reader в adapter/out/maskpg).
type LogMaskRepoPg struct {
	db     DBTX
	logger logging.Logger
}

var _ port.LogMaskRepo = (*LogMaskRepoPg)(nil)

func NewLogMaskRepoPg(db DBTX, logger logging.Logger) *LogMaskRepoPg {
	return &LogMaskRepoPg{db: db, logger: logger}
}

const logMaskCols = `id, pattern, replacement, description, enabled, sort_order,
	created_by, updated_by, created_at, updated_at`

func (r *LogMaskRepoPg) scan(row rowScanner) (*domain.LogMaskPattern, error) {
	var e domain.LogMaskPattern
	err := row.Scan(&e.ID, &e.Pattern, &e.Replacement, &e.Description, &e.Enabled, &e.SortOrder,
		&e.CreatedBy, &e.UpdatedBy, &e.CreatedAt, &e.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrLogMaskNotFound
		}
		return nil, fmt.Errorf("scan log_mask_patterns: %w", err)
	}
	return &e, nil
}

func (r *LogMaskRepoPg) List(ctx context.Context, q string) ([]*domain.LogMaskPattern, error) {
	rows, err := r.db.Query(ctx, `
SELECT `+logMaskCols+`
FROM log_mask_patterns
WHERE ($1 = '' OR pattern ILIKE '%' || $1 || '%' OR description ILIKE '%' || $1 || '%')
ORDER BY sort_order, created_at`, q)
	if err != nil {
		return nil, fmt.Errorf("list log_mask_patterns: %w", err)
	}
	defer rows.Close()
	var out []*domain.LogMaskPattern
	for rows.Next() {
		e, err := r.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *LogMaskRepoPg) Get(ctx context.Context, id string) (*domain.LogMaskPattern, error) {
	return r.scan(r.db.QueryRow(ctx,
		`SELECT `+logMaskCols+` FROM log_mask_patterns WHERE id = $1::uuid`, id))
}

func (r *LogMaskRepoPg) Create(ctx context.Context, e *domain.LogMaskPattern) error {
	err := r.db.QueryRow(ctx, `
INSERT INTO log_mask_patterns (pattern, replacement, description, enabled, sort_order, created_by, updated_by)
VALUES ($1, $2, $3, $4, $5, $6, $6)
RETURNING id, created_at, updated_at`,
		e.Pattern, e.Replacement, e.Description, e.Enabled, e.SortOrder, e.CreatedBy,
	).Scan(&e.ID, &e.CreatedAt, &e.UpdatedAt)
	if err != nil {
		return fmt.Errorf("create log_mask_patterns: %w", err)
	}
	return nil
}

func (r *LogMaskRepoPg) Update(ctx context.Context, e *domain.LogMaskPattern) error {
	tag, err := r.db.Exec(ctx, `
UPDATE log_mask_patterns
SET pattern = $2, replacement = $3, description = $4, enabled = $5, sort_order = $6,
    updated_by = $7, updated_at = now()
WHERE id = $1::uuid`,
		e.ID, e.Pattern, e.Replacement, e.Description, e.Enabled, e.SortOrder, e.UpdatedBy)
	if err != nil {
		return fmt.Errorf("update log_mask_patterns: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrLogMaskNotFound
	}
	return nil
}

func (r *LogMaskRepoPg) Delete(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM log_mask_patterns WHERE id = $1::uuid`, id)
	if err != nil {
		return fmt.Errorf("delete log_mask_patterns: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrLogMaskNotFound
	}
	return nil
}
