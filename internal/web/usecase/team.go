package usecase

import (
	"context"
	"errors"
	"fmt"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// TeamUsecase — CRUD команд и членства (multi-tenancy v2, §16 ТЗ, Phase 10.C).
//
// Provisioning ClickHouse-БД при создании команды происходит через
// TeamProvisioner. Если CH-провижининг упал — PG-запись откатывается
// (см. Create). При nil provisioner'е (Web стартовал без ClickHouse)
// Create отказывается с ErrCHUnavailable — нельзя создать команду, в
// которую потом нельзя будет писать логи.
type TeamUsecase struct {
	repo        port.TeamRepo
	provisioner port.TeamProvisioner
	audit       *AuditUsecase
	logger      logging.Logger
}

// ErrCHUnavailable — попытка создать команду без подключённого ClickHouse.
var ErrCHUnavailable = errors.New("usecase: ClickHouse unavailable, cannot provision team database")

func NewTeamUsecase(
	repo port.TeamRepo,
	provisioner port.TeamProvisioner,
	audit *AuditUsecase,
	logger logging.Logger,
) *TeamUsecase {
	return &TeamUsecase{repo: repo, provisioner: provisioner, audit: audit, logger: logger}
}

func (u *TeamUsecase) Get(ctx context.Context, id string) (*domain.Team, error) {
	return u.repo.GetByID(ctx, id)
}

func (u *TeamUsecase) List(ctx context.Context) ([]*domain.Team, error) {
	return u.repo.List(ctx)
}

// Create создаёт команду атомарно: row в teams + CREATE DATABASE
// nexus_<slug>. Если CH-команда упала — PG-запись удаляется. CH-БД
// идемпотентна (IF NOT EXISTS), повторный вызов с тем же slug безопасен.
//
// Creator автоматически становится owner'ом через AddMember (otherwise
// он бы не увидел созданную команду в /api/me/teams).
func (u *TeamUsecase) Create(ctx context.Context, actor Actor, slug, name, creatorUserID string) (*domain.Team, error) {
	if u.provisioner == nil {
		return nil, ErrCHUnavailable
	}
	t := &domain.Team{
		Slug:       slug,
		Name:       name,
		CHDatabase: domain.CHDatabaseForSlug(slug),
	}
	if err := t.Validate(); err != nil {
		return nil, err
	}

	if err := u.repo.Create(ctx, t); err != nil {
		return nil, err
	}

	if err := u.provisioner.CreateDatabase(ctx, t.CHDatabase); err != nil {
		// Откат PG-записи; CH-БД могла остаться (IF NOT EXISTS) — это
		// допустимо: повторный Create с тем же slug пройдёт идемпотентно.
		if delErr := u.repo.Delete(ctx, t.ID); delErr != nil {
			u.logger.ErrorWithOp("rollback team after CH provision fail", delErr,
				"team.create.rollback",
				u.logger.Str("team_id", t.ID),
				u.logger.Str("slug", t.Slug))
		}
		return nil, fmt.Errorf("provision ch database: %w", err)
	}

	// Creator → owner.
	if creatorUserID != "" {
		if err := u.repo.AddMember(ctx, creatorUserID, t.ID, domain.TeamRoleOwner); err != nil {
			u.logger.Warn("add creator as owner failed (team created, membership missed)",
				u.logger.Str("team_id", t.ID),
				u.logger.Str("user_id", creatorUserID),
				u.logger.Err(err))
		}
	}

	u.audit.Log(ctx, actor, domain.ActionTeamCreate, "team", t.ID, map[string]any{
		"slug":        t.Slug,
		"name":        t.Name,
		"ch_database": t.CHDatabase,
	})
	return t, nil
}

// Update — меняет только name. Slug и ch_database immutable после
// создания (переименование БД ClickHouse сломало бы Sender в полёте).
func (u *TeamUsecase) Update(ctx context.Context, actor Actor, id, name string) (*domain.Team, error) {
	t, err := u.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	t.Name = name
	if err := t.Validate(); err != nil {
		return nil, err
	}
	if err := u.repo.Update(ctx, t); err != nil {
		return nil, err
	}
	u.audit.Log(ctx, actor, domain.ActionTeamUpdate, "team", t.ID, map[string]any{
		"name": t.Name,
	})
	return t, nil
}

// Delete — удаляет команду. ON DELETE RESTRICT на nodes/users.default_team_id
// означает: удалить команду нельзя, если в ней есть узлы или дефолтные
// юзеры. CH-БД остаётся как orphan — drop через UI «Orphans» (Phase 10.D).
func (u *TeamUsecase) Delete(ctx context.Context, actor Actor, id string) error {
	t, err := u.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if t.Slug == domain.DefaultTeamSlug {
		return domain.ErrPermissionDenied
	}
	if err := u.repo.Delete(ctx, id); err != nil {
		return err
	}
	u.audit.Log(ctx, actor, domain.ActionTeamDelete, "team", id, map[string]any{
		"slug":        t.Slug,
		"ch_database": t.CHDatabase,
	})
	return nil
}

// AddMember — добавляет пользователя в команду (или обновляет роль,
// см. ON CONFLICT в TeamRepoPg.AddMember).
func (u *TeamUsecase) AddMember(ctx context.Context, actor Actor, teamID, userID string, role domain.TeamRole) error {
	if err := u.repo.AddMember(ctx, userID, teamID, role); err != nil {
		return err
	}
	u.audit.Log(ctx, actor, domain.ActionTeamMemberAdd, "team_member", teamID, map[string]any{
		"user_id": userID,
		"role":    string(role),
	})
	return nil
}

// RemoveMember убирает пользователя из команды.
func (u *TeamUsecase) RemoveMember(ctx context.Context, actor Actor, teamID, userID string) error {
	if err := u.repo.RemoveMember(ctx, userID, teamID); err != nil {
		return err
	}
	u.audit.Log(ctx, actor, domain.ActionTeamMemberRemove, "team_member", teamID, map[string]any{
		"user_id": userID,
	})
	return nil
}

// UpdateMemberRole меняет роль пользователя в команде.
func (u *TeamUsecase) UpdateMemberRole(ctx context.Context, actor Actor, teamID, userID string, role domain.TeamRole) error {
	if err := u.repo.UpdateMemberRole(ctx, userID, teamID, role); err != nil {
		return err
	}
	u.audit.Log(ctx, actor, domain.ActionTeamMemberRole, "team_member", teamID, map[string]any{
		"user_id": userID,
		"role":    string(role),
	})
	return nil
}

// ListMembers — список членов команды.
func (u *TeamUsecase) ListMembers(ctx context.Context, teamID string) ([]*domain.TeamMember, error) {
	return u.repo.ListMembers(ctx, teamID)
}
