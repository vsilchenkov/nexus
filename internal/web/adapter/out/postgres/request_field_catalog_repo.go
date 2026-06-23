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

// RequestFieldCatalogRepoPg — PG-реализация port.RequestFieldCatalogRepo (§41).
// usage_count считается коррелированным подзапросом по обеим колонкам имени
// поля динамической авторизации.
type RequestFieldCatalogRepoPg struct {
	db     DBTX
	logger logging.Logger
}

var _ port.RequestFieldCatalogRepo = (*RequestFieldCatalogRepoPg)(nil)

func NewRequestFieldCatalogRepoPg(db DBTX, logger logging.Logger) *RequestFieldCatalogRepoPg {
	return &RequestFieldCatalogRepoPg{db: db, logger: logger}
}

// requestFieldUsageExpr — on-read usage_count: число узлов, у которых имя
// встречается в исходящем auth_dynamic_field ИЛИ входящем
// incoming_auth_dynamic_field (без учёта регистра).
const requestFieldUsageExpr = `(SELECT count(*) FROM nodes n
	WHERE lower(n.auth_dynamic_field) = lower(request_fields_catalog.name)
	   OR lower(n.incoming_auth_dynamic_field) = lower(request_fields_catalog.name))`

func (r *RequestFieldCatalogRepoPg) scan(row rowScanner) (*domain.RequestFieldCatalogEntry, error) {
	var e domain.RequestFieldCatalogEntry
	err := row.Scan(&e.ID, &e.Name, &e.Description, &e.CreatedBy, &e.CreatedAt, &e.UpdatedAt, &e.UsageCount)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrRequestFieldNotFound
		}
		return nil, fmt.Errorf("scan request_fields_catalog: %w", err)
	}
	return &e, nil
}

func (r *RequestFieldCatalogRepoPg) Search(ctx context.Context, q string, limit int) ([]*domain.RequestFieldCatalogEntry, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := r.db.Query(ctx, `
SELECT id, name, description, created_by, created_at, updated_at, `+requestFieldUsageExpr+` AS usage_count
FROM request_fields_catalog
WHERE ($1 = '' OR name ILIKE $1 || '%')
ORDER BY usage_count DESC, name
LIMIT $2`, q, limit)
	if err != nil {
		return nil, fmt.Errorf("search request_fields_catalog: %w", err)
	}
	defer rows.Close()
	var out []*domain.RequestFieldCatalogEntry
	for rows.Next() {
		e, err := r.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *RequestFieldCatalogRepoPg) GetByName(ctx context.Context, name string) (*domain.RequestFieldCatalogEntry, error) {
	return r.scan(r.db.QueryRow(ctx,
		`SELECT id, name, description, created_by, created_at, updated_at, `+requestFieldUsageExpr+` AS usage_count
		 FROM request_fields_catalog WHERE lower(name) = lower($1)`, name))
}

func (r *RequestFieldCatalogRepoPg) Create(ctx context.Context, e *domain.RequestFieldCatalogEntry) error {
	err := r.db.QueryRow(ctx, `
INSERT INTO request_fields_catalog (name, description, created_by)
VALUES ($1, $2, $3)
RETURNING id, created_at, updated_at`,
		e.Name, e.Description, e.CreatedBy,
	).Scan(&e.ID, &e.CreatedAt, &e.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.ErrRequestFieldAlreadyExists
		}
		return fmt.Errorf("create request_fields_catalog: %w", err)
	}
	return nil
}
