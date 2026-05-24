package repository

import (
	"bus/app/internal/lib/caching"
	"bus/app/internal/lib/logging"
	"bus/app/internal/models"
	"bus/app/internal/storage"
	"bus/app/internal/storage/repository/user"
	"context"
)

type Repositorer interface {
	DB
	UserRepository
}

type DB interface {
	Close() error
}

type UserRepository interface {
	CreateUser(ctx context.Context, username, password string, role models.Role) (int, error)
	VerifyUser(ctx context.Context, username, password string) (int, *models.Role, error)
	ChangeUserPassword(ctx context.Context, username, password string) error
}

type Repository struct {
	DB
	UserRepository
}

func NewRepository(db storage.DB, cacher caching.Cacher, logger logging.Logger) *Repository {
	return &Repository{
		DB:             db,
		UserRepository: user.NewUserRepository(db, cacher, logger),
	}
}
