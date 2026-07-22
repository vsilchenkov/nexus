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

// List возвращает пользователей. По умолчанию (пустой f.TeamID) список
// глобальный: пользователь — глобальная сущность, членство в командах —
// отдельная ось (§18). Непустой f.TeamID — опциональный фильтр по команде
// (repo добавит JOIN user_teams). /api/users — admin-only, поэтому глобальный
// список видит только глобальный админ.
func (u *UserUsecase) List(ctx context.Context, f port.ListUsersFilter) ([]*domain.User, error) {
	return u.users.List(ctx, f)
}

// UserWithTeams — пользователь + его команды (членства) для списка (§44.G).
type UserWithTeams struct {
	User  *domain.User
	Teams []*domain.UserTeam
}

// ListWithTeams — список пользователей, обогащённый членствами в командах (§44.G,
// колонка «Команды» в Settings → Users). Членства тянутся одним батч-запросом
// (без N+1). Ошибка обогащения деградирует до списка без команд (не 500).
func (u *UserUsecase) ListWithTeams(ctx context.Context, f port.ListUsersFilter) ([]UserWithTeams, error) {
	users, err := u.users.List(ctx, f)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(users))
	for i, usr := range users {
		ids[i] = usr.ID
	}
	byUser, err := u.teams.ListTeamsByUsers(ctx, ids)
	if err != nil {
		u.logger.Warn("list users: teams enrichment failed", u.logger.Err(err))
		byUser = map[string][]*domain.UserTeam{}
	}
	out := make([]UserWithTeams, len(users))
	for i, usr := range users {
		out[i] = UserWithTeams{User: usr, Teams: byUser[usr.ID]}
	}
	return out, nil
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
		if err := validatePassword(password); err != nil {
			return err
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
		"login": in.Login, "name": in.Name, "role": string(in.Role), "team_id": teamID,
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
	// §66: след переименования в аудите. Активные сессии пользователя несут
	// старое имя до следующего входа (осознанно: рестартовать сессии из-за
	// смены подписи не стоит).
	details := map[string]any{"name": in.Name}
	if old.Name != in.Name {
		details["prev_name"] = old.Name
	}
	u.audit.Log(ctx, actor, domain.ActionUserUpdate, "user", in.ID, details)
	return nil
}

// SetDefaultTeam меняет команду по умолчанию пользователя (§45, inline-смена в
// списке Settings → Users). Валидирует, что teamID входит в членства пользователя
// (иначе ErrUserNotTeamMember): сделать дефолтной чужую команду нельзя — §18.9 при
// входе всё равно перекинул бы на команду по фактическому членству. Существующие
// сессии не трогаем: default_team_id влияет на резолв current_team при СЛЕДУЮЩЕМ
// входе, активная сессия уже несёт свой current_team.
func (u *UserUsecase) SetDefaultTeam(ctx context.Context, actor Actor, userID, teamID string) error {
	if userID == "" || teamID == "" {
		return domain.ErrUserNotFound
	}
	// Пользователь должен существовать (даёт 404 на невалидный/несуществующий id).
	if _, err := u.users.Get(ctx, userID); err != nil {
		return err
	}
	memberships, err := u.teams.ListUserTeams(ctx, userID)
	if err != nil {
		return err
	}
	member := false
	for _, m := range memberships {
		if m.Team.ID == teamID {
			member = true
			break
		}
	}
	if !member {
		return domain.ErrUserNotTeamMember
	}
	if err := u.users.UpdateDefaultTeam(ctx, userID, teamID); err != nil {
		return err
	}
	u.audit.Log(ctx, actor, domain.ActionUserUpdate, "user", userID, map[string]any{
		"default_team_id": teamID,
	})
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
