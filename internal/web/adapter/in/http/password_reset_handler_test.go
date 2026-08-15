package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/clock"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/mail"
	"nexus/internal/web/usecase"
	"nexus/internal/web/usecase/port"
)

// §88.4.2: HTTP-слой восстановления пароля. Главное, что здесь проверяется, —
// ответ 202 одинаков при любом внутреннем исходе: именно на этом уровне
// утечка «такой пользователь есть» была бы заметна снаружи.

// ── фейки ──────────────────────────────────────────────────────────────────

type prUserRepo struct {
	byLogin map[string]*domain.User
	byID    map[string]*domain.User
	getErr  error
}

func (r *prUserRepo) Get(_ context.Context, id string) (*domain.User, error) {
	if u, ok := r.byID[id]; ok {
		return u, nil
	}
	return nil, domain.ErrUserNotFound
}

func (r *prUserRepo) GetByLogin(_ context.Context, login string) (*domain.User, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	if u, ok := r.byLogin[login]; ok {
		return u, nil
	}
	return nil, domain.ErrUserNotFound
}

func (r *prUserRepo) GetByEmail(_ context.Context, email string) (*domain.User, error) {
	for _, u := range r.byLogin {
		if u.Email != "" && strings.EqualFold(u.Email, email) {
			return u, nil
		}
	}
	return nil, domain.ErrUserNotFound
}

func (r *prUserRepo) List(_ context.Context, _ port.ListUsersFilter) ([]*domain.User, error) {
	return nil, nil
}
func (r *prUserRepo) CountActiveAdmins(_ context.Context) (int, error)            { return 1, nil }
func (r *prUserRepo) Create(_ context.Context, _ *domain.User) error              { return nil }
func (r *prUserRepo) Update(_ context.Context, _ *domain.User) error              { return nil }
func (r *prUserRepo) UpdateDefaultTeam(_ context.Context, _, _ string) error      { return nil }
func (r *prUserRepo) UpdatePassword(_ context.Context, _, _ string, _ bool) error { return nil }
func (r *prUserRepo) UpdateLastLogin(_ context.Context, _ string, _ time.Time) error {
	return nil
}
func (r *prUserRepo) Delete(_ context.Context, _ string) error { return nil }

type prTokenRepo struct {
	mu        sync.Mutex
	created   int
	consume   *domain.OneTimeToken
	consErr   error
	peek      bool
	createErr error
}

func (r *prTokenRepo) Create(_ context.Context, _ *domain.OneTimeToken) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.createErr != nil {
		return r.createErr
	}
	r.created++
	return nil
}

func (r *prTokenRepo) Consume(_ context.Context, _ domain.TokenPurpose, _ string, _ time.Time) (*domain.OneTimeToken, error) {
	if r.consErr != nil {
		return nil, r.consErr
	}
	return r.consume, nil
}
func (r *prTokenRepo) Peek(_ context.Context, _ domain.TokenPurpose, _ string, _ time.Time) (bool, error) {
	return r.peek, nil
}
func (r *prTokenRepo) CountActive(_ context.Context, _ domain.TokenPurpose, _ string, _ time.Time) (int, error) {
	return 0, nil
}
func (r *prTokenRepo) InvalidateByUser(_ context.Context, _ domain.TokenPurpose, _ string, _ time.Time) (int, error) {
	return 0, nil
}
func (r *prTokenRepo) DeleteExpiredBefore(_ context.Context, _ time.Time) (int, error) {
	return 0, nil
}

type prMailSender struct {
	mu   sync.Mutex
	sent int
	err  error
}

func (s *prMailSender) Send(_ context.Context, _ mail.Config, _ mail.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.sent++
	return nil
}

type prChanger struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (c *prChanger) ChangePassword(_ context.Context, _ usecase.Actor, _, _ string, _ bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	c.calls++
	return nil
}

// prLimiter — лимитер с управляемым вердиктом и ошибкой (fail-open).
type prLimiter struct {
	allow bool
	err   error
}

func (l *prLimiter) Allow(_ context.Context, _ string, _ int) (bool, error) {
	return l.allow, l.err
}

// ── сборка ─────────────────────────────────────────────────────────────────

type prFixture struct {
	router  *gin.Engine
	tokens  *prTokenRepo
	sender  *prMailSender
	changer *prChanger
	limiter *prLimiter
}

func prSettings() *domain.AppSettings {
	return &domain.AppSettings{
		General: domain.GeneralSettings{PublicBaseURL: new("https://nexus.example.com")},
		Mail: domain.MailSettings{
			Enabled:              new(true),
			Host:                 new("smtp.example.com"),
			FromAddress:          new("nexus@example.com"),
			PasswordResetEnabled: new(true),
			PasswordResetTTLMin:  new(60),
		},
	}
}

func newPRFixture(t *testing.T, users *prUserRepo, settings *domain.AppSettings) *prFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)

	tpl, err := mail.NewTemplates()
	require.NoError(t, err)

	f := &prFixture{
		tokens:  &prTokenRepo{},
		sender:  &prMailSender{},
		changer: &prChanger{},
		limiter: &prLimiter{allow: true},
	}
	uc := usecase.NewPasswordResetUsecase(
		users, f.tokens, &fakeSettingsRepo{current: settings}, f.sender, tpl,
		f.changer, usecase.NewAuditUsecase(nopAuditRepo{}, logging.NewNoop()),
		"Nexus", "", logging.NewNoop(),
		usecase.WithPasswordResetClock(clock.System()),
		usecase.WithPasswordResetRunner(func(fn func()) { fn() }),
	)
	h := NewPasswordResetHandler(uc, f.limiter, 5, logging.NewNoop())

	r := gin.New()
	r.POST("/api/auth/password-reset/request", h.Request)
	r.GET("/api/auth/password-reset/validate", h.Validate)
	r.POST("/api/auth/password-reset/confirm", h.Confirm)
	f.router = r
	return f
}

func (f *prFixture) post(path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	f.router.ServeHTTP(w, req)
	return w
}

func prActiveUser() *domain.User {
	return &domain.User{
		ID: "u-1", Login: "ivanov", Name: "Иван",
		Email: "ivanov@example.com", Active: true, Lang: domain.UserLangRU,
	}
}

func usersWith(list ...*domain.User) *prUserRepo {
	r := &prUserRepo{byLogin: map[string]*domain.User{}, byID: map[string]*domain.User{}}
	for _, u := range list {
		r.byLogin[u.Login] = u
		r.byID[u.ID] = u
	}
	return r
}

// ── Request: единый ответ ──────────────────────────────────────────────────

// КЛЮЧЕВОЙ тест раздела: снаружи исходы неразличимы — ни кодом, ни телом.
func TestPasswordResetHandler_Request_IdenticalResponse(t *testing.T) {
	t.Parallel()

	noEmail := prActiveUser()
	noEmail.ID, noEmail.Login, noEmail.Email = "u-2", "petrov", ""

	tests := []struct {
		name     string
		users    *prUserRepo
		settings *domain.AppSettings
		input    string
	}{
		{"пользователь есть", usersWith(prActiveUser()), prSettings(), "ivanov"},
		{"пользователя нет", usersWith(prActiveUser()), prSettings(), "nobody"},
		{"нет email", usersWith(noEmail), prSettings(), "petrov"},
		{"почта выключена", usersWith(prActiveUser()), &domain.AppSettings{}, "ivanov"},
	}

	var bodies []string
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newPRFixture(t, tt.users, tt.settings)
			w := f.post("/api/auth/password-reset/request", `{"login":"`+tt.input+`"}`)

			require.Equal(t, http.StatusAccepted, w.Code)
			bodies = append(bodies, w.Body.String())
		})
	}

	for i := 1; i < len(bodies); i++ {
		assert.Equal(t, bodies[0], bodies[i],
			"тело ответа обязано совпадать при любом исходе (§88.4.3)")
	}
}

func TestPasswordResetHandler_Request_BadBody(t *testing.T) {
	t.Parallel()
	f := newPRFixture(t, usersWith(prActiveUser()), prSettings())

	for _, body := range []string{`{}`, `{"login":""}`, `not json`} {
		w := f.post("/api/auth/password-reset/request", body)
		assert.Equal(t, http.StatusBadRequest, w.Code, "тело: %s", body)
	}
}

func TestPasswordResetHandler_Request_RateLimited(t *testing.T) {
	t.Parallel()
	f := newPRFixture(t, usersWith(prActiveUser()), prSettings())
	f.limiter.allow = false

	w := f.post("/api/auth/password-reset/request", `{"login":"ivanov"}`)

	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.Zero(t, f.tokens.created, "при отказе лимита токен создаваться не должен")
}

// §9.4: при недоступном Redis лимит деградирует в пропуск, а не в отказ —
// иначе сбой кеша обрушил бы восстановление пароля целиком.
func TestPasswordResetHandler_Request_RateLimiterFailOpen(t *testing.T) {
	t.Parallel()
	f := newPRFixture(t, usersWith(prActiveUser()), prSettings())
	f.limiter.allow = true
	f.limiter.err = errors.New("redis is down")

	w := f.post("/api/auth/password-reset/request", `{"login":"ivanov"}`)

	assert.Equal(t, http.StatusAccepted, w.Code)
}

// Инфраструктурный сбой обязан быть 500, а не тихим 202: иначе пользователю
// обещано письмо, которого не будет.
func TestPasswordResetHandler_Request_StorageFailureIs500(t *testing.T) {
	t.Parallel()
	f := newPRFixture(t, usersWith(prActiveUser()), prSettings())
	f.tokens.createErr = errors.New("db is down")

	w := f.post("/api/auth/password-reset/request", `{"login":"ivanov"}`)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestPasswordResetHandler_Request_ReturnsTTL(t *testing.T) {
	t.Parallel()
	f := newPRFixture(t, usersWith(prActiveUser()), prSettings())

	w := f.post("/api/auth/password-reset/request", `{"login":"ivanov"}`)
	require.Equal(t, http.StatusAccepted, w.Code)

	var got passwordResetRequestResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.True(t, got.OK)
	assert.Equal(t, 60, got.TTLMinutes)
}

// ── Validate ───────────────────────────────────────────────────────────────

func TestPasswordResetHandler_Validate(t *testing.T) {
	t.Parallel()
	f := newPRFixture(t, usersWith(prActiveUser()), prSettings())

	get := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		f.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
			"/api/auth/password-reset/validate?token=abc", nil))
		return w
	}

	f.tokens.peek = true
	w := get()
	require.Equal(t, http.StatusOK, w.Code)
	var ok passwordResetValidateResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &ok))
	assert.True(t, ok.Valid)

	f.tokens.peek = false
	w = get()
	require.Equal(t, http.StatusOK, w.Code, "недействительная ссылка — не ошибка запроса")
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &ok))
	assert.False(t, ok.Valid)
}

func TestPasswordResetHandler_Validate_NoToken(t *testing.T) {
	t.Parallel()
	f := newPRFixture(t, usersWith(prActiveUser()), prSettings())

	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/api/auth/password-reset/validate", nil))

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ── Confirm ────────────────────────────────────────────────────────────────

func TestPasswordResetHandler_Confirm(t *testing.T) {
	t.Parallel()

	const goodToken = "0123456789abcdef0123"

	t.Run("успех — 204", func(t *testing.T) {
		t.Parallel()
		u := prActiveUser()
		f := newPRFixture(t, usersWith(u), prSettings())
		f.tokens.consume = &domain.OneTimeToken{UserID: u.ID, Purpose: domain.TokenPurposePasswordReset}

		w := f.post("/api/auth/password-reset/confirm",
			`{"token":"`+goodToken+`","new_password":"new-strong-password"}`)

		assert.Equal(t, http.StatusNoContent, w.Code)
		assert.Equal(t, 1, f.changer.calls)
	})

	t.Run("недействительный токен — 400", func(t *testing.T) {
		t.Parallel()
		f := newPRFixture(t, usersWith(prActiveUser()), prSettings())
		f.tokens.consErr = domain.ErrOneTimeTokenInvalid

		w := f.post("/api/auth/password-reset/confirm",
			`{"token":"`+goodToken+`","new_password":"new-strong-password"}`)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Zero(t, f.changer.calls)
	})

	t.Run("отключённый пользователь — 400", func(t *testing.T) {
		t.Parallel()
		u := prActiveUser()
		u.Active = false
		f := newPRFixture(t, usersWith(u), prSettings())
		f.tokens.consume = &domain.OneTimeToken{UserID: u.ID, Purpose: domain.TokenPurposePasswordReset}

		w := f.post("/api/auth/password-reset/confirm",
			`{"token":"`+goodToken+`","new_password":"new-strong-password"}`)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Zero(t, f.changer.calls)
	})

	// Короткий пароль отсекается биндингом до usecase — ссылка не тратится.
	t.Run("короткий пароль — 400", func(t *testing.T) {
		t.Parallel()
		f := newPRFixture(t, usersWith(prActiveUser()), prSettings())
		f.tokens.consErr = errors.New("Consume не должен вызываться")

		w := f.post("/api/auth/password-reset/confirm",
			`{"token":"`+goodToken+`","new_password":"short"}`)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Zero(t, f.changer.calls)
	})

	// Ревизия §88.9: эндпоинт публичный, и текст внутренней ошибки наружу
	// отдавать нельзя — в ошибке хранилища бывает имя таблицы и кусок запроса.
	t.Run("внутренний сбой не показывает свой текст", func(t *testing.T) {
		t.Parallel()
		u := prActiveUser()
		f := newPRFixture(t, usersWith(u), prSettings())
		f.tokens.consume = &domain.OneTimeToken{UserID: u.ID, Purpose: domain.TokenPurposePasswordReset}
		f.changer.err = errors.New(`pq: relation "users" does not exist`)

		w := f.post("/api/auth/password-reset/confirm",
			`{"token":"`+goodToken+`","new_password":"new-strong-password"}`)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
		assert.NotContains(t, w.Body.String(), "relation")
		assert.NotContains(t, w.Body.String(), "users")
	})

	t.Run("лимит — 429", func(t *testing.T) {
		t.Parallel()
		f := newPRFixture(t, usersWith(prActiveUser()), prSettings())
		f.limiter.allow = false

		w := f.post("/api/auth/password-reset/confirm",
			`{"token":"`+goodToken+`","new_password":"new-strong-password"}`)

		assert.Equal(t, http.StatusTooManyRequests, w.Code)
	})
}
