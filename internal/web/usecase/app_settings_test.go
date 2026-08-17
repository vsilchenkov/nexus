package usecase

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/reloader"
	"nexus/internal/web/usecase/port"
)

func TestMaskSecret(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, in, want string
	}{
		{"empty", "", "***"},
		{"short", "abc", "***"},
		{"exactly8", "12345678", "***"},
		{"dsn-like", "https://abcdef@sentry.io/123456", "http***3456"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, maskSecret(tc.in))
		})
	}
}

func TestIsMaskedSecret(t *testing.T) {
	t.Parallel()
	assert.True(t, isMaskedSecret("***"))
	assert.True(t, isMaskedSecret("http***3456"))
	assert.False(t, isMaskedSecret("https://real@sentry.io/123"))
	assert.False(t, isMaskedSecret("plain-token"))
}

func TestMergeAppSettings_PreservesCurrentWhenPatchNil(t *testing.T) {
	t.Parallel()
	useTrue := true
	envFromCurrent := "production"
	current := &domain.AppSettings{
		Sentry: domain.SentrySettings{
			Use:         &useTrue,
			Environment: &envFromCurrent,
		},
	}
	patch := &domain.AppSettings{} // nothing changed
	out := mergeAppSettings(current, patch)

	require.NotNil(t, out.Sentry.Use)
	assert.True(t, *out.Sentry.Use)
	require.NotNil(t, out.Sentry.Environment)
	assert.Equal(t, "production", *out.Sentry.Environment)
}

func TestMergeAppSettings_MaskedDSNDoesNotOverwrite(t *testing.T) {
	t.Parallel()
	realDSN := "https://real@sentry.io/123"
	maskedDSN := "http***rest" // эмулирует то, что вернёт Get()

	current := &domain.AppSettings{Sentry: domain.SentrySettings{DSN: &realDSN}}
	patch := &domain.AppSettings{Sentry: domain.SentrySettings{DSN: &maskedDSN}}

	out := mergeAppSettings(current, patch)
	require.NotNil(t, out.Sentry.DSN)
	assert.Equal(t, "https://real@sentry.io/123", *out.Sentry.DSN,
		"masked DSN in patch must NOT replace real DSN in current")
}

func TestMergeAppSettings_NewDSNOverwrites(t *testing.T) {
	t.Parallel()
	oldDSN := "https://old@sentry.io/1"
	newDSN := "https://new@sentry.io/2"
	current := &domain.AppSettings{Sentry: domain.SentrySettings{DSN: &oldDSN}}
	patch := &domain.AppSettings{Sentry: domain.SentrySettings{DSN: &newDSN}}

	out := mergeAppSettings(current, patch)
	require.NotNil(t, out.Sentry.DSN)
	assert.Equal(t, newDSN, *out.Sentry.DSN)
}

func TestMergeAppSettings_MaskedCHPasswordDoesNotOverwrite(t *testing.T) {
	t.Parallel()
	realPwd := "supersecret"
	masked := "***"
	current := &domain.AppSettings{ClickHouse: domain.ClickHouseSettings{Password: &realPwd}}
	patch := &domain.AppSettings{ClickHouse: domain.ClickHouseSettings{Password: &masked}}

	out := mergeAppSettings(current, patch)
	require.NotNil(t, out.ClickHouse.Password)
	assert.Equal(t, realPwd, *out.ClickHouse.Password)
}

func TestChangedSections(t *testing.T) {
	t.Parallel()
	useTrue := true
	host := "ch.example.com"

	t.Run("sentry only", func(t *testing.T) {
		t.Parallel()
		p := &domain.AppSettings{Sentry: domain.SentrySettings{Use: &useTrue}}
		assert.Equal(t, []string{"sentry"}, changedSections(p))
	})
	t.Run("clickhouse only", func(t *testing.T) {
		t.Parallel()
		p := &domain.AppSettings{ClickHouse: domain.ClickHouseSettings{Host: &host}}
		assert.Equal(t, []string{"clickhouse"}, changedSections(p))
	})
	t.Run("both", func(t *testing.T) {
		t.Parallel()
		p := &domain.AppSettings{
			Sentry:     domain.SentrySettings{Use: &useTrue},
			ClickHouse: domain.ClickHouseSettings{Host: &host},
		}
		assert.Equal(t, []string{"sentry", "clickhouse"}, changedSections(p))
	})
	t.Run("nothing", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, changedSections(&domain.AppSettings{}))
	})
}

func TestAppSettingsUsecase_GetMasksSecrets(t *testing.T) {
	t.Parallel()
	realDSN := "https://realtoken@sentry.io/123456"
	realPwd := "topsecret"

	repo := &fakeAppSettingsRepo{
		current: &domain.AppSettings{
			Sentry:     domain.SentrySettings{DSN: &realDSN},
			ClickHouse: domain.ClickHouseSettings{Password: &realPwd},
		},
	}
	uc := NewAppSettingsUsecase(repo, NewAuditUsecase(&fakeAuditRepo{}, logging.NewNoop()), nil, false, logging.NewNoop())

	got, err := uc.Get(context.Background())
	require.NoError(t, err)

	require.NotNil(t, got.Sentry.DSN)
	assert.NotEqual(t, realDSN, *got.Sentry.DSN, "DSN must be masked")
	assert.Contains(t, *got.Sentry.DSN, "***")

	require.NotNil(t, got.ClickHouse.Password)
	assert.Equal(t, "***", *got.ClickHouse.Password)
}

func TestAppSettingsUsecase_UpdateMergesAndAudits(t *testing.T) {
	t.Parallel()
	envOld := "staging"
	envNew := "production"

	repo := &fakeAppSettingsRepo{
		current: &domain.AppSettings{Sentry: domain.SentrySettings{Environment: &envOld}},
	}
	audit := &fakeAuditRepo{}
	pub := &fakeReloadPublisher{}
	uc := NewAppSettingsUsecase(repo, NewAuditUsecase(audit, logging.NewNoop()), pub, false, logging.NewNoop())

	err := uc.Update(context.Background(), Actor{UserID: "u-1"}, &domain.AppSettings{
		Sentry: domain.SentrySettings{Environment: &envNew},
	})
	require.NoError(t, err)

	require.NotNil(t, repo.lastSaved)
	require.NotNil(t, repo.lastSaved.Sentry.Environment)
	assert.Equal(t, "production", *repo.lastSaved.Sentry.Environment)
	assert.Equal(t, "u-1", repo.lastSaved.UpdatedBy)

	require.Len(t, audit.written, 1)
	assert.Equal(t, domain.ActionAppSettingsUpdate, audit.written[0].Action)
	assert.Equal(t, []any{"sentry"},
		audit.written[0].Details["changed_sections"].([]any))

	// Publisher должен получить событие для секции sentry.
	assert.Equal(t, []string{"sentry"}, pub.sections)
}

func TestAppSettings_GetMasksBotToken(t *testing.T) {
	t.Parallel()
	repo := &fakeAppSettingsRepo{current: &domain.AppSettings{
		Notifications: domain.NotificationsSettings{Telegram: domain.TelegramSettings{
			BotToken: new("123456:secret-bot-token"),
		}},
	}}
	uc := NewAppSettingsUsecase(repo, NewAuditUsecase(&fakeAuditRepo{}, logging.NewNoop()), nil, false, logging.NewNoop())

	got, err := uc.Get(context.Background())
	require.NoError(t, err)
	require.NotNil(t, got.Notifications.Telegram.BotToken)
	assert.Equal(t, "***", *got.Notifications.Telegram.BotToken)
}

func TestMergeAppSettings_MaskedBotTokenDoesNotOverwrite(t *testing.T) {
	t.Parallel()
	current := &domain.AppSettings{Notifications: domain.NotificationsSettings{
		Telegram: domain.TelegramSettings{BotToken: new("real-token")},
	}}
	patch := &domain.AppSettings{Notifications: domain.NotificationsSettings{
		Telegram: domain.TelegramSettings{BotToken: new("***"), Cron: new("*/5 * * * *")},
	}}
	out := mergeAppSettings(current, patch)
	require.NotNil(t, out.Notifications.Telegram.BotToken)
	assert.Equal(t, "real-token", *out.Notifications.Telegram.BotToken)
	require.NotNil(t, out.Notifications.Telegram.Cron)
	assert.Equal(t, "*/5 * * * *", *out.Notifications.Telegram.Cron)
}

func TestChangedSections_Notifications(t *testing.T) {
	t.Parallel()
	p := &domain.AppSettings{Notifications: domain.NotificationsSettings{
		Telegram: domain.TelegramSettings{Enabled: new(true)},
	}}
	assert.Equal(t, []string{"notifications"}, changedSections(p))
}

func TestAppSettings_Update_InvalidCron(t *testing.T) {
	t.Parallel()
	repo := &fakeAppSettingsRepo{current: &domain.AppSettings{}}
	uc := NewAppSettingsUsecase(repo, NewAuditUsecase(&fakeAuditRepo{}, logging.NewNoop()), nil, false, logging.NewNoop())

	err := uc.Update(context.Background(), Actor{UserID: "u"}, &domain.AppSettings{
		Notifications: domain.NotificationsSettings{Telegram: domain.TelegramSettings{Cron: new("not a cron")}},
	})
	assert.ErrorIs(t, err, domain.ErrTelegramCronInvalid)
	assert.Nil(t, repo.lastSaved, "invalid cron must not persist")
}

func TestAppSettings_Update_ValidCron(t *testing.T) {
	t.Parallel()
	repo := &fakeAppSettingsRepo{current: &domain.AppSettings{}}
	pub := &fakeReloadPublisher{}
	uc := NewAppSettingsUsecase(repo, NewAuditUsecase(&fakeAuditRepo{}, logging.NewNoop()), pub, false, logging.NewNoop())

	err := uc.Update(context.Background(), Actor{UserID: "u"}, &domain.AppSettings{
		Notifications: domain.NotificationsSettings{Telegram: domain.TelegramSettings{
			Enabled: new(true), ChatID: new("-100123"), BotToken: new("tok"), Cron: new("*/15 * * * *"),
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, repo.lastSaved)
	assert.Equal(t, []string{"notifications"}, pub.sections)
}

// TestAppSettings_VersionOverride_GatedInProd (§34.3): при выключенном
// allowVersionOverride попытка задать version_override отклоняется и не
// сохраняется.
func TestAppSettings_VersionOverride_GatedInProd(t *testing.T) {
	t.Parallel()
	repo := &fakeAppSettingsRepo{current: &domain.AppSettings{}}
	uc := NewAppSettingsUsecase(repo, NewAuditUsecase(&fakeAuditRepo{}, logging.NewNoop()), nil, false, logging.NewNoop())

	err := uc.Update(context.Background(), Actor{UserID: "u"}, &domain.AppSettings{
		General: domain.GeneralSettings{VersionOverride: new("dev-local")},
	})
	assert.ErrorIs(t, err, domain.ErrVersionOverrideForbidden)
	assert.Nil(t, repo.lastSaved, "version override must not persist in prod")
}

// TestAppSettings_VersionOverride_AllowedInDev (§34.3): при включённом гейте
// override сохраняется, секция general попадает в changed_sections.
func TestAppSettings_VersionOverride_AllowedInDev(t *testing.T) {
	t.Parallel()
	repo := &fakeAppSettingsRepo{current: &domain.AppSettings{}}
	pub := &fakeReloadPublisher{}
	uc := NewAppSettingsUsecase(repo, NewAuditUsecase(&fakeAuditRepo{}, logging.NewNoop()), pub, true, logging.NewNoop())

	err := uc.Update(context.Background(), Actor{UserID: "u"}, &domain.AppSettings{
		General: domain.GeneralSettings{VersionOverride: new("dev-local")},
	})
	require.NoError(t, err)
	require.NotNil(t, repo.lastSaved)
	require.NotNil(t, repo.lastSaved.General.VersionOverride)
	assert.Equal(t, "dev-local", *repo.lastSaved.General.VersionOverride)
	assert.Equal(t, []string{"general"}, pub.sections)
}

func TestChangedSections_VersionOverride(t *testing.T) {
	t.Parallel()
	p := &domain.AppSettings{General: domain.GeneralSettings{VersionOverride: new("x")}}
	assert.Equal(t, []string{"general"}, changedSections(p))
}

// TestAppSettings_SessionTTL_MergeAndSection (§34.2): валидный TTL сохраняется,
// секция security попадает в changed_sections.
func TestAppSettings_SessionTTL_MergeAndSection(t *testing.T) {
	t.Parallel()
	repo := &fakeAppSettingsRepo{current: &domain.AppSettings{}}
	pub := &fakeReloadPublisher{}
	uc := NewAppSettingsUsecase(repo, NewAuditUsecase(&fakeAuditRepo{}, logging.NewNoop()), pub, false, logging.NewNoop())

	ttl := 7200
	err := uc.Update(context.Background(), Actor{UserID: "u"}, &domain.AppSettings{
		Security: domain.SecuritySettings{SessionTTLSeconds: &ttl},
	})
	require.NoError(t, err)
	require.NotNil(t, repo.lastSaved)
	require.NotNil(t, repo.lastSaved.Security.SessionTTLSeconds)
	assert.Equal(t, 7200, *repo.lastSaved.Security.SessionTTLSeconds)
	assert.Equal(t, []string{"security"}, pub.sections)
}

// TestAppSettings_SessionTTL_Invalid (§34.2): TTL вне диапазона отклоняется.
func TestAppSettings_SessionTTL_Invalid(t *testing.T) {
	t.Parallel()
	repo := &fakeAppSettingsRepo{current: &domain.AppSettings{}}
	uc := NewAppSettingsUsecase(repo, NewAuditUsecase(&fakeAuditRepo{}, logging.NewNoop()), nil, false, logging.NewNoop())

	tooSmall := 5
	err := uc.Update(context.Background(), Actor{UserID: "u"}, &domain.AppSettings{
		Security: domain.SecuritySettings{SessionTTLSeconds: &tooSmall},
	})
	assert.ErrorIs(t, err, domain.ErrSessionTTLInvalid)
	assert.Nil(t, repo.lastSaved, "invalid TTL must not persist")
}

func TestChangedSections_Security(t *testing.T) {
	t.Parallel()
	ttl := 3600
	p := &domain.AppSettings{Security: domain.SecuritySettings{SessionTTLSeconds: &ttl}}
	assert.Equal(t, []string{"security"}, changedSections(p))
}

// TestMergeAppSettings_Logging (§51): nil сохраняет current, значение
// перекрывает, соседние секции не тронуты.
func TestMergeAppSettings_Logging(t *testing.T) {
	t.Parallel()
	current := &domain.AppSettings{
		Logging: domain.LoggingSettings{Level: new(4)},
		Sentry:  domain.SentrySettings{Environment: new("prod")},
	}

	out := mergeAppSettings(current, &domain.AppSettings{})
	require.NotNil(t, out.Logging.Level)
	assert.Equal(t, 4, *out.Logging.Level, "nil patch must keep current level")

	out = mergeAppSettings(current, &domain.AppSettings{
		Logging: domain.LoggingSettings{Level: new(2)},
	})
	require.NotNil(t, out.Logging.Level)
	assert.Equal(t, 2, *out.Logging.Level)
	require.NotNil(t, out.Sentry.Environment)
	assert.Equal(t, "prod", *out.Sentry.Environment, "neighbour sections untouched")
}

func TestChangedSections_Logging(t *testing.T) {
	t.Parallel()
	p := &domain.AppSettings{Logging: domain.LoggingSettings{Level: new(5)}}
	assert.Equal(t, []string{"logging"}, changedSections(p))

	mixed := &domain.AppSettings{
		Logging:  domain.LoggingSettings{Level: new(5)},
		Security: domain.SecuritySettings{SessionTTLSeconds: new(3600)},
	}
	assert.Equal(t, []string{"security", "logging"}, changedSections(mixed))
}

// TestAppSettings_LogLevel_MergeAndPublish (§51): валидный уровень сохраняется,
// publisher получает секцию "logging" (имя = reloader.SectionLogging).
func TestAppSettings_LogLevel_MergeAndPublish(t *testing.T) {
	t.Parallel()
	repo := &fakeAppSettingsRepo{current: &domain.AppSettings{}}
	pub := &fakeReloadPublisher{}
	uc := NewAppSettingsUsecase(repo, NewAuditUsecase(&fakeAuditRepo{}, logging.NewNoop()), pub, false, logging.NewNoop())

	err := uc.Update(context.Background(), Actor{UserID: "u"}, &domain.AppSettings{
		Logging: domain.LoggingSettings{Level: new(2)},
	})
	require.NoError(t, err)
	require.NotNil(t, repo.lastSaved)
	require.NotNil(t, repo.lastSaved.Logging.Level)
	assert.Equal(t, 2, *repo.lastSaved.Logging.Level)
	assert.Equal(t, []string{string(reloader.SectionLogging)}, pub.sections)
}

// TestAppSettings_LogLevel_Invalid (§51): уровень вне 2..5 отклоняется,
// ничего не сохраняется и не публикуется.
func TestAppSettings_LogLevel_Invalid(t *testing.T) {
	t.Parallel()
	for _, lvl := range []int{0, 1, 6, -3} {
		repo := &fakeAppSettingsRepo{current: &domain.AppSettings{}}
		pub := &fakeReloadPublisher{}
		uc := NewAppSettingsUsecase(repo, NewAuditUsecase(&fakeAuditRepo{}, logging.NewNoop()), pub, false, logging.NewNoop())

		err := uc.Update(context.Background(), Actor{UserID: "u"}, &domain.AppSettings{
			Logging: domain.LoggingSettings{Level: &lvl},
		})
		assert.ErrorIs(t, err, domain.ErrLogLevelInvalid, "level %d", lvl)
		assert.Nil(t, repo.lastSaved, "invalid level must not persist")
		assert.Empty(t, pub.sections, "invalid level must not publish")
	}
}

// ---- fakes ----

type fakeAppSettingsRepo struct {
	mu        sync.Mutex
	current   *domain.AppSettings
	lastSaved *domain.AppSettings
	getErr    error // если задан — Get возвращает эту ошибку (для error-path тестов)
}

func (f *fakeAppSettingsRepo) Get(_ context.Context) (*domain.AppSettings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.current == nil {
		return &domain.AppSettings{}, nil
	}
	cpy := *f.current
	return &cpy, nil
}

func (f *fakeAppSettingsRepo) Update(_ context.Context, s *domain.AppSettings) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cpy := *s
	f.lastSaved = &cpy
	f.current = &cpy
	return nil
}

type fakeAuditRepo struct {
	mu      sync.Mutex
	written []*domain.AuditEntry
}

func (f *fakeAuditRepo) Write(_ context.Context, e *domain.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	// нормализуем []string в []any как делает JSON-сериализация details,
	// чтобы тест мог сравнивать через any-slice.
	if v, ok := e.Details["changed_sections"].([]string); ok {
		anySlice := make([]any, len(v))
		for i := range v {
			anySlice[i] = v[i]
		}
		e.Details["changed_sections"] = anySlice
	}
	f.written = append(f.written, e)
	return nil
}

func (f *fakeAuditRepo) List(_ context.Context, _ port.AuditFilter) ([]*domain.AuditEntry, error) {
	return f.written, nil
}

func (f *fakeAuditRepo) Count(_ context.Context, _ port.AuditFilter) (int, error) {
	return len(f.written), nil
}

func (f *fakeAuditRepo) DeleteOlderThan(_ context.Context, _ time.Time) (int, error) {
	return 0, nil
}

type fakeReloadPublisher struct {
	mu       sync.Mutex
	sections []string
}

func (f *fakeReloadPublisher) Publish(_ context.Context, s reloader.Section) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sections = append(f.sections, string(s))
	return nil
}
