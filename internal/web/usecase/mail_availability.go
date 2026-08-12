package usecase

import (
	"context"
	"sync/atomic"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
)

// MailAvailabilityProvider — атомарный держатель признака «восстановление
// пароля доступно» (§88.4.5).
//
// Зачем провайдер, а не чтение из БД по требованию: признак отдаётся публичным
// GET /api/version, который запрашивает форма входа и опрашивают соседние
// инстансы (§73). Сегодня этот эндпоинт на боевой установке не делает ни
// одного запроса в БД (провайдер версии дёргается только при
// web.allow_version_override, то есть в dev) — вешать на него чтение
// app_settings нельзя.
//
// Значение сидится на старте и обновляется по reload-событию секции mail на
// всех репликах. Приём тот же, что у SessionTTLProvider (§34.2).
//
// Безопасен для конкурентного доступа.
type MailAvailabilityProvider struct {
	ready  atomic.Bool
	repo   appSettingsReader
	logger logging.Logger
}

// appSettingsReader — минимум, нужный провайдеру: немаскированное чтение
// настроек. Интерфейс объявлен на стороне потребителя (CLAUDE.md §3).
type appSettingsReader interface {
	Get(ctx context.Context) (*domain.AppSettings, error)
}

func NewMailAvailabilityProvider(repo appSettingsReader, logger logging.Logger) *MailAvailabilityProvider {
	return &MailAvailabilityProvider{repo: repo, logger: logger}
}

// Get возвращает текущий признак. До первого успешного Refresh — false
// (fail-closed: ссылки «Забыли пароль?» на форме входа не будет).
func (p *MailAvailabilityProvider) Get() bool {
	if p == nil {
		return false
	}
	return p.ready.Load()
}

// Refresh перечитывает настройки и обновляет признак. Вызывается на старте и
// из reload-подписчика секции mail.
//
// Ошибка чтения не сбрасывает уже известное значение в false: провал одного
// запроса к БД не повод убирать со страницы входа работающую функцию.
func (p *MailAvailabilityProvider) Refresh(ctx context.Context) error {
	s, err := p.repo.Get(ctx)
	if err != nil {
		p.logger.Warn("mail availability refresh failed",
			p.logger.Err(err))
		return err
	}
	ready := domain.MailPasswordResetReady(s)
	prev := p.ready.Swap(ready)
	if prev != ready {
		// §51.9: смена признака меняет ВИДИМОСТЬ элемента на форме входа —
		// без этой строки «почему пропала ссылка» выясняется только по БД.
		p.logger.Info("mail availability changed",
			p.logger.Any("password_reset_ready", ready))
	}
	return nil
}
