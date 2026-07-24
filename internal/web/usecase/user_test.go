package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

func newUserUC(users *authUserRepo, sessions *memSessionRepo) (*UserUsecase, *stubAuditRepo) {
	repo := &stubAuditRepo{}
	audit := NewAuditUsecase(repo, logging.NewNoop())
	// nopTeamRepo и defaultTeamID — multi-tenancy v2 (Phase 11.A).
	uc := NewUserUsecase(users, sessions, &nopTeamRepo{}, audit,
		"00000000-0000-0000-0000-000000000000", logging.NewNoop())
	return uc, repo
}

func TestUserUC_Get_Delegates(t *testing.T) {
	t.Parallel()
	users := newAuthUserRepo()
	users.put(&domain.User{ID: "u1", Login: "alice"})
	uc, _ := newUserUC(users, newMemSessionRepo())

	got, err := uc.Get(context.Background(), "u1")
	require.NoError(t, err)
	assert.Equal(t, "alice", got.Login)
}

func TestUserUC_List_Delegates(t *testing.T) {
	t.Parallel()
	users := newAuthUserRepo()
	users.put(&domain.User{ID: "u1"})
	users.put(&domain.User{ID: "u2"})
	uc, _ := newUserUC(users, newMemSessionRepo())

	list, err := uc.List(context.Background(), port.ListUsersFilter{})
	require.NoError(t, err)
	assert.Len(t, list, 2)
}

// TestUserUC_List_Global_NoTeamScope: List НЕ навязывает team-scope — пустой
// фильтр уходит в repo как есть (TeamID==""), значит список глобальный (§18).
// Раньше (Phase 11.A) usecase подставлял defaultTeamID, из-за чего в одной
// команде не было видно пользователей другой.
func TestUserUC_List_Global_NoTeamScope(t *testing.T) {
	t.Parallel()
	users := newAuthUserRepo()
	users.put(&domain.User{ID: "u1"})
	uc, _ := newUserUC(users, newMemSessionRepo()) // defaultTeamID непустой

	_, err := uc.List(context.Background(), port.ListUsersFilter{})
	require.NoError(t, err)
	assert.Empty(t, users.lastListFilter.TeamID,
		"List не должен подставлять team-scope — список глобальный (§18)")
}

func TestUserUC_Create_InvalidRole(t *testing.T) {
	t.Parallel()
	uc, _ := newUserUC(newAuthUserRepo(), newMemSessionRepo())

	err := uc.Create(context.Background(), SystemActor(), "",
		&domain.User{ID: "u1", Login: "alice", Role: "editor"}, "longenough")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid role")
}

func TestUserUC_Create_PasswordTooShort(t *testing.T) {
	t.Parallel()
	uc, _ := newUserUC(newAuthUserRepo(), newMemSessionRepo())

	err := uc.Create(context.Background(), SystemActor(), "",
		&domain.User{ID: "u1", Login: "alice", Role: domain.UserRoleViewer}, "short")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least 8")
}

func TestUserUC_Create_DefaultsLang(t *testing.T) {
	t.Parallel()
	users := newAuthUserRepo()
	uc, _ := newUserUC(users, newMemSessionRepo())

	u := &domain.User{ID: "u1", Login: "alice", Role: domain.UserRoleAdmin, Lang: ""}
	require.NoError(t, uc.Create(context.Background(), SystemActor(), "", u, ""))
	assert.Equal(t, domain.UserLangEN, u.Lang)
}

func TestUserUC_Create_Happy(t *testing.T) {
	t.Parallel()
	users := newAuthUserRepo()
	uc, audit := newUserUC(users, newMemSessionRepo())

	u := &domain.User{ID: "u1", Login: "alice", Role: domain.UserRoleAdmin, Lang: domain.UserLangRU}
	require.NoError(t, uc.Create(context.Background(), SystemActor(), "", u, "secret123"))

	assert.NotEmpty(t, u.PasswordHash, "password must be hashed")
	assert.NotEqual(t, "secret123", u.PasswordHash, "plaintext must not be stored")
	assert.Equal(t, 1, users.createCalls)

	require.Len(t, audit.entries, 1)
	assert.Equal(t, domain.ActionUserCreate, audit.entries[0].Action)
	assert.Equal(t, "alice", audit.entries[0].Details["login"])
	assert.Equal(t, string(domain.UserRoleAdmin), audit.entries[0].Details["role"])
}

// recordingTeamRepo — фиксирует вызовы AddMember (Phase 11.A).
type recordingTeamRepo struct {
	nopTeamRepo
	addUserID string
	addTeamID string
	addRole   domain.TeamRole
	addCalls  int
}

func (r *recordingTeamRepo) AddMember(_ context.Context, userID, teamID string, role domain.TeamRole) error {
	r.addCalls++
	r.addUserID, r.addTeamID, r.addRole = userID, teamID, role
	return nil
}

// TestUserUC_Create_AddsMembership: при создании юзера в команде должна
// появиться строка в user_teams (иначе юзер не попадёт в scoped-список).
func TestUserUC_Create_AddsMembership(t *testing.T) {
	t.Parallel()
	users := newAuthUserRepo()
	teams := &recordingTeamRepo{}
	audit := NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop())
	uc := NewUserUsecase(users, newMemSessionRepo(), teams, audit,
		"default-team-uuid", logging.NewNoop())

	u := &domain.User{ID: "u9", Login: "bob", Role: domain.UserRoleViewer}
	require.NoError(t, uc.Create(context.Background(), SystemActor(), "team-acme", u, "secret123"))

	assert.Equal(t, 1, teams.addCalls, "Create must add user_teams membership")
	assert.Equal(t, "u9", teams.addUserID)
	assert.Equal(t, "team-acme", teams.addTeamID)
	assert.Equal(t, domain.TeamRoleMember, teams.addRole)
}

// TestUserUC_Create_AddsMembership_DefaultFallback: пустой teamID →
// defaultTeamID.
func TestUserUC_Create_AddsMembership_DefaultFallback(t *testing.T) {
	t.Parallel()
	users := newAuthUserRepo()
	teams := &recordingTeamRepo{}
	audit := NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop())
	uc := NewUserUsecase(users, newMemSessionRepo(), teams, audit,
		"default-team-uuid", logging.NewNoop())

	u := &domain.User{ID: "u10", Login: "carol", Role: domain.UserRoleViewer}
	require.NoError(t, uc.Create(context.Background(), SystemActor(), "", u, "secret123"))

	assert.Equal(t, "default-team-uuid", teams.addTeamID, "empty teamID falls back to default")
}

func TestUserUC_Create_RepoError(t *testing.T) {
	t.Parallel()
	users := newAuthUserRepo()
	users.createErr = errors.New("db down")
	uc, audit := newUserUC(users, newMemSessionRepo())

	err := uc.Create(context.Background(), SystemActor(), "",
		&domain.User{ID: "u1", Login: "alice", Role: domain.UserRoleAdmin}, "")
	require.Error(t, err)
	// При ошибке Create в audit ничего не должно попасть.
	assert.Len(t, audit.entries, 0)
}

func TestUserUC_Update_LastAdmin_Demote_Rejected(t *testing.T) {
	t.Parallel()
	users := newAuthUserRepo()
	old := &domain.User{ID: "u1", Login: "admin", Role: domain.UserRoleAdmin, Active: true}
	users.put(old)
	users.activeAdmins = 1
	uc, _ := newUserUC(users, newMemSessionRepo())

	upd := &domain.User{ID: "u1", Role: domain.UserRoleViewer, Active: true}
	err := uc.Update(context.Background(), SystemActor(), upd)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "last active admin")
	assert.Equal(t, 0, users.updateCalls)
}

func TestUserUC_Update_LastAdmin_Disable_Rejected(t *testing.T) {
	t.Parallel()
	users := newAuthUserRepo()
	users.put(&domain.User{ID: "u1", Login: "admin", Role: domain.UserRoleAdmin, Active: true})
	users.activeAdmins = 1
	uc, _ := newUserUC(users, newMemSessionRepo())

	err := uc.Update(context.Background(), SystemActor(),
		&domain.User{ID: "u1", Role: domain.UserRoleAdmin, Active: false})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "last active admin")
}

func TestUserUC_Update_RoleOrActiveChange_PurgesSessions(t *testing.T) {
	t.Parallel()
	users := newAuthUserRepo()
	users.put(&domain.User{ID: "u1", Login: "alice", Role: domain.UserRoleViewer, Active: true})
	sessions := newMemSessionRepo()
	sessions.byToken["tok"] = &domain.Session{Token: "tok", UserID: "u1"}

	uc, audit := newUserUC(users, sessions)
	// admins count > 1 — допустим понижение.
	users.activeAdmins = 5

	upd := &domain.User{ID: "u1", Login: "alice", Role: domain.UserRoleAdmin, Active: true}
	require.NoError(t, uc.Update(context.Background(), SystemActor(), upd))

	assert.Equal(t, 1, users.updateCalls)
	assert.Contains(t, sessions.deletedByUser, "u1")

	require.Len(t, audit.entries, 1)
	assert.Equal(t, domain.ActionUserUpdate, audit.entries[0].Action)
}

// §66: переименование пользователя оставляет след в аудите (name + prev_name).
func TestUserUC_Update_Rename_AuditTrail(t *testing.T) {
	t.Parallel()
	users := newAuthUserRepo()
	users.put(&domain.User{ID: "u1", Login: "alice", Name: "Alice",
		Role: domain.UserRoleViewer, Active: true})
	uc, audit := newUserUC(users, newMemSessionRepo())

	upd := &domain.User{ID: "u1", Login: "alice", Name: "Alice Cooper",
		Role: domain.UserRoleViewer, Active: true}
	require.NoError(t, uc.Update(context.Background(), SystemActor(), upd))

	require.Len(t, audit.entries, 1)
	assert.Equal(t, "Alice Cooper", audit.entries[0].Details["name"])
	assert.Equal(t, "Alice", audit.entries[0].Details["prev_name"])
}

func TestUserUC_Update_NoRoleNoActiveChange_NoSessionPurge(t *testing.T) {
	t.Parallel()
	users := newAuthUserRepo()
	users.put(&domain.User{ID: "u1", Login: "alice", Role: domain.UserRoleViewer, Active: true})
	sessions := newMemSessionRepo()
	uc, _ := newUserUC(users, sessions)

	upd := &domain.User{ID: "u1", Login: "alice", Email: "new@x.com",
		Role: domain.UserRoleViewer, Active: true}
	require.NoError(t, uc.Update(context.Background(), SystemActor(), upd))

	assert.Empty(t, sessions.deletedByUser, "session purge must skip when role/active unchanged")
}

// membershipTeamRepo — teams-фейк с фиксированным набором членств пользователя
// (§45, валидация SetDefaultTeam).
type membershipTeamRepo struct {
	nopTeamRepo
	memberships []*domain.UserTeam
}

func (r *membershipTeamRepo) ListUserTeams(context.Context, string) ([]*domain.UserTeam, error) {
	return r.memberships, nil
}

func newUserUCWithTeams(users *authUserRepo, teams port.TeamRepo) (*UserUsecase, *stubAuditRepo) {
	repo := &stubAuditRepo{}
	audit := NewAuditUsecase(repo, logging.NewNoop())
	uc := NewUserUsecase(users, newMemSessionRepo(), teams, audit,
		"00000000-0000-0000-0000-000000000000", logging.NewNoop())
	return uc, repo
}

// §45: назначение дефолтной команды из членств — успех + audit + запись колонки.
func TestUserUC_SetDefaultTeam_Member_OK(t *testing.T) {
	t.Parallel()
	users := newAuthUserRepo()
	users.put(&domain.User{ID: "u1", Login: "alice", DefaultTeamID: "team-old"})
	teams := &membershipTeamRepo{memberships: []*domain.UserTeam{
		{Team: domain.Team{ID: "team-new"}, Role: domain.TeamRoleMember},
	}}
	uc, audit := newUserUCWithTeams(users, teams)

	err := uc.SetDefaultTeam(context.Background(), SystemActor(), "u1", "team-new")
	require.NoError(t, err)
	got, _ := users.Get(context.Background(), "u1")
	assert.Equal(t, "team-new", got.DefaultTeamID, "default_team_id должен обновиться")
	require.Len(t, audit.entries, 1)
	assert.Equal(t, domain.ActionUserUpdate, audit.entries[0].Action)
	assert.Equal(t, "team-new", audit.entries[0].Details["default_team_id"])
}

// §45: нельзя назначить дефолтной команду, в которой пользователь не состоит.
func TestUserUC_SetDefaultTeam_NotMember_Rejected(t *testing.T) {
	t.Parallel()
	users := newAuthUserRepo()
	users.put(&domain.User{ID: "u1", Login: "alice", DefaultTeamID: "team-old"})
	teams := &membershipTeamRepo{memberships: []*domain.UserTeam{
		{Team: domain.Team{ID: "team-a"}, Role: domain.TeamRoleMember},
	}}
	uc, audit := newUserUCWithTeams(users, teams)

	err := uc.SetDefaultTeam(context.Background(), SystemActor(), "u1", "team-foreign")
	require.ErrorIs(t, err, domain.ErrUserNotTeamMember)
	got, _ := users.Get(context.Background(), "u1")
	assert.Equal(t, "team-old", got.DefaultTeamID, "при не-членстве колонка не меняется")
	assert.Empty(t, audit.entries, "при отказе audit не пишется")
}

// §45: несуществующий пользователь → ErrUserNotFound (до проверки членства).
func TestUserUC_SetDefaultTeam_UserNotFound(t *testing.T) {
	t.Parallel()
	uc, _ := newUserUCWithTeams(newAuthUserRepo(), &membershipTeamRepo{})
	err := uc.SetDefaultTeam(context.Background(), SystemActor(), "ghost", "team-x")
	require.ErrorIs(t, err, domain.ErrUserNotFound)
}

func TestUserUC_Delete_SelfRejected(t *testing.T) {
	t.Parallel()
	uc, _ := newUserUC(newAuthUserRepo(), newMemSessionRepo())

	err := uc.Delete(context.Background(), Actor{UserID: "u1"}, "u1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot delete yourself")
}

func TestUserUC_Delete_LastAdmin_Rejected(t *testing.T) {
	t.Parallel()
	users := newAuthUserRepo()
	users.put(&domain.User{ID: "u1", Login: "admin", Role: domain.UserRoleAdmin, Active: true})
	users.activeAdmins = 1
	uc, _ := newUserUC(users, newMemSessionRepo())

	err := uc.Delete(context.Background(), SystemActor(), "u1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "last active admin")
	assert.Equal(t, 0, users.deleteCalls)
}

func TestUserUC_Delete_Happy_PurgesSessions(t *testing.T) {
	t.Parallel()
	users := newAuthUserRepo()
	users.put(&domain.User{ID: "u2", Login: "bob", Role: domain.UserRoleViewer, Active: true})
	sessions := newMemSessionRepo()
	sessions.byToken["tok"] = &domain.Session{Token: "tok", UserID: "u2"}

	uc, audit := newUserUC(users, sessions)
	require.NoError(t, uc.Delete(context.Background(), Actor{UserID: "u1"}, "u2"))

	assert.Equal(t, 1, users.deleteCalls)
	assert.Contains(t, sessions.deletedByUser, "u2")
	require.Len(t, audit.entries, 1)
	assert.Equal(t, domain.ActionUserDelete, audit.entries[0].Action)
	assert.Equal(t, "bob", audit.entries[0].Details["login"])
}

func TestUserUC_Delete_NotFound(t *testing.T) {
	t.Parallel()
	users := newAuthUserRepo()
	uc, _ := newUserUC(users, newMemSessionRepo())

	err := uc.Delete(context.Background(), SystemActor(), "ghost")
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrUserNotFound)
}
