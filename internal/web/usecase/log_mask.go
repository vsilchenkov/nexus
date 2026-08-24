package usecase

import (
	"context"
	"regexp"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/reloader"
	"nexus/internal/web/usecase/port"
)

// LogMaskUsecase — справочник маскирования логов узлов (§95): CRUD из панели
// (admin-only) + превью. После каждой мутации публикует reload секции masking,
// чтобы Sender'ы перечитали набор шаблонов без рестарта.
type LogMaskUsecase struct {
	repo      port.LogMaskRepo
	audit     *AuditUsecase
	publisher ReloadPublisher
	logger    logging.Logger
}

func NewLogMaskUsecase(repo port.LogMaskRepo, audit *AuditUsecase, publisher ReloadPublisher, logger logging.Logger) *LogMaskUsecase {
	return &LogMaskUsecase{repo: repo, audit: audit, publisher: publisher, logger: logger}
}

// List возвращает все шаблоны (включая выключенные) в порядке применения.
func (u *LogMaskUsecase) List(ctx context.Context, q string) ([]*domain.LogMaskPattern, error) {
	return u.repo.List(ctx, q)
}

// Create добавляет шаблон и публикует reload.
func (u *LogMaskUsecase) Create(ctx context.Context, actor Actor, e *domain.LogMaskPattern) error {
	if err := e.Validate(); err != nil {
		return err
	}
	e.CreatedBy = actor.UserLogin
	e.UpdatedBy = actor.UserLogin
	if err := u.repo.Create(ctx, e); err != nil {
		return err
	}
	u.audit.Log(ctx, actor, domain.ActionLogMaskCreate, "log_mask", e.ID, map[string]any{"description": e.Description})
	u.publishReload(ctx)
	return nil
}

// Update меняет шаблон и публикует reload.
func (u *LogMaskUsecase) Update(ctx context.Context, actor Actor, e *domain.LogMaskPattern) error {
	if err := e.Validate(); err != nil {
		return err
	}
	e.UpdatedBy = actor.UserLogin
	if err := u.repo.Update(ctx, e); err != nil {
		return err
	}
	u.audit.Log(ctx, actor, domain.ActionLogMaskUpdate, "log_mask", e.ID, map[string]any{"enabled": e.Enabled})
	u.publishReload(ctx)
	return nil
}

// Delete удаляет шаблон и публикует reload.
func (u *LogMaskUsecase) Delete(ctx context.Context, actor Actor, id string) error {
	e, err := u.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := u.repo.Delete(ctx, id); err != nil {
		return err
	}
	u.audit.Log(ctx, actor, domain.ActionLogMaskDelete, "log_mask", id, map[string]any{"description": e.Description})
	u.publishReload(ctx)
	return nil
}

// LogMaskPreview — результат применения шаблона к образцу (§95).
type LogMaskPreview struct {
	Result  string
	Matched bool
}

// Preview применяет один шаблон к образцу без обращения к БД — живая проверка
// regex в форме. Невалидный шаблон → ErrLogMaskPatternInvalid (handler отдаёт
// его как 200 valid:false, чтобы недописанный ввод не сыпал 400 в консоль).
func (u *LogMaskUsecase) Preview(pattern, replacement, sample string) (LogMaskPreview, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return LogMaskPreview{}, domain.ErrLogMaskPatternInvalid
	}
	result := re.ReplaceAllString(sample, replacement)
	return LogMaskPreview{Result: result, Matched: result != sample}, nil
}

// publishReload — best-effort уведомление Sender'ов о смене справочника. Ошибка
// Redis pub/sub не должна валить мутацию (§51.9: тихий сбой фиксируем в лог).
func (u *LogMaskUsecase) publishReload(ctx context.Context) {
	if u.publisher == nil {
		return
	}
	if err := u.publisher.Publish(ctx, reloader.SectionMasking); err != nil {
		u.logger.Debug("log mask: publish reload failed (senders will pick up on next restart)",
			u.logger.Err(err))
	}
}
