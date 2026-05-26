package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

type APITokenRepoPg struct {
	pool   *pgxpool.Pool
	logger logging.Logger
}

var _ port.APITokenRepo = (*APITokenRepoPg)(nil)

func NewAPITokenRepoPg(pool *pgxpool.Pool, logger logging.Logger) *APITokenRepoPg {
	return &APITokenRepoPg{pool: pool, logger: logger}
}

const apiTokenCols = `id, user_id, name, token_hash, prefix, scopes,
	created_at, last_used_at, expires_at, revoked_at`

func scanAPIToken(row pgx.Row) (*domain.APIToken, error) {
	var t domain.APIToken
	var lastUsed, expires, revoked *time.Time
	err := row.Scan(&t.ID, &t.UserID, &t.Name, &t.TokenHash, &t.Prefix, &t.Scopes,
		&t.CreatedAt, &lastUsed, &expires, &revoked)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("scan api_token: %w", err)
	}
	if t.Scopes == nil {
		t.Scopes = []string{}
	}
	t.LastUsedAt = lastUsed
	t.ExpiresAt = expires
	t.RevokedAt = revoked
	return &t, nil
}

func (r *APITokenRepoPg) GetByHash(ctx context.Context, hash string) (*domain.APIToken, error) {
	return scanAPIToken(r.pool.QueryRow(ctx,
		`SELECT `+apiTokenCols+` FROM api_tokens WHERE token_hash = $1`, hash))
}

func (r *APITokenRepoPg) ListByUser(ctx context.Context, userID string) ([]*domain.APIToken, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+apiTokenCols+` FROM api_tokens WHERE user_id = $1::uuid ORDER BY created_at DESC`,
		userID)
	if err != nil {
		return nil, fmt.Errorf("list api_tokens: %w", err)
	}
	defer rows.Close()
	var out []*domain.APIToken
	for rows.Next() {
		t, err := scanAPIToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *APITokenRepoPg) Create(ctx context.Context, t *domain.APIToken) error {
	err := r.pool.QueryRow(ctx, `
INSERT INTO api_tokens (user_id, name, token_hash, prefix, scopes, expires_at)
VALUES ($1::uuid, $2, $3, $4, $5, $6)
RETURNING id, created_at`,
		t.UserID, t.Name, t.TokenHash, t.Prefix, t.Scopes, t.ExpiresAt,
	).Scan(&t.ID, &t.CreatedAt)
	if err != nil {
		return fmt.Errorf("create api_token: %w", err)
	}
	return nil
}

func (r *APITokenRepoPg) Revoke(ctx context.Context, id, userID string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE api_tokens SET revoked_at=now() WHERE id=$1::uuid AND user_id=$2::uuid AND revoked_at IS NULL`,
		id, userID)
	if err != nil {
		return fmt.Errorf("revoke api_token: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *APITokenRepoPg) Delete(ctx context.Context, id, userID string) error {
	// §7.14: удалять можно только отозванные или истёкшие — для аудита.
	tag, err := r.pool.Exec(ctx, `
DELETE FROM api_tokens
WHERE id=$1::uuid AND user_id=$2::uuid
  AND (revoked_at IS NOT NULL OR (expires_at IS NOT NULL AND expires_at < now()))`,
		id, userID)
	if err != nil {
		return fmt.Errorf("delete api_token: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *APITokenRepoPg) TouchLastUsed(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE api_tokens SET last_used_at=now() WHERE id=$1::uuid`, id)
	return err
}
