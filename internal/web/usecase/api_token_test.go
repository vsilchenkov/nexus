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

	"nexus/internal/domain"
	"nexus/internal/platform/clock"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// inMemAPITokenRepo — in-memory реализация port.APITokenRepo.
// Хранит токены и фиксирует вызов TouchLastUsed (Verify дёргает его
// в фоновой горутине).
type inMemAPITokenRepo struct {
	mu       sync.Mutex
	byHash   map[string]*domain.APIToken
	getErr   error
	touched  []string
	createOK bool
}

func newInMemAPITokenRepo() *inMemAPITokenRepo {
	return &inMemAPITokenRepo{byHash: map[string]*domain.APIToken{}, createOK: true}
}

func (r *inMemAPITokenRepo) GetByHash(_ context.Context, hash string) (*domain.APIToken, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.getErr != nil {
		return nil, r.getErr
	}
	if t, ok := r.byHash[hash]; ok {
		return t, nil
	}
	return nil, domain.ErrNotFound
}

// ListByUser — эмуляция `WHERE user_id = $1` (без team-фильтра): список токенов
// не скоупится командой, поэтому и стаб не фильтрует.
func (r *inMemAPITokenRepo) ListByUser(_ context.Context, userID string) ([]*domain.APIToken, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*domain.APIToken
	for _, t := range r.byHash {
		if t.UserID == userID {
			out = append(out, t)
		}
	}
	return out, nil
}
func (r *inMemAPITokenRepo) Create(_ context.Context, t *domain.APIToken) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.createOK {
		return errors.New("create failed")
	}
	// эмулируем БД: записываем ID, чтобы CreatedToken.Token.ID был не пустым.
	if t.ID == "" {
		t.ID = "tok-" + t.Prefix
	}
	r.byHash[t.TokenHash] = t
	return nil
}
func (r *inMemAPITokenRepo) Revoke(_ context.Context, _, _ string) error { return nil }

// Rotate — эмуляция `UPDATE ... WHERE id AND user_id AND активен`: находит токен
// владельца, только активный (не отозван/не просрочен), перевыпускает hash/prefix
// (перекладывает под новый ключ), сбрасывает last_used_at.
func (r *inMemAPITokenRepo) Rotate(_ context.Context, id, userID, newHash, newPrefix string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for oldHash, t := range r.byHash {
		if t.ID != id || t.UserID != userID {
			continue
		}
		if t.RevokedAt != nil {
			return domain.ErrNotFound
		}
		if t.ExpiresAt != nil && t.ExpiresAt.Before(time.Now()) {
			return domain.ErrNotFound
		}
		delete(r.byHash, oldHash)
		t.TokenHash = newHash
		t.Prefix = newPrefix
		t.LastUsedAt = nil
		r.byHash[newHash] = t
		return nil
	}
	return domain.ErrNotFound
}
func (r *inMemAPITokenRepo) Delete(_ context.Context, _, _ string) error { return nil }
func (r *inMemAPITokenRepo) TouchLastUsed(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.touched = append(r.touched, id)
	return nil
}

// teamsStub — teamMembershipLister: членства пользователя. По умолчанию
// (nil-мапа) членств нет, поэтому тесты, где команда важна, задают их явно.
type teamsStub struct {
	byUser map[string][]string // userID → teamIDs
	err    error
}

func (s *teamsStub) ListUserTeams(_ context.Context, userID string) ([]*domain.UserTeam, error) {
	if s.err != nil {
		return nil, s.err
	}
	var out []*domain.UserTeam
	for _, id := range s.byUser[userID] {
		out = append(out, &domain.UserTeam{Team: domain.Team{ID: id}})
	}
	return out, nil
}

// memberOf — стаб членств для «пользователь состоит во всех перечисленных командах».
func memberOf(userID string, teamIDs ...string) *teamsStub {
	return &teamsStub{byUser: map[string][]string{userID: teamIDs}}
}

// userRepoStub — UserRepo, отвечает на Get конкретным набором пользователей.
type userRepoStub struct {
	byID map[string]*domain.User
	err  error
}

func (s *userRepoStub) Get(_ context.Context, id string) (*domain.User, error) {
	if s.err != nil {
		return nil, s.err
	}
	if u, ok := s.byID[id]; ok {
		return u, nil
	}
	return nil, domain.ErrNotFound
}
func (s *userRepoStub) GetByLogin(_ context.Context, _ string) (*domain.User, error) {
	return nil, domain.ErrNotFound
}
func (s *userRepoStub) GetByEmail(_ context.Context, _ string) (*domain.User, error) {
	return nil, domain.ErrNotFound
}
func (s *userRepoStub) List(_ context.Context, _ port.ListUsersFilter) ([]*domain.User, error) {
	return nil, nil
}
func (s *userRepoStub) CountActiveAdmins(_ context.Context) (int, error)       { return 1, nil }
func (s *userRepoStub) Create(_ context.Context, _ *domain.User) error         { return nil }
func (s *userRepoStub) Update(_ context.Context, _ *domain.User) error         { return nil }
func (s *userRepoStub) UpdateDefaultTeam(_ context.Context, _, _ string) error { return nil }
func (s *userRepoStub) UpdatePassword(_ context.Context, _, _ string, _ bool) error {
	return nil
}
func (s *userRepoStub) UpdateLastLogin(_ context.Context, _ string, _ time.Time) error {
	return nil
}
func (s *userRepoStub) Delete(_ context.Context, _ string) error { return nil }

func TestGenerateToken_ShapeAndUniqueness(t *testing.T) {
	t.Parallel()

	tok1, err := generateToken()
	require.NoError(t, err)
	tok2, err := generateToken()
	require.NoError(t, err)

	// Префикс db_ — §7.14, по нему middleware отличает API-token от Bearer.
	assert.True(t, strings.HasPrefix(tok1, APITokenPrefix), "want %s prefix, got %q", APITokenPrefix, tok1)
	assert.True(t, strings.HasPrefix(tok2, APITokenPrefix))

	// 32 байта raw → 43 символа base64-url без padding + длина префикса.
	assert.Equal(t, len(APITokenPrefix)+43, len(tok1))

	// Должна быть энтропия — два подряд токена не равны.
	assert.NotEqual(t, tok1, tok2)
}

func TestHashToken_StableAndHex(t *testing.T) {
	t.Parallel()

	const value = "db_some_token_value"
	h1 := hashToken(value)
	h2 := hashToken(value)

	assert.Equal(t, h1, h2, "hash должен быть детерминистическим")
	// SHA-256 hex = 64 символа [0-9a-f].
	assert.Len(t, h1, 64)
	for _, c := range h1 {
		ok := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
		assert.True(t, ok, "expected lowercase hex, got %q", c)
	}

	// Разные входные значения → разные хеши.
	assert.NotEqual(t, hashToken("a"), hashToken("b"))
}

func TestAPITokenUsecase_Create_HappyPath(t *testing.T) {
	t.Parallel()

	repo := newInMemAPITokenRepo()
	users := &userRepoStub{}
	auditRepo := &stubAuditRepo{}
	audit := NewAuditUsecase(auditRepo, logging.NewNoop())

	uc := NewAPITokenUsecase(repo, users, nil, audit, logging.NewNoop())

	created, err := uc.Create(context.Background(), Actor{UserLogin: "alice"},
		"u-1", "team-1", "prod-token", []string{domain.ScopeLogsRead}, nil)
	require.NoError(t, err)
	require.NotNil(t, created)

	// Plain отдан один раз, начинается с префикса (§7.14).
	assert.True(t, strings.HasPrefix(created.Plain, APITokenPrefix))

	// В БД лежит SHA-256(plain) — не сам plaintext.
	assert.Equal(t, hashToken(created.Plain), created.Token.TokenHash)
	assert.NotContains(t, created.Token.TokenHash, created.Plain,
		"в БД не должно быть plaintext-токена")

	// Prefix = первые 8 символов plaintext'а — для отображения в UI.
	assert.Equal(t, created.Plain[:8], created.Token.Prefix)

	// Scope записан.
	assert.Equal(t, []string{domain.ScopeLogsRead}, created.Token.Scopes)

	// Audit-запись c action=ActionAPITokenCreate.
	require.Len(t, auditRepo.entries, 1)
	assert.Equal(t, domain.ActionAPITokenCreate, auditRepo.entries[0].Action)
	assert.Equal(t, "api_token", auditRepo.entries[0].TargetType)
	assert.Equal(t, "prod-token", auditRepo.entries[0].Details["name"])
}

// Срок действия: раньше UI слал expires_in_days, а handler принимал expires_at —
// поле молча терялось и ВСЕ токены выходили бессрочными вопреки выбранному сроку.
func TestAPITokenUsecase_Create_ExpiresInDays(t *testing.T) {
	t.Parallel()

	newUC := func() *APITokenUsecase {
		return NewAPITokenUsecase(newInMemAPITokenRepo(), &userRepoStub{}, memberOf("u-1", "team-1"),
			NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()), logging.NewNoop())
	}

	t.Run("срок в днях считает сервер от своего времени", func(t *testing.T) {
		t.Parallel()
		days := 30
		before := time.Now()
		created, err := newUC().Create(context.Background(), Actor{}, "u-1", "team-1", "t",
			[]string{domain.ScopeLogsRead}, &days)
		require.NoError(t, err)
		require.NotNil(t, created.Token.ExpiresAt, "срок обязан быть проставлен")

		want := before.Add(30 * 24 * time.Hour)
		assert.WithinDuration(t, want, *created.Token.ExpiresAt, time.Minute)
		assert.True(t, created.Token.IsActive(time.Now()), "свежий токен активен")
		assert.False(t, created.Token.IsActive(want.Add(time.Hour)), "после срока — неактивен")
	})

	t.Run("nil = бессрочный", func(t *testing.T) {
		t.Parallel()
		created, err := newUC().Create(context.Background(), Actor{}, "u-1", "team-1", "t",
			[]string{domain.ScopeLogsRead}, nil)
		require.NoError(t, err)
		assert.Nil(t, created.Token.ExpiresAt)
	})

	t.Run("неположительный срок отклоняется", func(t *testing.T) {
		t.Parallel()
		for _, days := range []int{0, -1} {
			created, err := newUC().Create(context.Background(), Actor{}, "u-1", "team-1", "t",
				[]string{domain.ScopeLogsRead}, &days)
			require.Error(t, err, "days=%d", days)
			assert.Nil(t, created)
		}
	})
}

// «Настройки» вне скоупа команды: список отдаёт токены пользователя по ВСЕМ
// его командам (команду показывает колонка), но чужие токены — никогда.
func TestAPITokenUsecase_ListByUser_AllTeamsButOwnOnly(t *testing.T) {
	t.Parallel()

	uc := NewAPITokenUsecase(newInMemAPITokenRepo(), &userRepoStub{}, memberOf("u-1", "team-a", "team-b"),
		NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()), logging.NewNoop())
	ctx := context.Background()

	_, err := uc.Create(ctx, Actor{}, "u-1", "team-a", "in-a", []string{domain.ScopeLogsRead}, nil)
	require.NoError(t, err)
	_, err = uc.Create(ctx, Actor{}, "u-1", "team-b", "in-b", []string{domain.ScopeLogsRead}, nil)
	require.NoError(t, err)

	mine, err := uc.ListByUser(ctx, "u-1")
	require.NoError(t, err)
	require.Len(t, mine, 2, "видны токены обеих команд — список не скоупится")
	byName := map[string]string{}
	for _, tk := range mine {
		byName[tk.Name] = tk.TeamID
	}
	assert.Equal(t, map[string]string{"in-a": "team-a", "in-b": "team-b"}, byName,
		"team_id отдаётся наружу — UI показывает команду колонкой")

	alien, err := uc.ListByUser(ctx, "u-2")
	require.NoError(t, err)
	assert.Empty(t, alien, "чужие токены не видны")
}

// Токен создаётся в выбранной команде — инвариант §18.3, который раньше не
// проверял ни один assert.
func TestAPITokenUsecase_Create_BindsToTeam(t *testing.T) {
	t.Parallel()

	uc := NewAPITokenUsecase(newInMemAPITokenRepo(), &userRepoStub{}, memberOf("u-1", "team-vika"),
		NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()), logging.NewNoop())

	created, err := uc.Create(context.Background(), Actor{}, "u-1", "team-vika", "t",
		[]string{domain.ScopeLogsRead}, nil)
	require.NoError(t, err)
	assert.Equal(t, "team-vika", created.Token.TeamID)
}

// team_id приходит от клиента (селект в форме), поэтому членство обязано
// проверяться на сервере — иначе любой выпишет себе токен в чужую команду и
// обойдёт изоляцию §18.
func TestAPITokenUsecase_Create_ForeignTeam_Denied(t *testing.T) {
	t.Parallel()

	repo := newInMemAPITokenRepo()
	uc := NewAPITokenUsecase(repo, &userRepoStub{}, memberOf("u-1", "team-a"),
		NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()), logging.NewNoop())

	created, err := uc.Create(context.Background(), Actor{}, "u-1", "team-foreign", "t",
		[]string{domain.ScopeLogsRead}, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrPermissionDenied)
	assert.Nil(t, created)

	mine, err := uc.ListByUser(context.Background(), "u-1")
	require.NoError(t, err)
	assert.Empty(t, mine, "токен не должен быть создан")
}

func TestAPITokenUsecase_Create_RequiresName(t *testing.T) {
	t.Parallel()

	repo := newInMemAPITokenRepo()
	users := &userRepoStub{}
	audit := NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop())
	uc := NewAPITokenUsecase(repo, users, nil, audit, logging.NewNoop())

	created, err := uc.Create(context.Background(), Actor{}, "u-1", "team-1", "", nil, nil)
	require.Error(t, err)
	assert.Nil(t, created)
	assert.Contains(t, err.Error(), "name is required")
}

func TestAPITokenUsecase_Verify_HappyPath_TouchesLastUsed(t *testing.T) {
	t.Parallel()

	repo := newInMemAPITokenRepo()
	uid := "u-42"
	users := &userRepoStub{byID: map[string]*domain.User{
		uid: {ID: uid, Login: "bob", Active: true},
	}}
	audit := NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop())
	uc := NewAPITokenUsecase(repo, users, nil, audit, logging.NewNoop())

	created, err := uc.Create(context.Background(), Actor{}, uid, "team-1", "tok", []string{domain.ScopeLogsRead}, nil)
	require.NoError(t, err)

	gotTok, gotUser, err := uc.Verify(context.Background(), created.Plain)
	require.NoError(t, err)
	assert.Equal(t, created.Token.ID, gotTok.ID)
	assert.Equal(t, uid, gotUser.ID)
	assert.True(t, gotUser.Active)

	// TouchLastUsed работает асинхронно — даём ему добежать.
	assert.Eventually(t, func() bool {
		repo.mu.Lock()
		defer repo.mu.Unlock()
		return len(repo.touched) == 1 && repo.touched[0] == created.Token.ID
	}, 200*time.Millisecond, 10*time.Millisecond,
		"TouchLastUsed должен быть вызван в фоне для best-effort обновления last_used_at")
}

func TestAPITokenUsecase_Verify_BadPrefix(t *testing.T) {
	t.Parallel()

	uc := NewAPITokenUsecase(newInMemAPITokenRepo(), &userRepoStub{}, nil,
		NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()), logging.NewNoop())

	tests := []struct {
		name  string
		value string
	}{
		{"plain bearer", "Bearer xxxxxxxx"},
		{"empty", ""},
		{"only prefix", APITokenPrefix},
		{"short with prefix", APITokenPrefix + "abc"}, // < 8 байт после префикса
		{"random hex", "deadbeefcafef00d"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := uc.Verify(context.Background(), tc.value)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "not an api token")
		})
	}
}

func TestAPITokenUsecase_Verify_UnknownToken_Unauthorized(t *testing.T) {
	t.Parallel()

	repo := newInMemAPITokenRepo() // пустой
	users := &userRepoStub{}
	audit := NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop())
	uc := NewAPITokenUsecase(repo, users, nil, audit, logging.NewNoop())

	// Сгенерируем валидный по форме токен, но в БД его нет.
	v, err := generateToken()
	require.NoError(t, err)

	_, _, err = uc.Verify(context.Background(), v)
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrUnauthorized,
		"неизвестный токен должен возвращать ErrUnauthorized (а не ErrNotFound — не утекаем существование)")
}

func TestAPITokenUsecase_Verify_ExpiredToken_Unauthorized(t *testing.T) {
	t.Parallel()

	repo := newInMemAPITokenRepo()
	users := &userRepoStub{byID: map[string]*domain.User{"u-1": {ID: "u-1", Active: true}}}
	audit := NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop())
	uc := NewAPITokenUsecase(repo, users, nil, audit, logging.NewNoop())

	created, err := uc.Create(context.Background(), Actor{}, "u-1", "team-1", "expired",
		[]string{domain.ScopeLogsRead}, nil)
	require.NoError(t, err)
	// Create принимает срок в днях и прошедшее не пропустит (сервер считает от
	// «сейчас»), поэтому протухание эмулируем на уровне хранилища.
	past := time.Now().Add(-time.Hour)
	created.Token.ExpiresAt = &past

	_, _, err = uc.Verify(context.Background(), created.Plain)
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
}

func TestAPITokenUsecase_Verify_RevokedToken_Unauthorized(t *testing.T) {
	t.Parallel()

	repo := newInMemAPITokenRepo()
	users := &userRepoStub{byID: map[string]*domain.User{"u-1": {ID: "u-1", Active: true}}}
	audit := NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop())
	uc := NewAPITokenUsecase(repo, users, nil, audit, logging.NewNoop())

	created, err := uc.Create(context.Background(), Actor{}, "u-1", "team-1", "t",
		[]string{domain.ScopeLogsRead}, nil)
	require.NoError(t, err)

	// Вручную проставляем RevokedAt — IsActive вернёт false.
	now := time.Now()
	created.Token.RevokedAt = &now

	_, _, err = uc.Verify(context.Background(), created.Plain)
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
}

func TestAPITokenUsecase_Verify_InactiveUser_ErrUserInactive(t *testing.T) {
	t.Parallel()

	repo := newInMemAPITokenRepo()
	users := &userRepoStub{byID: map[string]*domain.User{"u-1": {ID: "u-1", Active: false}}}
	audit := NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop())
	uc := NewAPITokenUsecase(repo, users, nil, audit, logging.NewNoop())

	created, err := uc.Create(context.Background(), Actor{}, "u-1", "team-1", "t",
		[]string{domain.ScopeLogsRead}, nil)
	require.NoError(t, err)

	_, _, err = uc.Verify(context.Background(), created.Plain)
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrUserInactive,
		"inactive user → ErrUserInactive (отличается от ErrUnauthorized, чтобы middleware вернул понятный 403)")
}

// §4 CLAUDE.md: время — зависимость. Проверяем ровно то, ради чего она введена:
// протухший токен перестаёт авторизовывать, а срок жизни считается от «сейчас»
// сервера. С прямым time.Now() такой тест требовал бы либо ожидания реального
// срока, либо ручной подделки ExpiresAt мимо бизнес-логики.
func TestAPITokenUsecase_ExpiryUsesInjectedClock(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	fake := clock.NewFake(now)

	repo := newInMemAPITokenRepo()
	users := &userRepoStub{byID: map[string]*domain.User{
		"u-1": {ID: "u-1", Login: "alice", Active: true},
	}}
	audit := NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop())
	uc := NewAPITokenUsecase(repo, users, nil, audit, logging.NewNoop(),
		WithAPITokenClock(fake))

	days := 2
	created, err := uc.Create(context.Background(), Actor{UserLogin: "alice"},
		"u-1", "team-1", "short-lived", []string{domain.ScopeLogsRead}, &days)
	require.NoError(t, err)
	require.NotNil(t, created.Token.ExpiresAt)
	assert.True(t, now.Add(48*time.Hour).Equal(*created.Token.ExpiresAt),
		"срок считается от часов сервера, а не от часов клиента")

	// Пока не истёк — токен валиден.
	_, _, err = uc.Verify(context.Background(), created.Plain)
	require.NoError(t, err)

	// Через двое суток и минуту — уже нет.
	fake.Advance(48*time.Hour + time.Minute)
	_, _, err = uc.Verify(context.Background(), created.Plain)
	assert.ErrorIs(t, err, domain.ErrUnauthorized,
		"истёкший токен обязан перестать авторизовывать")
}
