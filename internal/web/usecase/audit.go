package usecase

import (
	"context"
	"fmt"
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
	teams  port.TeamRepo // §86.7: только для сквозного скоупа; nil — режим выключен
	logger logging.Logger
}

func NewAuditUsecase(repo port.AuditRepo, logger logging.Logger) *AuditUsecase {
	return &AuditUsecase{repo: repo, logger: logger}
}

// WithTeams включает сквозной режим журнала (§86.7).
//
// Builder, а не параметр конструктора: AuditUsecase создаётся в полутора
// десятках мест (каждый usecase пишет свой аудит), и большинству из них членства
// не нужны — расширять сигнатуру ради одного вызова значило бы править их все.
// nil-репозиторий оставляет ListAcrossTeams недоступным, а не падающим.
func (u *AuditUsecase) WithTeams(teams port.TeamRepo) *AuditUsecase {
	u.teams = teams
	return u
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

// ListAcrossTeams — журнал по всем командам пользователя (§86.7).
//
// Это НЕ то же самое, что существующий `?team_id=*`: тот снимает фильтр вовсе и
// показывает журнал всего инстанса (admin-only), а здесь скоуп ограничен
// членствами и доступен любому, кто вправе читать журнал (manager+, §26.3).
//
// Пустые членства → пустая выдача БЕЗ запроса: пустой TeamIDs в адаптере
// означал бы «фильтра нет», то есть ровно тот глобальный журнал, которого здесь
// быть не должно.
func (u *AuditUsecase) ListAcrossTeams(ctx context.Context, userID string, f port.AuditFilter) ([]*domain.AuditEntry, error) {
	teamIDs, _, err := teamScope(ctx, u.teams, userID)
	if err != nil {
		return nil, fmt.Errorf("audit across teams: %w", err)
	}
	if len(teamIDs) == 0 {
		u.logger.Debug("audit across teams: user has no memberships",
			u.logger.Str("user_id", userID))
		return []*domain.AuditEntry{}, nil
	}
	f.TeamID = ""
	f.TeamIDs = teamIDs
	entries, err := u.repo.List(ctx, f)
	if err != nil {
		return nil, fmt.Errorf("audit across teams: list: %w", err)
	}
	u.logger.Debug("audit across teams",
		u.logger.Str("user_id", userID), u.logger.Int("teams", len(teamIDs)),
		u.logger.Int("entries", len(entries)))
	return entries, nil
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
