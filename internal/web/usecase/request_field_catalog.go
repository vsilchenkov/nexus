package usecase

import (
	"context"
	"errors"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// RequestFieldCatalogUsecase — справочник полей запроса (§41): поиск с
// автодополнением и идемпотентное создание из combobox формы узла.
type RequestFieldCatalogUsecase struct {
	repo   port.RequestFieldCatalogRepo
	audit  *AuditUsecase
	logger logging.Logger
}

func NewRequestFieldCatalogUsecase(repo port.RequestFieldCatalogRepo, audit *AuditUsecase, logger logging.Logger) *RequestFieldCatalogUsecase {
	return &RequestFieldCatalogUsecase{repo: repo, audit: audit, logger: logger}
}

// Search — prefix-поиск (пустой q = топ-используемые).
func (u *RequestFieldCatalogUsecase) Search(ctx context.Context, q string, limit int) ([]*domain.RequestFieldCatalogEntry, error) {
	return u.repo.Search(ctx, q, limit)
}

// Create добавляет поле в справочник. Идемпотентно по case-insensitive имени:
// повторный вызов возвращает существующую запись (§41) — combobox формы узла
// создаёт поля без диалогов и без гонок.
func (u *RequestFieldCatalogUsecase) Create(ctx context.Context, actor Actor, e *domain.RequestFieldCatalogEntry) error {
	if err := e.Validate(); err != nil {
		return err
	}
	err := u.repo.Create(ctx, e)
	if errors.Is(err, domain.ErrRequestFieldAlreadyExists) {
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
	u.audit.Log(ctx, actor, domain.ActionRequestFieldCreate, "request_field", e.ID, map[string]any{"name": e.Name})
	return nil
}
