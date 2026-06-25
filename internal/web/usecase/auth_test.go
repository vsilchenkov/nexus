package usecase

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// authUserRepo — гибкий mock UserRepo для auth/user тестов.
type authUserRepo struct {
	mu              sync.Mutex
	byID            map[string]*domain.User
	byLogin         map[string]*domain.User
	activeAdmins    int
	getErr          error
	updateLoginErr  error
	updatePassErr   error
	updateErr       error
	createErr       error
	deleteErr       error
	updatePassCalls []updatePassCall
	updateCalls     int
	createCalls     int
	deleteCalls     int
	lastListFilter  port.ListUsersFilter
}

type updatePassCall struct {
	UserID     string
	MustChange bool
}

func newAuthUserRepo() *authUserRepo {
	return &authUserRepo{
		byID:    make(map[string]*domain.User),
		byLogin: make(map[string]*domain.User),
	}
}

func (r *authUserRepo) put(u *domain.User) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID[u.ID] = u
	if u.Login != "" {
		r.byLogin[u.Login] = u
	}
}

func (r *authUserRepo) Get(_ context.Context, id string) (*domain.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.getErr != nil {
		return nil, r.getErr
	}
	if u, ok := r.byID[id]; ok {
		return u, nil
	}
	return nil, domain.ErrUserNotFound
}

func (r *authUserRepo) GetByLogin(_ context.Context, login string) (*domain.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.getErr != nil {
		return nil, r.getErr
	}
	if u, ok := r.byLogin[login]; ok {
		return u, nil
	}
	return nil, domain.ErrUserNotFound
}

func (r *authUserRepo) List(_ context.Context, f port.ListUsersFilter) ([]*domain.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastListFilter = f
	out := make([]*domain.User, 0, len(r.byID))
	for _, u := range r.byID {
		out = append(out, u)
	}
	return out, nil
}

func (r *authUserRepo) CountActiveAdmins(_ context.Context) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.activeAdmins, nil
}

func (r *authUserRepo) Create(_ context.Context, u *domain.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.createCalls++
	if r.createErr != nil {
		return r.createErr
	}
	r.byID[u.ID] = u
	r.byLogin[u.Login] = u
	return nil
}

func (r *authUserRepo) Update(_ context.Context, u *domain.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updateCalls++
	if r.updateErr != nil {
		return r.updateErr
	}
	r.byID[u.ID] = u
	return nil
}

func (r *authUserRepo) UpdatePassword(_ context.Context, id, hash string, mustChange bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updatePassCalls = append(r.updatePassCalls, updatePassCall{UserID: id, MustChange: mustChange})
	if r.updatePassErr != nil {
		return r.updatePassErr
	}
	if u, ok := r.byID[id]; ok {
		u.PasswordHash = hash
		u.MustChangePassword = mustChange
	}
	return nil
}

func (r *authUserRepo) UpdateLastLogin(_ context.Context, id string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.updateLoginErr != nil {
		return r.updateLoginErr
	}
	if u, ok := r.byID[id]; ok {
		u.LastLoginAt = &at
	}
	return nil
}

func (r *authUserRepo) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deleteCalls++
	if r.deleteErr != nil {
		return r.deleteErr
	}
	if u, ok := r.byID[id]; ok {
		delete(r.byLogin, u.Login)
		delete(r.byID, id)
	}
	return nil
}

// memSessionRepo — in-memory session store.
type memSessionRepo struct {
	mu            sync.Mutex
	byToken       map[string]*domain.Session
	deleteByUser  map[string]int
	getErr        error
	createErr     error
	deleteCalls   int
	touchCalls    int
	deletedByUser []string
	// §34.2: последний TTL, переданный в Create/Touch — для проверки
	// динамической длительности сессии.
	lastCreateTTL time.Duration
	lastTouchTTL  time.Duration
}

func newMemSessionRepo() *memSessionRepo {
	return &memSessionRepo{
		byToken:      make(map[string]*domain.Session),
		deleteByUser: make(map[string]int),
	}
}

func (r *memSessionRepo) Create(_ context.Context, s *domain.Session, ttl time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.createErr != nil {
		return r.createErr
	}
	r.lastCreateTTL = ttl
	r.byToken[s.Token] = s
	return nil
}
func (r *memSessionRepo) Get(_ context.Context, token string) (*domain.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.getErr != nil {
		return nil, r.getErr
	}
	if s, ok := r.byToken[token]; ok {
		return s, nil
	}
	return nil, domain.ErrSessionNotFound
}
func (r *memSessionRepo) Touch(_ context.Context, s *domain.Session, ttl time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.touchCalls++
	r.lastTouchTTL = ttl
	if s != nil {
		r.byToken[s.Token] = s
	}
	return nil
}
func (r *memSessionRepo) Delete(_ context.Context, token string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deleteCalls++
	delete(r.byToken, token)
	return nil
}
func (r *memSessionRepo) DeleteByUser(_ context.Context, userID string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deletedByUser = append(r.deletedByUser, userID)
	deleted := 0
	for k, s := range r.byToken {
		if s.UserID == userID {
			delete(r.byToken, k)
			deleted++
		}
	}
	r.deleteByUser[userID] = deleted
	return deleted, nil
}

func hash(t *testing.T, pw string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
	require.NoError(t, err)
	return string(h)
}

func newAuthUC(users *authUserRepo, sessions port.SessionRepo) (*AuthUsecase, *stubAuditRepo) {
	repo := &stubAuditRepo{}
	audit := NewAuditUsecase(repo, logging.NewNoop())
	return NewAuthUsecase(users, sessions, &nopTeamRepo{}, audit, func() time.Duration { return time.Hour }, logging.NewNoop()), repo
}

// nopTeamRepo — заглушка port.TeamRepo для unit-тестов AuthUsecase.
// Возвращает пустые/zero значения; нужна, потому что AuthUsecase теперь
// требует TeamRepo в конструкторе (multi-tenancy v2, Phase 10.B).
type nopTeamRepo struct{}

func (nopTeamRepo) GetByID(context.Context, string) (*domain.Team, error) {
	return nil, domain.ErrTeamNotFound
}
func (nopTeamRepo) GetBySlug(context.Context, string) (*domain.Team, error) {
	return nil, domain.ErrTeamNotFound
}
func (nopTeamRepo) List(context.Context) ([]*domain.Team, error) { return nil, nil }
func (nopTeamRepo) Create(context.Context, *domain.Team) error   { return nil }
func (nopTeamRepo) Update(context.Context, *domain.Team) error   { return nil }
func (nopTeamRepo) Delete(context.Context, string) error         { return nil }
func (nopTeamRepo) AddMember(context.Context, string, string, domain.TeamRole) error {
	return nil
}
func (nopTeamRepo) RemoveMember(context.Context, string, string) error { return nil }
func (nopTeamRepo) UpdateMemberRole(context.Context, string, string, domain.TeamRole) error {
	return nil
}
func (nopTeamRepo) ListMembers(context.Context, string) ([]*domain.TeamMember, error) {
	return nil, nil
}
func (nopTeamRepo) ListUserTeams(context.Context, string) ([]*domain.UserTeam, error) {
	return nil, nil
}

// stubTeamRepo — TeamRepo с настраиваемым ListUserTeams (§43.H): остальные
// методы от nopTeamRepo, ListUserTeams возвращает заданные членства/ошибку.
type stubTeamRepo struct {
	nopTeamRepo
	teams   []*domain.UserTeam
	listErr error
}

func (r *stubTeamRepo) ListUserTeams(context.Context, string) ([]*domain.UserTeam, error) {
	return r.teams, r.listErr
}

func userTeam(id, slug string) *domain.UserTeam {
	return &domain.UserTeam{Team: domain.Team{ID: id, Slug: slug}, Role: domain.TeamRoleMember}
}

// TestAuthUC_Login_ResolvesTeam (§43.H): current_team новой сессии резолвится
// по членству, а не слепо из default_team_id.
func TestAuthUC_Login_ResolvesTeam(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		defaultTeam string
		memberships []*domain.UserTeam
		wantTeam    string
	}{
		{
			name:        "default team среди членств → он",
			defaultTeam: "tA",
			memberships: []*domain.UserTeam{userTeam("tA", "a"), userTeam("tB", "b")},
			wantTeam:    "tA",
		},
		{
			name:        "default team не член → первое членство (баг p.nikonov)",
			defaultTeam: "tDefault",
			memberships: []*domain.UserTeam{userTeam("tVika", "vika")},
			wantTeam:    "tVika",
		},
		{
			name:        "нет членств → оставляем default team",
			defaultTeam: "tDefault",
			memberships: nil,
			wantTeam:    "tDefault",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			users := newAuthUserRepo()
			users.put(&domain.User{
				ID: "u1", Login: "alice", Active: true,
				PasswordHash:  hash(t, "secret123"),
				DefaultTeamID: tt.defaultTeam,
			})
			sessions := newMemSessionRepo()
			audit := NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop())
			uc := NewAuthUsecase(users, sessions, &stubTeamRepo{teams: tt.memberships}, audit,
				func() time.Duration { return time.Hour }, logging.NewNoop())

			tok, _, err := uc.Login(context.Background(), "alice", "secret123", "1.2.3.4")
			require.NoError(t, err)
			s, err := sessions.Get(context.Background(), tok)
			require.NoError(t, err)
			assert.Equal(t, tt.wantTeam, s.CurrentTeamID)
		})
	}
}

// TestAuthUC_Login_TeamListError_FallsBack (§43.H): ошибка чтения членств не
// блокирует логин — current_team деградирует на default_team_id.
func TestAuthUC_Login_TeamListError_FallsBack(t *testing.T) {
	t.Parallel()
	users := newAuthUserRepo()
	users.put(&domain.User{ID: "u1", Login: "alice", Active: true,
		PasswordHash: hash(t, "secret123"), DefaultTeamID: "tDefault"})
	sessions := newMemSessionRepo()
	audit := NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop())
	uc := NewAuthUsecase(users, sessions, &stubTeamRepo{listErr: errors.New("db down")}, audit,
		func() time.Duration { return time.Hour }, logging.NewNoop())

	tok, _, err := uc.Login(context.Background(), "alice", "secret123", "")
	require.NoError(t, err)
	s, err := sessions.Get(context.Background(), tok)
	require.NoError(t, err)
	assert.Equal(t, "tDefault", s.CurrentTeamID)
}

// TestAuthUC_MyTeamsAndCurrent (§43.H): самолечение current_team в сессии.
func TestAuthUC_MyTeamsAndCurrent(t *testing.T) {
	t.Parallel()

	t.Run("current не член → переключение + persist", func(t *testing.T) {
		t.Parallel()
		sessions := newMemSessionRepo()
		s := &domain.Session{Token: "tok", UserID: "u1", CurrentTeamID: "tStale"}
		sessions.byToken["tok"] = s
		teams := &stubTeamRepo{teams: []*domain.UserTeam{userTeam("tVika", "vika")}}
		uc := NewAuthUsecase(newAuthUserRepo(), sessions, teams,
			NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()),
			func() time.Duration { return time.Hour }, logging.NewNoop())

		ms, current, healed, err := uc.MyTeamsAndCurrent(context.Background(), s)
		require.NoError(t, err)
		require.Len(t, ms, 1)
		assert.True(t, healed)
		assert.Equal(t, "tVika", current)
		assert.Equal(t, "tVika", s.CurrentTeamID)
		stored, err := sessions.Get(context.Background(), "tok")
		require.NoError(t, err)
		assert.Equal(t, "tVika", stored.CurrentTeamID)
	})

	t.Run("current член → без изменений", func(t *testing.T) {
		t.Parallel()
		sessions := newMemSessionRepo()
		s := &domain.Session{Token: "tok", UserID: "u1", CurrentTeamID: "tVika"}
		sessions.byToken["tok"] = s
		teams := &stubTeamRepo{teams: []*domain.UserTeam{userTeam("tVika", "vika"), userTeam("tB", "b")}}
		uc := NewAuthUsecase(newAuthUserRepo(), sessions, teams,
			NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()),
			func() time.Duration { return time.Hour }, logging.NewNoop())

		_, current, healed, err := uc.MyTeamsAndCurrent(context.Background(), s)
		require.NoError(t, err)
		assert.False(t, healed)
		assert.Equal(t, "tVika", current)
	})

	t.Run("API-токен (пустой token) → не лечим", func(t *testing.T) {
		t.Parallel()
		sessions := newMemSessionRepo()
		s := &domain.Session{Token: "", UserID: "u1", CurrentTeamID: "tFixed"}
		teams := &stubTeamRepo{teams: []*domain.UserTeam{userTeam("tVika", "vika")}}
		uc := NewAuthUsecase(newAuthUserRepo(), sessions, teams,
			NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()),
			func() time.Duration { return time.Hour }, logging.NewNoop())

		_, current, healed, err := uc.MyTeamsAndCurrent(context.Background(), s)
		require.NoError(t, err)
		assert.False(t, healed)
		assert.Equal(t, "tFixed", current)
	})
}

func TestAuthUC_Login_UnknownUser(t *testing.T) {
	t.Parallel()
	users := newAuthUserRepo()
	uc, audit := newAuthUC(users, newMemSessionRepo())

	tok, u, err := uc.Login(context.Background(), "ghost", "x", "1.2.3.4")
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
	assert.Empty(t, tok)
	assert.Nil(t, u)
	require.Len(t, audit.entries, 1)
	assert.Equal(t, domain.ActionUserLoginFailed, audit.entries[0].Action)
	assert.Equal(t, "not_found", audit.entries[0].Details["reason"])
}

func TestAuthUC_Login_InactiveUser(t *testing.T) {
	t.Parallel()
	users := newAuthUserRepo()
	users.put(&domain.User{ID: "u1", Login: "alice", Active: false, PasswordHash: hash(t, "secret123")})
	uc, audit := newAuthUC(users, newMemSessionRepo())

	tok, _, err := uc.Login(context.Background(), "alice", "secret123", "")
	assert.ErrorIs(t, err, domain.ErrUserInactive)
	assert.Empty(t, tok)
	require.Len(t, audit.entries, 1)
	assert.Equal(t, "inactive", audit.entries[0].Details["reason"])
}

func TestAuthUC_Login_NoPasswordSet(t *testing.T) {
	t.Parallel()
	users := newAuthUserRepo()
	users.put(&domain.User{ID: "u1", Login: "admin", Active: true, PasswordHash: ""})
	uc, _ := newAuthUC(users, newMemSessionRepo())

	tok, _, err := uc.Login(context.Background(), "admin", "anything", "")
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
	assert.Empty(t, tok)
}

func TestAuthUC_Login_BadPassword(t *testing.T) {
	t.Parallel()
	users := newAuthUserRepo()
	users.put(&domain.User{ID: "u1", Login: "alice", Active: true, PasswordHash: hash(t, "rightpw1")})
	uc, audit := newAuthUC(users, newMemSessionRepo())

	tok, _, err := uc.Login(context.Background(), "alice", "wrongpw", "")
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
	assert.Empty(t, tok)
	require.Len(t, audit.entries, 1)
	assert.Equal(t, "bad_password", audit.entries[0].Details["reason"])
}

func TestAuthUC_Login_HappyPath(t *testing.T) {
	t.Parallel()
	users := newAuthUserRepo()
	users.put(&domain.User{
		ID: "u1", Login: "alice", Active: true,
		PasswordHash: hash(t, "secret123"),
		Role:         domain.UserRoleAdmin,
		Lang:         domain.UserLangRU,
	})
	sessions := newMemSessionRepo()
	uc, audit := newAuthUC(users, sessions)

	tok, u, err := uc.Login(context.Background(), "alice", "secret123", "10.0.0.1")
	require.NoError(t, err)
	assert.NotEmpty(t, tok)
	require.NotNil(t, u)
	assert.Equal(t, "u1", u.ID)

	// Сессия сохранена с тем же токеном/userID/role/lang.
	s, err := sessions.Get(context.Background(), tok)
	require.NoError(t, err)
	assert.Equal(t, "u1", s.UserID)
	assert.Equal(t, domain.UserRoleAdmin, s.Role)
	assert.Equal(t, domain.UserLangRU, s.Lang)

	// last_login обновлён.
	assert.NotNil(t, users.byID["u1"].LastLoginAt)

	require.Len(t, audit.entries, 1)
	assert.Equal(t, domain.ActionUserLogin, audit.entries[0].Action)
	assert.Equal(t, "10.0.0.1", audit.entries[0].IPAddress)
}

// TestAuthUC_DynamicSessionTTL (§34.2): Login и sliding-Touch берут актуальное
// значение TTL из провайдера; смена провайдера между логином и Check меняет
// TTL без рестарта.
func TestAuthUC_DynamicSessionTTL(t *testing.T) {
	t.Parallel()
	users := newAuthUserRepo()
	users.put(&domain.User{ID: "u1", Login: "alice", Active: true, PasswordHash: hash(t, "secret123")})
	sessions := newMemSessionRepo()
	audit := NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop())

	ttl := 2 * time.Hour
	uc := NewAuthUsecase(users, sessions, &nopTeamRepo{}, audit,
		func() time.Duration { return ttl }, logging.NewNoop())

	tok, _, err := uc.Login(context.Background(), "alice", "secret123", "10.0.0.1")
	require.NoError(t, err)
	assert.Equal(t, 2*time.Hour, sessions.lastCreateTTL, "Login must use current provider TTL")

	// Меняем TTL «в рантайме» (как сделал бы hot-reload) — следующий Touch берёт новое значение.
	ttl = 30 * time.Minute
	_, err = uc.Check(context.Background(), tok)
	require.NoError(t, err)
	assert.Equal(t, 30*time.Minute, sessions.lastTouchTTL, "Touch must use updated TTL without restart")
}

// stubLoginLimiter — управляемый RateLimiter для тестов анти-брутфорса.
type stubLoginLimiter struct {
	denyKeys map[string]bool
	err      error
}

func (s *stubLoginLimiter) Allow(_ context.Context, key string, _ int) (bool, error) {
	if s.err != nil {
		return true, s.err
	}
	return !s.denyKeys[key], nil
}

func TestAuthUC_Login_RateLimited(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		deny string
	}{
		{"лимит по IP", "login:ip:10.0.0.1"},
		{"лимит по login (distributed brute-force)", "login:user:alice"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			users := newAuthUserRepo()
			users.put(&domain.User{
				ID: "u1", Login: "alice", Active: true,
				PasswordHash: hash(t, "secret123"),
			})
			uc, audit := newAuthUC(users, newMemSessionRepo())
			uc.WithLoginRateLimit(&stubLoginLimiter{denyKeys: map[string]bool{tt.deny: true}}, 10)

			_, _, err := uc.Login(context.Background(), "alice", "secret123", "10.0.0.1")
			require.ErrorIs(t, err, ErrLoginRateLimited)

			require.NotEmpty(t, audit.entries)
			last := audit.entries[len(audit.entries)-1]
			assert.Equal(t, domain.ActionUserLoginFailed, last.Action)
		})
	}
}

func TestAuthUC_Login_RateLimiterError_FailOpen(t *testing.T) {
	t.Parallel()
	users := newAuthUserRepo()
	users.put(&domain.User{
		ID: "u1", Login: "alice", Active: true,
		PasswordHash: hash(t, "secret123"),
	})
	uc, _ := newAuthUC(users, newMemSessionRepo())
	uc.WithLoginRateLimit(&stubLoginLimiter{err: errors.New("redis down")}, 10)

	// §9.4: сбой Redis не должен блокировать вход.
	tok, _, err := uc.Login(context.Background(), "alice", "secret123", "10.0.0.1")
	require.NoError(t, err)
	assert.NotEmpty(t, tok)
}

func TestAuthUC_Login_GetError(t *testing.T) {
	t.Parallel()
	users := newAuthUserRepo()
	users.getErr = errors.New("db down")
	uc, _ := newAuthUC(users, newMemSessionRepo())

	_, _, err := uc.Login(context.Background(), "alice", "x", "")
	require.Error(t, err)
	assert.NotErrorIs(t, err, domain.ErrUnauthorized)
	assert.True(t, strings.Contains(err.Error(), "get user"))
}

func TestAuthUC_Login_SessionCreateError(t *testing.T) {
	t.Parallel()
	users := newAuthUserRepo()
	users.put(&domain.User{ID: "u1", Login: "alice", Active: true, PasswordHash: hash(t, "secret123")})
	sessions := newMemSessionRepo()
	sessions.createErr = errors.New("redis down")
	uc, _ := newAuthUC(users, sessions)

	_, _, err := uc.Login(context.Background(), "alice", "secret123", "")
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "create session"))
}

func TestAuthUC_Logout(t *testing.T) {
	t.Parallel()
	sessions := newMemSessionRepo()
	sessions.byToken["tok"] = &domain.Session{Token: "tok"}
	uc, _ := newAuthUC(newAuthUserRepo(), sessions)

	require.NoError(t, uc.Logout(context.Background(), "tok"))
	_, ok := sessions.byToken["tok"]
	assert.False(t, ok)
}

func TestAuthUC_Check_TouchesAndReturns(t *testing.T) {
	t.Parallel()
	sessions := newMemSessionRepo()
	want := &domain.Session{Token: "tok", UserID: "u1"}
	sessions.byToken["tok"] = want
	uc, _ := newAuthUC(newAuthUserRepo(), sessions)

	got, err := uc.Check(context.Background(), "tok")
	require.NoError(t, err)
	assert.Same(t, want, got)
	assert.Equal(t, 1, sessions.touchCalls)
}

func TestAuthUC_Check_NotFound(t *testing.T) {
	t.Parallel()
	sessions := newMemSessionRepo()
	uc, _ := newAuthUC(newAuthUserRepo(), sessions)

	_, err := uc.Check(context.Background(), "unknown")
	assert.ErrorIs(t, err, domain.ErrSessionNotFound)
}

func TestAuthUC_Me(t *testing.T) {
	t.Parallel()
	users := newAuthUserRepo()
	want := &domain.User{ID: "u1", Login: "alice", Email: "a@x.com"}
	users.put(want)
	uc, _ := newAuthUC(users, newMemSessionRepo())

	got, err := uc.Me(context.Background(), "u1")
	require.NoError(t, err)
	assert.Same(t, want, got)
}

func TestAuthUC_ChangePassword_TooShort(t *testing.T) {
	t.Parallel()
	uc, _ := newAuthUC(newAuthUserRepo(), newMemSessionRepo())

	err := uc.ChangePassword(context.Background(), SystemActor(), "u1", "short", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least 8")
}

func TestAuthUC_ChangePassword_Happy_PurgesSessions(t *testing.T) {
	t.Parallel()
	users := newAuthUserRepo()
	users.put(&domain.User{ID: "u1", Login: "alice"})

	sessions := newMemSessionRepo()
	sessions.byToken["tok-1"] = &domain.Session{Token: "tok-1", UserID: "u1"}
	sessions.byToken["tok-2"] = &domain.Session{Token: "tok-2", UserID: "u1"}
	sessions.byToken["other"] = &domain.Session{Token: "other", UserID: "u2"}

	uc, audit := newAuthUC(users, sessions)
	require.NoError(t, uc.ChangePassword(context.Background(), SystemActor(), "u1", "longenough", true))

	// Сессии user'а u1 удалены, чужого — нет.
	assert.NotContains(t, sessions.byToken, "tok-1")
	assert.NotContains(t, sessions.byToken, "tok-2")
	assert.Contains(t, sessions.byToken, "other")

	// MustChangePassword прокинут.
	require.Len(t, users.updatePassCalls, 1)
	assert.True(t, users.updatePassCalls[0].MustChange)
	assert.Equal(t, "u1", users.updatePassCalls[0].UserID)

	// Запись в audit.
	require.Len(t, audit.entries, 1)
	assert.Equal(t, domain.ActionUserPassword, audit.entries[0].Action)
}

func TestAuthUC_ChangePassword_UpdateError(t *testing.T) {
	t.Parallel()
	users := newAuthUserRepo()
	users.updatePassErr = errors.New("db down")
	uc, _ := newAuthUC(users, newMemSessionRepo())

	err := uc.ChangePassword(context.Background(), SystemActor(), "u1", "longenough", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "db down")
}

// userWithPassword — хелпер: пользователь с bcrypt-хешем заданного пароля.
func userWithPassword(t *testing.T, id, login, password string, role domain.UserRole) *domain.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)
	return &domain.User{ID: id, Login: login, Role: role, Active: true, PasswordHash: string(hash)}
}

func TestAuthUC_ChangeOwnPassword_Happy(t *testing.T) {
	t.Parallel()
	users := newAuthUserRepo()
	users.put(userWithPassword(t, "u1", "bob", "oldpassword", domain.UserRoleManager))

	sessions := newMemSessionRepo()
	sessions.byToken["tok-1"] = &domain.Session{Token: "tok-1", UserID: "u1"}
	uc, audit := newAuthUC(users, sessions)

	err := uc.ChangeOwnPassword(context.Background(), SystemActor(), "u1", "oldpassword", "newpassword")
	require.NoError(t, err)

	// Пароль обновлён с mustChange=false; сессии инвалидированы; есть audit.
	require.Len(t, users.updatePassCalls, 1)
	assert.False(t, users.updatePassCalls[0].MustChange)
	assert.NotContains(t, sessions.byToken, "tok-1")
	require.Len(t, audit.entries, 1)
	assert.Equal(t, domain.ActionUserPassword, audit.entries[0].Action)
}

func TestAuthUC_ChangeOwnPassword_WrongCurrent(t *testing.T) {
	t.Parallel()
	users := newAuthUserRepo()
	users.put(userWithPassword(t, "u1", "bob", "oldpassword", domain.UserRoleManager))
	uc, _ := newAuthUC(users, newMemSessionRepo())

	err := uc.ChangeOwnPassword(context.Background(), SystemActor(), "u1", "wrongpassword", "newpassword")
	require.ErrorIs(t, err, domain.ErrUnauthorized)
	// Пароль не менялся.
	assert.Empty(t, users.updatePassCalls)
}

func TestAuthUC_ChangeOwnPassword_NewTooShort(t *testing.T) {
	t.Parallel()
	users := newAuthUserRepo()
	users.put(userWithPassword(t, "u1", "bob", "oldpassword", domain.UserRoleManager))
	uc, _ := newAuthUC(users, newMemSessionRepo())

	err := uc.ChangeOwnPassword(context.Background(), SystemActor(), "u1", "oldpassword", "short")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least 8")
}
