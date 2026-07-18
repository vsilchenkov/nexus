package port

import (
	"context"

	"nexus/internal/domain"
)

// APITokenRepo — CRUD для read-only API-токенов (§7.14).
type APITokenRepo interface {
	GetByHash(ctx context.Context, hash string) (*domain.APIToken, error)
	// ListByUser — токены пользователя по ВСЕМ командам: «Настройки» вне скоупа
	// команды, а команда каждого токена (§18.3) показывается колонкой.
	ListByUser(ctx context.Context, userID string) ([]*domain.APIToken, error)
	Create(ctx context.Context, t *domain.APIToken) error
	Revoke(ctx context.Context, id, userID string) error
	// Rotate перевыпускает значение токена (новые hash/prefix), сохраняя остальные
	// поля; last_used_at сбрасывается. Затрагивает только АКТИВНЫЙ токен (не
	// отозванный и не просроченный) — иначе ErrNotFound.
	Rotate(ctx context.Context, id, userID, newHash, newPrefix string) error
	Delete(ctx context.Context, id, userID string) error
	TouchLastUsed(ctx context.Context, id string) error
}
