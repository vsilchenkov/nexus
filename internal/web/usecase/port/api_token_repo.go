package port

import (
	"context"

	"bus/internal/domain"
)

// APITokenRepo — CRUD для read-only API-токенов (§7.14).
type APITokenRepo interface {
	GetByHash(ctx context.Context, hash string) (*domain.APIToken, error)
	ListByUser(ctx context.Context, userID string) ([]*domain.APIToken, error)
	Create(ctx context.Context, t *domain.APIToken) error
	Revoke(ctx context.Context, id, userID string) error
	Delete(ctx context.Context, id, userID string) error
	TouchLastUsed(ctx context.Context, id string) error
}
