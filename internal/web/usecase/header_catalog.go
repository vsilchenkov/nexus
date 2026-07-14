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

// Get возвращает запись справочника по id (с usage_count).
func (u *HeaderCatalogUsecase) Get(ctx context.Context, id string) (*domain.HeaderCatalogEntry, error) {
	return u.repo.Get(ctx, id)
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

// Update меняет описание (всегда) и имя (только если usage_count = 0 — узлы
// ссылаются на заголовок по имени строкой в forward_headers, авто-каскада нет,
// иначе ссылки узлов «осиротеют»). Переименование в занятое имя →
// ErrHeaderAlreadyExists.
func (u *HeaderCatalogUsecase) Update(ctx context.Context, actor Actor, id, name, description string) error {
	cand := &domain.HeaderCatalogEntry{Name: name, Description: description}
	if err := cand.Validate(); err != nil {
		return err
	}
	existing, err := u.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	nameChanged := existing.Name != name
	if nameChanged {
		if existing.UsageCount > 0 {
			return domain.ErrHeaderInUse
		}
		if err := u.repo.UpdateName(ctx, id, name); err != nil {
			return err
		}
	}
	if existing.Description != description {
		if err := u.repo.UpdateDescription(ctx, id, description); err != nil {
			return err
		}
	}
	u.audit.Log(ctx, actor, domain.ActionHeaderUpdate, "header", id, map[string]any{
		"name":         name,
		"name_changed": nameChanged,
	})
	return nil
}

// Delete удаляет заголовок из справочника. Запрещено, если он используется хотя
// бы одним узлом (usage_count > 0) — удаление осиротило бы имя в forward_headers.
func (u *HeaderCatalogUsecase) Delete(ctx context.Context, actor Actor, id string) error {
	e, err := u.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if e.UsageCount > 0 {
		return domain.ErrHeaderInUse
	}
	if err := u.repo.Delete(ctx, id); err != nil {
		return err
	}
	u.audit.Log(ctx, actor, domain.ActionHeaderDelete, "header", id, map[string]any{"name": e.Name})
	return nil
}
