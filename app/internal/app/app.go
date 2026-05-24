package app

import (
	"bus/app/internal/config"
	"bus/app/internal/lib/caching"
	"bus/app/internal/lib/logging"
	"bus/app/internal/storage/repository"
	"context"
)

type App struct {
	Ctx    context.Context
	Store  repository.Repositorer
	Cacher caching.Cacher
	Config *config.Config
	Logger logging.Logger
	Cancel context.CancelFunc
}

func New(ctx context.Context, store repository.Repositorer, cacher caching.Cacher, c *config.Config, logger logging.Logger, cancel context.CancelFunc) App {
	return App{
		Ctx:    ctx,
		Store:  store,
		Cacher: cacher,
		Config: c,
		Logger: logger,
		Cancel: cancel}
}
