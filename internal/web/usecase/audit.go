package usecase

import (
	"context"
	"time"

	"bus/internal/domain"
	"bus/internal/platform/logging"
	"bus/internal/web/usecase/port"
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

// Actor — кто выполнил действие. В Phase 2 — всегда system; в Phase 3
// будем брать из request-context (middleware auth положит User).
type Actor struct {
	UserID    string
	UserLogin string
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
