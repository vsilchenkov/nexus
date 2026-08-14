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
	"nexus/internal/platform/mail"
)

// §88.4: восстановление пароля. Ключевое, что здесь проверяется, — единый
// исход наружу при семи разных внутренних причинах и порядок операций в
// Confirm (политика пароля ДО гашения токена).

var testNow = time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)

// ── фейки ──────────────────────────────────────────────────────────────────

type fakeTokenRepo struct {
	mu        sync.Mutex
	created   []*domain.OneTimeToken
	active    int
	consume   *domain.OneTimeToken
	consErr   error
	peek      bool
	killed    int
	cleaned   int
	createErr error
	countErr  error
}

func (f *fakeTokenRepo) Create(_ context.Context, t *domain.OneTimeToken) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return f.createErr
	}
	f.created = append(f.created, t)
	return nil
}

func (f *fakeTokenRepo) Consume(_ context.Context, p domain.TokenPurpose, _ string, _ time.Time) (*domain.OneTimeToken, error) {
	if p != domain.TokenPurposePasswordReset {
		return nil, domain.ErrOneTimeTokenInvalid
	}
	if f.consErr != nil {
		return nil, f.consErr
	}
	return f.consume, nil
}

func (f *fakeTokenRepo) Peek(_ context.Context, _ domain.TokenPurpose, _ string, _ time.Time) (bool, error) {
	return f.peek, nil
}

func (f *fakeTokenRepo) CountActive(_ context.Context, _ domain.TokenPurpose, _ string, _ time.Time) (int, error) {
	if f.countErr != nil {
		return 0, f.countErr
	}
	return f.active, nil
}

func (f *fakeTokenRepo) InvalidateByUser(_ context.Context, _ domain.TokenPurpose, _ string, _ time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killed++
	return 1, nil
}

func (f *fakeTokenRepo) DeleteExpiredBefore(_ context.Context, _ time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleaned++
	return 0, nil
}

func (f *fakeTokenRepo) lastCreated() *domain.OneTimeToken {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.created) == 0 {
		return nil
	}
	return f.created[len(f.created)-1]
}

type fakeChanger struct {
	mu     sync.Mutex
	calls  []string
	last   string
	mustCh bool
	err    error
}

func (f *fakeChanger) ChangePassword(_ context.Context, _ Actor, userID, newPassword string, mustChange bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, userID)
	f.last = newPassword
	f.mustCh = mustChange
	return nil
}

type fakeResetMetrics struct {
	mu      sync.Mutex
	results []string
}

func (f *fakeResetMetrics) IncPasswordResetRequest(result string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results = append(f.results, result)
}

func (f *fakeResetMetrics) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.results) == 0 {
		return ""
	}
	return f.results[len(f.results)-1]
}

// ── сборка ─────────────────────────────────────────────────────────────────

func mailReadySettings() *domain.AppSettings {
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

type resetFixture struct {
	uc      *PasswordResetUsecase
	users   *authUserRepo
	tokens  *fakeTokenRepo
	sender  *fakeMailSender
	changer *fakeChanger
	audit   *fakeAuditRepo
	metrics *fakeResetMetrics
}

func newResetFixture(t *testing.T, settings *domain.AppSettings, users ...*domain.User) *resetFixture {
	t.Helper()

	repo := &authUserRepo{byID: map[string]*domain.User{}, byLogin: map[string]*domain.User{}}
	for _, u := range users {
		repo.byID[u.ID] = u
		repo.byLogin[u.Login] = u
	}

	tpl, err := mail.NewTemplates()
	require.NoError(t, err)

	f := &resetFixture{
		users:   repo,
		tokens:  &fakeTokenRepo{},
		sender:  &fakeMailSender{},
		changer: &fakeChanger{},
		audit:   &fakeAuditRepo{},
		metrics: &fakeResetMetrics{},
	}
	f.uc = NewPasswordResetUsecase(
		repo, f.tokens, &fakeAppSettingsRepo{current: settings}, f.sender, tpl,
		f.changer, NewAuditUsecase(f.audit, logging.NewNoop()),
		"Nexus", "", logging.NewNoop(),
		WithPasswordResetClock(clock.Func(func() time.Time { return testNow })),
		// Синхронный runner: иначе тесты флейкуют и спорят с goleak.
		WithPasswordResetRunner(func(fn func()) { fn() }),
		WithPasswordResetMetrics(f.metrics),
	)
	return f
}

func activeUser() *domain.User {
	return &domain.User{
		ID: "u-1", Login: "ivanov", Name: "Иван Иванов",
		Email: "ivanov@example.com", Active: true, Lang: domain.UserLangRU,
	}
}

// auditDetail достаёт поле последней аудит-записи.
func (f *resetFixture) auditDetail(t *testing.T, key string) any {
	t.Helper()
	require.NotEmpty(t, f.audit.written, "аудит-запись обязана быть у каждого исхода")
	return f.audit.written[len(f.audit.written)-1].Details[key]
}

// ── Request: единый ответ при разных исходах ───────────────────────────────

func TestPasswordReset_Request_AllOutcomesLookIdentical(t *testing.T) {
	t.Parallel()

	noEmail := activeUser()
	noEmail.ID, noEmail.Login, noEmail.Email = "u-2", "petrov", ""

	disabled := activeUser()
	disabled.ID, disabled.Login, disabled.Active = "u-3", "sidorov", false

	tests := []struct {
		name        string
		input       string
		settings    *domain.AppSettings
		users       []*domain.User
		activeLinks int
		wantResult  string
		wantSent    bool
	}{
		{
			name: "письмо ушло", input: "ivanov", settings: mailReadySettings(),
			users: []*domain.User{activeUser()}, wantResult: resetResultSent, wantSent: true,
		},
		{
			name: "пользователя нет", input: "nobody", settings: mailReadySettings(),
			users: []*domain.User{activeUser()}, wantResult: resetResultUserNotFound,
		},
		{
			name: "нет email", input: "petrov", settings: mailReadySettings(),
			users: []*domain.User{noEmail}, wantResult: resetResultNoEmail,
		},
		{
			name: "пользователь отключён", input: "sidorov", settings: mailReadySettings(),
			users: []*domain.User{disabled}, wantResult: resetResultInactive,
		},
		{
			name: "почта выключена", input: "ivanov", settings: &domain.AppSettings{},
			users: []*domain.User{activeUser()}, wantResult: resetResultMailDisabled,
		},
		{
			name: "превышен предел активных ссылок", input: "ivanov", settings: mailReadySettings(),
			users: []*domain.User{activeUser()}, activeLinks: maxActivePasswordResets,
			wantResult: resetResultTooManyActive,
		},
		{
			name: "пустой ввод", input: "   ", settings: mailReadySettings(),
			users: []*domain.User{activeUser()}, wantResult: resetResultUserNotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newResetFixture(t, tt.settings, tt.users...)
			f.tokens.active = tt.activeLinks

			ttl, err := f.uc.Request(context.Background(), tt.input, "10.1.2.3")

			// Наружу — одно и то же при любом исходе.
			require.NoError(t, err, "внутренняя причина не должна становиться ошибкой")
			assert.Equal(t, 60, ttl, "срок жизни ссылки одинаков во всех исходах")

			// Различие видно только в аудите.
			assert.Equal(t, tt.wantResult, f.auditDetail(t, "result"))
			assert.Equal(t, tt.wantResult, f.metrics.last())

			_, _, sent := f.sender.last()
			assert.Equal(t, tt.wantSent, sent)
			if !tt.wantSent {
				assert.Empty(t, f.tokens.created, "без отправки токен создаваться не должен")
			}
		})
	}
}

// Неоднозначный email — отдельный исход: письмо не уходит, чтобы владелец
// общего адреса не получил ссылку на чужую учётную запись (§88.4.2).
func TestPasswordReset_Request_AmbiguousEmail(t *testing.T) {
	t.Parallel()

	a, b := activeUser(), activeUser()
	b.ID, b.Login = "u-2", "petrov"
	b.Email = a.Email // общий адрес

	f := newResetFixture(t, mailReadySettings(), a, b)

	ttl, err := f.uc.Request(context.Background(), a.Email, "10.1.2.3")
	require.NoError(t, err)
	assert.Equal(t, 60, ttl)

	assert.Equal(t, resetResultEmailAmbiguous, f.auditDetail(t, "result"))
	_, _, sent := f.sender.last()
	assert.False(t, sent)
}

func TestPasswordReset_Request_ByEmail(t *testing.T) {
	t.Parallel()
	f := newResetFixture(t, mailReadySettings(), activeUser())

	_, err := f.uc.Request(context.Background(), "IVANOV@example.com", "10.1.2.3")
	require.NoError(t, err)

	assert.Equal(t, resetResultSent, f.auditDetail(t, "result"))
	_, msg, sent := f.sender.last()
	require.True(t, sent)
	assert.Equal(t, "ivanov@example.com", msg.To)
}

// Письмо обязано нести рабочую ссылку, а в БД — только хеш токена.
func TestPasswordReset_Request_LinkAndHashing(t *testing.T) {
	t.Parallel()
	f := newResetFixture(t, mailReadySettings(), activeUser())

	_, err := f.uc.Request(context.Background(), "ivanov", "10.1.2.3")
	require.NoError(t, err)

	_, msg, sent := f.sender.last()
	require.True(t, sent)

	const prefix = "https://nexus.example.com/reset-password?token="
	require.Contains(t, msg.Text, prefix)
	idx := strings.Index(msg.Text, prefix)
	raw := msg.Text[idx+len(prefix):]
	raw = strings.Fields(raw)[0]
	require.NotEmpty(t, raw)

	rec := f.tokens.lastCreated()
	require.NotNil(t, rec)
	assert.NotEqual(t, raw, rec.TokenHash, "в хранилище обязан лежать хеш, а не сам токен")
	assert.Equal(t, hashToken(raw), rec.TokenHash)
	assert.Equal(t, domain.TokenPurposePasswordReset, rec.Purpose)
	assert.Equal(t, testNow.Add(60*time.Minute), rec.ExpiresAt)
	assert.Equal(t, "10.1.2.3", rec.RequestIP)

	// Ленивая чистка запускается тем же запросом.
	assert.Positive(t, f.tokens.cleaned)
}

// Язык письма — получателя, а не запрашивающего (§88.6).
func TestPasswordReset_Request_RecipientLanguage(t *testing.T) {
	t.Parallel()

	t.Run("ru", func(t *testing.T) {
		t.Parallel()
		f := newResetFixture(t, mailReadySettings(), activeUser())
		_, err := f.uc.Request(context.Background(), "ivanov", "")
		require.NoError(t, err)
		_, msg, _ := f.sender.last()
		assert.Contains(t, msg.Subject, "восстановление пароля")
		assert.Contains(t, msg.Text, "Здравствуйте")
	})

	t.Run("en", func(t *testing.T) {
		t.Parallel()
		u := activeUser()
		u.Lang = domain.UserLangEN
		f := newResetFixture(t, mailReadySettings(), u)
		_, err := f.uc.Request(context.Background(), "ivanov", "")
		require.NoError(t, err)
		_, msg, _ := f.sender.last()
		assert.Contains(t, msg.Subject, "password reset")
		assert.Contains(t, msg.Text, "Hello")
	})
}

// Пустой IP не должен оставлять в письме висящую строку «запрос с адреса».
func TestPasswordReset_Request_EmptyIPOmitsBlock(t *testing.T) {
	t.Parallel()
	f := newResetFixture(t, mailReadySettings(), activeUser())

	_, err := f.uc.Request(context.Background(), "ivanov", "")
	require.NoError(t, err)

	_, msg, _ := f.sender.last()
	assert.NotContains(t, msg.Text, "Запрос отправлен с адреса")
	assert.NotContains(t, msg.HTML, "Запрос отправлен с адреса")
}

// Сбой отправки не меняет ответ пользователю, но обязан быть видимым.
func TestPasswordReset_Request_SendFailureIsInvisibleToUser(t *testing.T) {
	t.Parallel()
	f := newResetFixture(t, mailReadySettings(), activeUser())
	f.sender.err = errors.New("dial tcp: i/o timeout")

	ttl, err := f.uc.Request(context.Background(), "ivanov", "10.1.2.3")

	require.NoError(t, err, "сбой SMTP не должен превращаться в 500")
	assert.Equal(t, 60, ttl)
	assert.Equal(t, resetResultSendFailed, f.auditDetail(t, "result"))
	assert.Contains(t, f.auditDetail(t, "error"), "i/o timeout")
	assert.Equal(t, resetResultSendFailed, f.metrics.last())
}

// Инфраструктурный сбой хранилища — наоборот, обязан быть ошибкой: молча
// «принять» запрос, не создав токен, значит обещать письмо, которого не будет.
func TestPasswordReset_Request_StorageFailureIsError(t *testing.T) {
	t.Parallel()

	t.Run("не удалось создать токен", func(t *testing.T) {
		t.Parallel()
		f := newResetFixture(t, mailReadySettings(), activeUser())
		f.tokens.createErr = errors.New("db is down")

		_, err := f.uc.Request(context.Background(), "ivanov", "")
		require.Error(t, err)
	})

	t.Run("не удалось посчитать активные", func(t *testing.T) {
		t.Parallel()
		f := newResetFixture(t, mailReadySettings(), activeUser())
		f.tokens.countErr = errors.New("db is down")

		_, err := f.uc.Request(context.Background(), "ivanov", "")
		require.Error(t, err)
	})
}

// ── Validate ───────────────────────────────────────────────────────────────

func TestPasswordReset_Validate(t *testing.T) {
	t.Parallel()

	f := newResetFixture(t, mailReadySettings(), activeUser())
	f.tokens.peek = true

	ok, err := f.uc.Validate(context.Background(), "some-token")
	require.NoError(t, err)
	assert.True(t, ok)

	f.tokens.peek = false
	ok, err = f.uc.Validate(context.Background(), "some-token")
	require.NoError(t, err)
	assert.False(t, ok)

	ok, err = f.uc.Validate(context.Background(), "  ")
	require.NoError(t, err)
	assert.False(t, ok, "пустой токен не должен ходить в хранилище")
}

// ── Confirm ────────────────────────────────────────────────────────────────

func consumable(userID string) *domain.OneTimeToken {
	used := testNow
	return &domain.OneTimeToken{
		ID: "t-1", UserID: userID, Purpose: domain.TokenPurposePasswordReset,
		ExpiresAt: testNow.Add(time.Hour), UsedAt: &used,
	}
}

func TestPasswordReset_Confirm_ChangesPassword(t *testing.T) {
	t.Parallel()
	f := newResetFixture(t, mailReadySettings(), activeUser())
	f.tokens.consume = consumable("u-1")

	require.NoError(t, f.uc.Confirm(context.Background(), "token", "new-strong-password", "10.1.2.3"))

	require.Equal(t, []string{"u-1"}, f.changer.calls)
	assert.Equal(t, "new-strong-password", f.changer.last)
	assert.False(t, f.changer.mustCh,
		"человек только что задал пароль сам — требовать смену повторно бессмысленно")

	require.NotEmpty(t, f.audit.written)
	assert.Equal(t, domain.ActionUserPasswordResetConfirm,
		f.audit.written[len(f.audit.written)-1].Action)
}

// Порядок операций: политика пароля проверяется ДО гашения токена — иначе
// опечатка в длине убивала бы ссылку безвозвратно (§88.7).
func TestPasswordReset_Confirm_WeakPasswordDoesNotBurnToken(t *testing.T) {
	t.Parallel()
	f := newResetFixture(t, mailReadySettings(), activeUser())
	f.tokens.consume = consumable("u-1")
	f.tokens.consErr = errors.New("Consume не должен вызываться")

	err := f.uc.Confirm(context.Background(), "token", "short", "")

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "не должен вызываться",
		"токен обязан оставаться нетронутым при негодном пароле")
	assert.Empty(t, f.changer.calls)
}

func TestPasswordReset_Confirm_InvalidToken(t *testing.T) {
	t.Parallel()

	t.Run("хранилище не нашло", func(t *testing.T) {
		t.Parallel()
		f := newResetFixture(t, mailReadySettings(), activeUser())
		f.tokens.consErr = domain.ErrOneTimeTokenInvalid

		err := f.uc.Confirm(context.Background(), "token", "new-strong-password", "")
		require.ErrorIs(t, err, domain.ErrOneTimeTokenInvalid)
		assert.Empty(t, f.changer.calls)
	})

	t.Run("пустой токен", func(t *testing.T) {
		t.Parallel()
		f := newResetFixture(t, mailReadySettings(), activeUser())

		err := f.uc.Confirm(context.Background(), "  ", "new-strong-password", "")
		require.ErrorIs(t, err, domain.ErrOneTimeTokenInvalid)
	})
}

// ── хук гашения ссылок (§88.7) ─────────────────────────────────────────────

// Смена пароля обязана гасить выданные ссылки, иначе старое письмо позволило
// бы перебить только что установленный пароль. Хук стоит в ChangePassword —
// единственной точке смены, — поэтому срабатывает на всех каналах сразу.
func TestChangePassword_InvalidatesResetLinks(t *testing.T) {
	t.Parallel()

	u := activeUser()
	users := &authUserRepo{
		byID:    map[string]*domain.User{u.ID: u},
		byLogin: map[string]*domain.User{u.Login: u},
	}
	uc, _ := newAuthUC(users, newMemSessionRepo())
	tokens := &fakeTokenRepo{}
	uc = uc.WithPasswordResetInvalidator(tokens)

	require.NoError(t, uc.ChangePassword(context.Background(),
		Actor{UserID: "admin"}, u.ID, "new-strong-password", false))

	assert.Equal(t, 1, tokens.killed, "ссылки восстановления обязаны погаснуть")
}

// Без хука (unit-тесты, старый wiring) смена пароля обязана работать как
// раньше — nil не должен ронять операцию.
func TestChangePassword_WithoutInvalidatorStillWorks(t *testing.T) {
	t.Parallel()

	u := activeUser()
	users := &authUserRepo{
		byID:    map[string]*domain.User{u.ID: u},
		byLogin: map[string]*domain.User{u.Login: u},
	}
	uc, _ := newAuthUC(users, newMemSessionRepo())

	require.NoError(t, uc.ChangePassword(context.Background(),
		Actor{UserID: "admin"}, u.ID, "new-strong-password", false))
}

// Пользователя могли отключить между письмом и переходом — до суток разрыва.
func TestPasswordReset_Confirm_DeactivatedBetweenRequestAndConfirm(t *testing.T) {
	t.Parallel()

	u := activeUser()
	u.Active = false
	f := newResetFixture(t, mailReadySettings(), u)
	f.tokens.consume = consumable("u-1")

	err := f.uc.Confirm(context.Background(), "token", "new-strong-password", "")

	require.ErrorIs(t, err, domain.ErrUserInactive)
	assert.Empty(t, f.changer.calls, "отключённому пароль менять нельзя")
}
