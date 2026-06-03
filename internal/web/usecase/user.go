package usecase

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// UserUsecase — CRUD пользователей UI (§7.9, §7.10).
//
// teams + defaultTeamID — multi-tenancy v2 (Phase 11.A): List scope'ится
// по членству в команде, Create добавляет membership в текущую команду.
type UserUsecase struct {
	users         port.UserRepo
	sessions      port.SessionRepo
	teams         port.TeamRepo
	audit         *AuditUsecase
	defaultTeamID string
	logger        logging.Logger
}

func NewUserUsecase(
	users port.UserRepo,
	sessions port.SessionRepo,
	teams port.TeamRepo,
	audit *AuditUsecase,
	defaultTeamID string,
	logger logging.Logger,
) *UserUsecase {
	return &UserUsecase{
		users:         users,
		sessions:      sessions,
		teams:         teams,
		audit:         audit,
		defaultTeamID: defaultTeamID,
		logger:        logger,
	}
}

func (u *UserUsecase) Get(ctx context.Context, id string) (*domain.User, error) {
	return u.users.Get(ctx, id)
}

// List возвращает пользователей текущей команды. Пустой f.TeamID →
// defaultTeamID (fallback для CLI/legacy).
func (u *UserUsecase) List(ctx context.Context, f port.ListUsersFilter) ([]*domain.User, error) {
	if f.TeamID == "" {
		f.TeamID = u.defaultTeamID
	}
	return u.users.List(ctx, f)
}

// Create — создание пользователя (admin only — проверка ролей в handler).
//
// teamID — команда, в которую добавляется membership (Phase 11.A): без
// строки в user_teams пользователь не попал бы в scoped-список List.
// Пустой teamID → defaultTeamID. default_team_id юзера выставляется
// репозиторием (COALESCE на default), здесь же добавляем явное членство.
func (u *UserUsecase) Create(ctx context.Context, actor Actor, teamID string, in *domain.User, password string) error {
	if !in.Role.Valid() {
		return errors.New("invalid role")
	}
	if !in.Lang.Valid() {
		in.Lang = domain.UserLangEN
	}
	if password != "" {
		if len(password) < 8 {
			return errors.New("password must be at least 8 characters")
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return fmt.Errorf("hash: %w", err)
		}
		in.PasswordHash = string(hash)
	}
	if err := u.users.Create(ctx, in); err != nil {
		return err
	}
	if teamID == "" {
		teamID = u.defaultTeamID
	}
	if teamID != "" {
		if err := u.teams.AddMember(ctx, in.ID, teamID, domain.TeamRoleMember); err != nil {
			u.logger.Warn("add new user to team failed (user created, membership missed)",
				u.logger.Str("user_id", in.ID),
				u.logger.Str("team_id", teamID),
				u.logger.Err(err))
		}
	}
	u.audit.Log(ctx, actor, domain.ActionUserCreate, "user", in.ID, map[string]any{
		"login": in.Login, "role": string(in.Role), "team_id": teamID,
	})
	return nil
}

// Update — изменение пользователя. Проверки «нельзя удалить/понизить
// последнего активного админа» и «нельзя самому себе понизить роль» —
// в handler через actor.
func (u *UserUsecase) Update(ctx context.Context, actor Actor, in *domain.User) error {
	old, err := u.users.Get(ctx, in.ID)
	if err != nil {
		return err
	}
	if old.Role == domain.UserRoleAdmin && old.Active &&
		(in.Role != domain.UserRoleAdmin || !in.Active) {
		count, err := u.users.CountActiveAdmins(ctx)
		if err != nil {
			return err
		}
		if count <= 1 {
			return errors.New("cannot demote/disable the last active admin")
		}
	}
	if err := u.users.Update(ctx, in); err != nil {
		return err
	}
	// Если поменялась роль или active — выкидываем все сессии.
	if old.Role != in.Role || old.Active != in.Active {
		_, _ = u.sessions.DeleteByUser(ctx, in.ID)
	}
	u.audit.Log(ctx, actor, domain.ActionUserUpdate, "user", in.ID, nil)
	return nil
}

// Delete — удаление. Та же проверка по последнему активному админу.
func (u *UserUsecase) Delete(ctx context.Context, actor Actor, id string) error {
	if actor.UserID != "" && actor.UserID == id {
		return errors.New("cannot delete yourself")
	}
	old, err := u.users.Get(ctx, id)
	if err != nil {
		return err
	}
	if old.Role == domain.UserRoleAdmin && old.Active {
		count, err := u.users.CountActiveAdmins(ctx)
		if err != nil {
			return err
		}
		if count <= 1 {
			return errors.New("cannot delete the last active admin")
		}
	}
	if err := u.users.Delete(ctx, id); err != nil {
		return err
	}
	_, _ = u.sessions.DeleteByUser(ctx, id)
	u.audit.Log(ctx, actor, domain.ActionUserDelete, "user", id, map[string]any{
		"login": old.Login,
	})
	return nil
}
