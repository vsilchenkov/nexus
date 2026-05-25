package port

import (
	"context"

	"bus/internal/domain"
)

// AppSettingsRepo — singleton-репозиторий динамических настроек (§14.5 ТЗ).
//
// Get всегда возвращает заполненную структуру (миграция 0006 гарантирует
// строку с id=1) — нет ErrNotFound сценария.
type AppSettingsRepo interface {
	Get(ctx context.Context) (*domain.AppSettings, error)
	Update(ctx context.Context, s *domain.AppSettings) error
}
