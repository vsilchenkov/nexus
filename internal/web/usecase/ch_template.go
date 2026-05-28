package usecase

import (
	"context"
	"errors"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// CHTemplateUsecase — CRUD глобального каталога шаблонов CH-таблиц (§19).
//
// Verify требует TeamProvisioner (live-проверка DDL в ClickHouse). При nil
// provisioner'е (Web без CH) Verify возвращает ErrCHUnavailable. teams нужен,
// чтобы взять БД default-команды (nexus_default) для временной таблицы.
type CHTemplateUsecase struct {
	repo        port.CHTemplateRepo
	provisioner port.TeamProvisioner
	teams       port.TeamRepo
	audit       *AuditUsecase
	logger      logging.Logger
}

func NewCHTemplateUsecase(
	repo port.CHTemplateRepo,
	provisioner port.TeamProvisioner,
	teams port.TeamRepo,
	audit *AuditUsecase,
	logger logging.Logger,
) *CHTemplateUsecase {
	return &CHTemplateUsecase{repo: repo, provisioner: provisioner, teams: teams, audit: audit, logger: logger}
}

func (u *CHTemplateUsecase) Get(ctx context.Context, id string) (*domain.CHTemplate, error) {
	return u.repo.Get(ctx, id)
}

func (u *CHTemplateUsecase) List(ctx context.Context) ([]*domain.CHTemplate, error) {
	return u.repo.List(ctx)
}

func (u *CHTemplateUsecase) Create(ctx context.Context, actor Actor, t *domain.CHTemplate) error {
	if err := t.Validate(); err != nil {
		return err
	}
	if t.IsDefault {
		if err := u.clearOtherDefault(ctx, ""); err != nil {
			return err
		}
	}
	if err := u.repo.Create(ctx, t); err != nil {
		return err
	}
	u.audit.Log(ctx, actor, domain.ActionCHTemplateCreate, "ch_template", t.ID, map[string]any{
		"name":       t.Name,
		"is_default": t.IsDefault,
	})
	return nil
}

func (u *CHTemplateUsecase) Update(ctx context.Context, actor Actor, t *domain.CHTemplate) error {
	if err := t.Validate(); err != nil {
		return err
	}
	// Существование проверяем до сброса чужого default — иначе при несуществующем
	// id мы бы сняли флаг с реального default, а Update упал бы с NotFound.
	if _, err := u.repo.Get(ctx, t.ID); err != nil {
		return err
	}
	if t.IsDefault {
		if err := u.clearOtherDefault(ctx, t.ID); err != nil {
			return err
		}
	}
	if err := u.repo.Update(ctx, t); err != nil {
		return err
	}
	u.audit.Log(ctx, actor, domain.ActionCHTemplateUpdate, "ch_template", t.ID, map[string]any{
		"name":       t.Name,
		"is_default": t.IsDefault,
	})
	return nil
}

func (u *CHTemplateUsecase) Delete(ctx context.Context, actor Actor, id string) error {
	t, err := u.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if t.IsDefault {
		return domain.ErrCHTemplateDefaultImmutable
	}
	cnt, err := u.repo.CountNodesUsing(ctx, id)
	if err != nil {
		return err
	}
	if cnt > 0 {
		return domain.ErrCHTemplateInUse
	}
	if err := u.repo.Delete(ctx, id); err != nil {
		return err
	}
	u.audit.Log(ctx, actor, domain.ActionCHTemplateDelete, "ch_template", id, map[string]any{
		"name": t.Name,
	})
	return nil
}

// Verify проверяет шаблон статически (Validate) и «вживую» (пробное создание
// временной таблицы в БД default-команды). ErrCHUnavailable — provisioner nil.
func (u *CHTemplateUsecase) Verify(ctx context.Context, t *domain.CHTemplate) error {
	if err := t.Validate(); err != nil {
		return err
	}
	if u.provisioner == nil {
		return ErrCHUnavailable
	}
	team, err := u.teams.GetBySlug(ctx, domain.DefaultTeamSlug)
	if err != nil {
		return err
	}
	return u.provisioner.VerifyTemplate(ctx, team.CHDatabase, t)
}

// clearOtherDefault снимает флаг is_default с текущего default-шаблона, если он
// не exceptID. Нужно, чтобы partial unique-индекс ch_templates_one_default не
// конфликтовал при назначении нового default.
func (u *CHTemplateUsecase) clearOtherDefault(ctx context.Context, exceptID string) error {
	cur, err := u.repo.GetDefault(ctx)
	if err != nil {
		if errors.Is(err, domain.ErrCHTemplateNotFound) {
			return nil
		}
		return err
	}
	if cur.ID == exceptID {
		return nil
	}
	cur.IsDefault = false
	return u.repo.Update(ctx, cur)
}
