package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// CHTemplateRepoPg — PG-реализация port.CHTemplateRepo. spec хранится в JSONB.
// Принимает DBTX (см. db.go) ради совместимости с UnitOfWork.
type CHTemplateRepoPg struct {
	db     DBTX
	logger logging.Logger
}

var _ port.CHTemplateRepo = (*CHTemplateRepoPg)(nil)

func NewCHTemplateRepoPg(db DBTX, logger logging.Logger) *CHTemplateRepoPg {
	return &CHTemplateRepoPg{db: db, logger: logger}
}

const chTemplateCols = `id, name, description, spec, is_default, created_at, updated_at`

func (r *CHTemplateRepoPg) scan(row rowScanner) (*domain.CHTemplate, error) {
	var (
		t   domain.CHTemplate
		raw []byte
	)
	err := row.Scan(&t.ID, &t.Name, &t.Description, &raw, &t.IsDefault, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrCHTemplateNotFound
		}
		return nil, fmt.Errorf("scan ch_template: %w", err)
	}
	if err := json.Unmarshal(raw, &t.Spec); err != nil {
		return nil, fmt.Errorf("unmarshal ch_template spec: %w", err)
	}
	return &t, nil
}

func (r *CHTemplateRepoPg) Get(ctx context.Context, id string) (*domain.CHTemplate, error) {
	return r.scan(r.db.QueryRow(ctx,
		`SELECT `+chTemplateCols+` FROM ch_templates WHERE id = $1::uuid`, id))
}

func (r *CHTemplateRepoPg) GetDefault(ctx context.Context) (*domain.CHTemplate, error) {
	return r.scan(r.db.QueryRow(ctx,
		`SELECT `+chTemplateCols+` FROM ch_templates WHERE is_default LIMIT 1`))
}

func (r *CHTemplateRepoPg) List(ctx context.Context) ([]*domain.CHTemplate, error) {
	rows, err := r.db.Query(ctx,
		`SELECT `+chTemplateCols+` FROM ch_templates ORDER BY is_default DESC, name`)
	if err != nil {
		return nil, fmt.Errorf("list ch_templates: %w", err)
	}
	defer rows.Close()
	var out []*domain.CHTemplate
	for rows.Next() {
		t, err := r.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *CHTemplateRepoPg) Create(ctx context.Context, t *domain.CHTemplate) error {
	spec, err := json.Marshal(t.Spec)
	if err != nil {
		return fmt.Errorf("marshal ch_template spec: %w", err)
	}
	err = r.db.QueryRow(ctx, `
INSERT INTO ch_templates (name, description, spec, is_default)
VALUES ($1, $2, $3::jsonb, $4)
RETURNING id, created_at, updated_at`,
		t.Name, t.Description, spec, t.IsDefault,
	).Scan(&t.ID, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.ErrCHTemplateAlreadyExists
		}
		return fmt.Errorf("create ch_template: %w", err)
	}
	return nil
}

func (r *CHTemplateRepoPg) Update(ctx context.Context, t *domain.CHTemplate) error {
	spec, err := json.Marshal(t.Spec)
	if err != nil {
		return fmt.Errorf("marshal ch_template spec: %w", err)
	}
	err = r.db.QueryRow(ctx, `
UPDATE ch_templates SET name = $2, description = $3, spec = $4::jsonb, is_default = $5, updated_at = now()
WHERE id = $1::uuid
RETURNING updated_at`,
		t.ID, t.Name, t.Description, spec, t.IsDefault,
	).Scan(&t.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrCHTemplateNotFound
		}
		if isUniqueViolation(err) {
			return domain.ErrCHTemplateAlreadyExists
		}
		return fmt.Errorf("update ch_template: %w", err)
	}
	return nil
}

func (r *CHTemplateRepoPg) Delete(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM ch_templates WHERE id = $1::uuid`, id)
	if err != nil {
		return fmt.Errorf("delete ch_template: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrCHTemplateNotFound
	}
	return nil
}

func (r *CHTemplateRepoPg) CountNodesUsing(ctx context.Context, id string) (int, error) {
	var n int
	err := r.db.QueryRow(ctx,
		`SELECT count(*) FROM nodes WHERE clickhouse_template_id = $1::uuid`, id).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count nodes using template: %w", err)
	}
	return n, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
