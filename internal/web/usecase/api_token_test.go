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
func (r *inMemAPITokenRepo) ListByUser(_ context.Context, _ string) ([]*domain.APIToken, error) {
	return nil, nil
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
func (r *inMemAPITokenRepo) Delete(_ context.Context, _, _ string) error { return nil }
func (r *inMemAPITokenRepo) TouchLastUsed(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.touched = append(r.touched, id)
	return nil
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
func (s *userRepoStub) List(_ context.Context, _ port.ListUsersFilter) ([]*domain.User, error) {
	return nil, nil
}
func (s *userRepoStub) CountActiveAdmins(_ context.Context) (int, error) { return 1, nil }
func (s *userRepoStub) Create(_ context.Context, _ *domain.User) error   { return nil }
func (s *userRepoStub) Update(_ context.Context, _ *domain.User) error   { return nil }
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

	uc := NewAPITokenUsecase(repo, users, audit, logging.NewNoop())

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

func TestAPITokenUsecase_Create_RequiresName(t *testing.T) {
	t.Parallel()

	repo := newInMemAPITokenRepo()
	users := &userRepoStub{}
	audit := NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop())
	uc := NewAPITokenUsecase(repo, users, audit, logging.NewNoop())

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
	uc := NewAPITokenUsecase(repo, users, audit, logging.NewNoop())

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

	uc := NewAPITokenUsecase(newInMemAPITokenRepo(), &userRepoStub{},
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
	uc := NewAPITokenUsecase(repo, users, audit, logging.NewNoop())

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
	uc := NewAPITokenUsecase(repo, users, audit, logging.NewNoop())

	past := time.Now().Add(-time.Hour)
	created, err := uc.Create(context.Background(), Actor{}, "u-1", "team-1", "expired",
		[]string{domain.ScopeLogsRead}, &past)
	require.NoError(t, err)

	_, _, err = uc.Verify(context.Background(), created.Plain)
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
}

func TestAPITokenUsecase_Verify_RevokedToken_Unauthorized(t *testing.T) {
	t.Parallel()

	repo := newInMemAPITokenRepo()
	users := &userRepoStub{byID: map[string]*domain.User{"u-1": {ID: "u-1", Active: true}}}
	audit := NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop())
	uc := NewAPITokenUsecase(repo, users, audit, logging.NewNoop())

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
	uc := NewAPITokenUsecase(repo, users, audit, logging.NewNoop())

	created, err := uc.Create(context.Background(), Actor{}, "u-1", "team-1", "t",
		[]string{domain.ScopeLogsRead}, nil)
	require.NoError(t, err)

	_, _, err = uc.Verify(context.Background(), created.Plain)
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrUserInactive,
		"inactive user → ErrUserInactive (отличается от ErrUnauthorized, чтобы middleware вернул понятный 403)")
}
