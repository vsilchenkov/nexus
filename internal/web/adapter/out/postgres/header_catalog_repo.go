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

// HeaderCatalogRepoPg — PG-реализация port.HeaderCatalogRepo (§24).
// usage_count считается коррелированным подзапросом по nodes.forward_headers.
type HeaderCatalogRepoPg struct {
	db     DBTX
	logger logging.Logger
}

var _ port.HeaderCatalogRepo = (*HeaderCatalogRepoPg)(nil)

func NewHeaderCatalogRepoPg(db DBTX, logger logging.Logger) *HeaderCatalogRepoPg {
	return &HeaderCatalogRepoPg{db: db, logger: logger}
}

// usageExpr — on-read usage_count: число узлов, у которых имя встречается в
// forward_headers. Без учёта регистра (имена в forward_headers вводятся
// пользователем; каноническое имя — в каталоге).
const usageExpr = `(SELECT count(*) FROM nodes n
	WHERE EXISTS (SELECT 1 FROM unnest(n.forward_headers) fh WHERE lower(fh) = lower(headers_catalog.name)))`

func (r *HeaderCatalogRepoPg) scan(row rowScanner) (*domain.HeaderCatalogEntry, error) {
	var e domain.HeaderCatalogEntry
	err := row.Scan(&e.ID, &e.Name, &e.Description, &e.CreatedBy, &e.CreatedAt, &e.UpdatedAt, &e.UsageCount)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrHeaderNotFound
		}
		return nil, fmt.Errorf("scan headers_catalog: %w", err)
	}
	return &e, nil
}

func (r *HeaderCatalogRepoPg) Search(ctx context.Context, q string, limit int) ([]*domain.HeaderCatalogEntry, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := r.db.Query(ctx, `
SELECT id, name, description, created_by, created_at, updated_at, `+usageExpr+` AS usage_count
FROM headers_catalog
WHERE ($1 = '' OR name ILIKE $1 || '%')
ORDER BY usage_count DESC, name
LIMIT $2`, q, limit)
	if err != nil {
		return nil, fmt.Errorf("search headers_catalog: %w", err)
	}
	defer rows.Close()
	var out []*domain.HeaderCatalogEntry
	for rows.Next() {
		e, err := r.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *HeaderCatalogRepoPg) Get(ctx context.Context, id string) (*domain.HeaderCatalogEntry, error) {
	return r.scan(r.db.QueryRow(ctx,
		`SELECT id, name, description, created_by, created_at, updated_at, `+usageExpr+` AS usage_count
		 FROM headers_catalog WHERE id = $1::uuid`, id))
}

func (r *HeaderCatalogRepoPg) GetByName(ctx context.Context, name string) (*domain.HeaderCatalogEntry, error) {
	return r.scan(r.db.QueryRow(ctx,
		`SELECT id, name, description, created_by, created_at, updated_at, `+usageExpr+` AS usage_count
		 FROM headers_catalog WHERE lower(name) = lower($1)`, name))
}

func (r *HeaderCatalogRepoPg) Create(ctx context.Context, e *domain.HeaderCatalogEntry) error {
	err := r.db.QueryRow(ctx, `
INSERT INTO headers_catalog (name, description, created_by)
VALUES ($1, $2, $3)
RETURNING id, created_at, updated_at`,
		e.Name, e.Description, e.CreatedBy,
	).Scan(&e.ID, &e.CreatedAt, &e.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.ErrHeaderAlreadyExists
		}
		return fmt.Errorf("create headers_catalog: %w", err)
	}
	return nil
}

func (r *HeaderCatalogRepoPg) UpdateName(ctx context.Context, id, name string) error {
	tag, err := r.db.Exec(ctx,
		`UPDATE headers_catalog SET name = $2, updated_at = now() WHERE id = $1::uuid`,
		id, name)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.ErrHeaderAlreadyExists
		}
		return fmt.Errorf("update headers_catalog name: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrHeaderNotFound
	}
	return nil
}

func (r *HeaderCatalogRepoPg) UpdateDescription(ctx context.Context, id, description string) error {
	tag, err := r.db.Exec(ctx,
		`UPDATE headers_catalog SET description = $2, updated_at = now() WHERE id = $1::uuid`,
		id, description)
	if err != nil {
		return fmt.Errorf("update headers_catalog description: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrHeaderNotFound
	}
	return nil
}

// Delete удаляет заголовок из справочника. Привязки к узлам живут строками в
// nodes.forward_headers (не FK), поэтому единственная защита от удаления
// используемого заголовка — usage_count-guard в usecase.
func (r *HeaderCatalogRepoPg) Delete(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM headers_catalog WHERE id = $1::uuid`, id)
	if err != nil {
		return fmt.Errorf("delete headers_catalog: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrHeaderNotFound
	}
	return nil
}
