package usecase

import (
	"context"
	"time"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// AuditUsecase — тонкая обёртка над AuditRepo. Существует, чтобы
// другие usecase не зависели от port напрямую (для тестов им подсунем
// мок AuditUsecase, а не весь repo).
type AuditUsecase struct {
	repo   port.AuditRepo
	logger logging.Logger
}

func NewAuditUsecase(repo port.AuditRepo, logger logging.Logger) *AuditUsecase {
	return &AuditUsecase{repo: repo, logger: logger}
}

// Actor — кто выполнил действие.
//
// TeamID — UUID команды в контексте сессии актёра (multi-tenancy v2,
// Phase 10.F.1). Прокидывается в audit-журнал, чтобы admin мог
// фильтровать журнал по своей команде. Пустая строка = глобальное
// действие или legacy single-team.
type Actor struct {
	UserID    string
	UserLogin string
	TeamID    string
	IPAddress string
}

func SystemActor() Actor { return Actor{UserLogin: "system"} }

// Log пишет запись в журнал. Ошибки логируются, но НЕ пробрасываются —
// сбой аудита не должен ломать бизнес-операцию (§7.13).
func (u *AuditUsecase) Log(ctx context.Context, a Actor, action, targetType, targetID string, details map[string]any) {
	if details == nil {
		details = map[string]any{}
	}
	e := &domain.AuditEntry{
		UserID:     a.UserID,
		UserLogin:  a.UserLogin,
		TeamID:     a.TeamID,
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		Details:    details,
		IPAddress:  a.IPAddress,
		CreatedAt:  time.Now().UTC(),
	}
	if err := u.repo.Write(ctx, e); err != nil {
		u.logger.Warn("audit write failed",
			u.logger.Str("action", action),
			u.logger.Str("target_id", targetID),
			u.logger.Err(err))
	}
}

// List возвращает страницу записей по фильтру.
func (u *AuditUsecase) List(ctx context.Context, f port.AuditFilter) ([]*domain.AuditEntry, error) {
	return u.repo.List(ctx, f)
}

// auditEntry — фабрика *domain.AuditEntry для использования внутри
// UnitOfWork (когда нужно вызвать AuditRepo.Write напрямую). В отличие
// от AuditUsecase.Log, ошибка проброса вверх — потому что транзакция
// либо коммитится целиком, либо откатывается.
func auditEntry(a Actor, action, targetType, targetID string, details map[string]any) *domain.AuditEntry {
	if details == nil {
		details = map[string]any{}
	}
	return &domain.AuditEntry{
		UserID:     a.UserID,
		UserLogin:  a.UserLogin,
		TeamID:     a.TeamID,
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		Details:    details,
		IPAddress:  a.IPAddress,
		CreatedAt:  time.Now().UTC(),
	}
}
