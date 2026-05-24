package example

import (
	"context"

	"bus/app/internal/app"
)

type Service struct {
	app.App
}

var _ interface {
	Ping(ctx context.Context) (string, error)
} = (*Service)(nil)

func New(a app.App) *Service {
	return &Service{App: a}
}

func (s *Service) Ping(_ context.Context) (string, error) {
	return "pong", nil
}
