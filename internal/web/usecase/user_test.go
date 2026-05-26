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
	return NewUserUsecase(users, sessions, audit, logging.NewNoop()), repo
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

func TestUserUC_Create_InvalidRole(t *testing.T) {
	t.Parallel()
	uc, _ := newUserUC(newAuthUserRepo(), newMemSessionRepo())

	err := uc.Create(context.Background(), SystemActor(),
		&domain.User{ID: "u1", Login: "alice", Role: "editor"}, "longenough")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid role")
}

func TestUserUC_Create_PasswordTooShort(t *testing.T) {
	t.Parallel()
	uc, _ := newUserUC(newAuthUserRepo(), newMemSessionRepo())

	err := uc.Create(context.Background(), SystemActor(),
		&domain.User{ID: "u1", Login: "alice", Role: domain.UserRoleViewer}, "short")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least 8")
}

func TestUserUC_Create_DefaultsLang(t *testing.T) {
	t.Parallel()
	users := newAuthUserRepo()
	uc, _ := newUserUC(users, newMemSessionRepo())

	u := &domain.User{ID: "u1", Login: "alice", Role: domain.UserRoleAdmin, Lang: ""}
	require.NoError(t, uc.Create(context.Background(), SystemActor(), u, ""))
	assert.Equal(t, domain.UserLangEN, u.Lang)
}

func TestUserUC_Create_Happy(t *testing.T) {
	t.Parallel()
	users := newAuthUserRepo()
	uc, audit := newUserUC(users, newMemSessionRepo())

	u := &domain.User{ID: "u1", Login: "alice", Role: domain.UserRoleAdmin, Lang: domain.UserLangRU}
	require.NoError(t, uc.Create(context.Background(), SystemActor(), u, "secret123"))

	assert.NotEmpty(t, u.PasswordHash, "password must be hashed")
	assert.NotEqual(t, "secret123", u.PasswordHash, "plaintext must not be stored")
	assert.Equal(t, 1, users.createCalls)

	require.Len(t, audit.entries, 1)
	assert.Equal(t, domain.ActionUserCreate, audit.entries[0].Action)
	assert.Equal(t, "alice", audit.entries[0].Details["login"])
	assert.Equal(t, string(domain.UserRoleAdmin), audit.entries[0].Details["role"])
}

func TestUserUC_Create_RepoError(t *testing.T) {
	t.Parallel()
	users := newAuthUserRepo()
	users.createErr = errors.New("db down")
	uc, audit := newUserUC(users, newMemSessionRepo())

	err := uc.Create(context.Background(), SystemActor(),
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
