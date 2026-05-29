package usecase

import (
	"context"
	"errors"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// HeaderCatalogUsecase — справочник HTTP-заголовков (§24): поиск с
// автодополнением и идемпотентное создание из combobox формы узла.
type HeaderCatalogUsecase struct {
	repo   port.HeaderCatalogRepo
	audit  *AuditUsecase
	logger logging.Logger
}

func NewHeaderCatalogUsecase(repo port.HeaderCatalogRepo, audit *AuditUsecase, logger logging.Logger) *HeaderCatalogUsecase {
	return &HeaderCatalogUsecase{repo: repo, audit: audit, logger: logger}
}

// Search — prefix-поиск (пустой q = топ-используемые).
func (u *HeaderCatalogUsecase) Search(ctx context.Context, q string, limit int) ([]*domain.HeaderCatalogEntry, error) {
	return u.repo.Search(ctx, q, limit)
}

// Create добавляет заголовок в справочник. Идемпотентно по case-insensitive
// имени: повторный вызов возвращает существующую запись (§24) — combobox
// формы узла создаёт заголовки без диалогов и без гонок.
func (u *HeaderCatalogUsecase) Create(ctx context.Context, actor Actor, e *domain.HeaderCatalogEntry) error {
	if err := e.Validate(); err != nil {
		return err
	}
	err := u.repo.Create(ctx, e)
	if errors.Is(err, domain.ErrHeaderAlreadyExists) {
		existing, getErr := u.repo.GetByName(ctx, e.Name)
		if getErr != nil {
			return getErr
		}
		*e = *existing
		return nil
	}
	if err != nil {
		return err
	}
	u.audit.Log(ctx, actor, domain.ActionHeaderCreate, "header", e.ID, map[string]any{"name": e.Name})
	return nil
}
