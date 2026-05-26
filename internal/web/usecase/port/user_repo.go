package port

import (
	"context"
	"time"

	"nexus/internal/domain"
)

// ListUsersFilter — фильтры списка пользователей.
type ListUsersFilter struct {
	Search string
	Limit  int
	Offset int
}

// UserRepo — CRUD пользователей UI (§7.9).
type UserRepo interface {
	Get(ctx context.Context, id string) (*domain.User, error)
	GetByLogin(ctx context.Context, login string) (*domain.User, error)
	List(ctx context.Context, f ListUsersFilter) ([]*domain.User, error)
	CountActiveAdmins(ctx context.Context) (int, error)
	Create(ctx context.Context, u *domain.User) error
	Update(ctx context.Context, u *domain.User) error
	UpdatePassword(ctx context.Context, id, passwordHash string, mustChange bool) error
	UpdateLastLogin(ctx context.Context, id string, at time.Time) error
	Delete(ctx context.Context, id string) error
}

// SessionRepo — Redis-сессии (§7.1).
type SessionRepo interface {
	Create(ctx context.Context, s *domain.Session, ttl time.Duration) error
	Get(ctx context.Context, token string) (*domain.Session, error)
	Touch(ctx context.Context, token string, ttl time.Duration) error
	Delete(ctx context.Context, token string) error
	DeleteByUser(ctx context.Context, userID string) (int, error)
}
