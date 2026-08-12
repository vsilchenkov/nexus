package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/reloader"
)

// Пароль релея не должен выходить наружу даже админу (§88.2).
func TestAppSettings_GetMasksMailPassword(t *testing.T) {
	t.Parallel()
	repo := &fakeAppSettingsRepo{current: &domain.AppSettings{
		Mail: domain.MailSettings{
			Host:     new("smtp.example.com"),
			Username: new("noreply@example.com"),
			Password: new("real-smtp-password"),
		},
	}}
	uc := NewAppSettingsUsecase(repo, NewAuditUsecase(&fakeAuditRepo{}, logging.NewNoop()), nil, false, logging.NewNoop())

	got, err := uc.Get(context.Background())
	require.NoError(t, err)

	require.NotNil(t, got.Mail.Password)
	assert.Equal(t, "***", *got.Mail.Password)
	// Логин — не секрет (как clickhouse.user), маскироваться не должен.
	require.NotNil(t, got.Mail.Username)
	assert.Equal(t, "noreply@example.com", *got.Mail.Username)
}

// Правка соседнего поля не должна стирать сохранённый пароль: форма
// присылает обратно маску "***", и merge обязан её игнорировать (§88.2).
func TestMergeAppSettings_MaskedMailPasswordDoesNotOverwrite(t *testing.T) {
	t.Parallel()
	current := &domain.AppSettings{Mail: domain.MailSettings{Password: new("real-smtp-password")}}
	patch := &domain.AppSettings{Mail: domain.MailSettings{
		Password: new("***"),
		Host:     new("smtp2.example.com"),
	}}

	out := mergeAppSettings(current, patch)

	require.NotNil(t, out.Mail.Password)
	assert.Equal(t, "real-smtp-password", *out.Mail.Password)
	require.NotNil(t, out.Mail.Host)
	assert.Equal(t, "smtp2.example.com", *out.Mail.Host)
}

func TestMergeAppSettings_NewMailPasswordOverwrites(t *testing.T) {
	t.Parallel()
	current := &domain.AppSettings{Mail: domain.MailSettings{Password: new("old")}}
	patch := &domain.AppSettings{Mail: domain.MailSettings{Password: new("brand-new")}}

	out := mergeAppSettings(current, patch)

	require.NotNil(t, out.Mail.Password)
	assert.Equal(t, "brand-new", *out.Mail.Password)
}

// nil-поля патча не затирают сохранённые значения — общий инвариант
// AppSettings, проверяем его и для новой секции.
func TestMergeAppSettings_MailPreservesCurrentWhenPatchNil(t *testing.T) {
	t.Parallel()
	current := &domain.AppSettings{Mail: domain.MailSettings{
		Enabled:              new(true),
		Host:                 new("smtp.example.com"),
		Port:                 new(465),
		Encryption:           new(domain.MailEncryptionTLS),
		FromAddress:          new("nexus@example.com"),
		PasswordResetEnabled: new(true),
		PasswordResetTTLMin:  new(30),
	}}

	out := mergeAppSettings(current, &domain.AppSettings{})

	got := out.Mail.Resolve()
	assert.True(t, got.Enabled)
	assert.Equal(t, "smtp.example.com", got.Host)
	assert.Equal(t, 465, got.Port)
	assert.Equal(t, domain.MailEncryptionTLS, got.Encryption)
	assert.True(t, got.PasswordResetEnabled)
	assert.Equal(t, 30, got.PasswordResetTTLMin)
}

func TestMergeAppSettings_MailAppliesEveryField(t *testing.T) {
	t.Parallel()
	patch := &domain.AppSettings{Mail: domain.MailSettings{
		Enabled:              new(true),
		Host:                 new("smtp.example.com"),
		Port:                 new(2525),
		Encryption:           new(domain.MailEncryptionNone),
		AuthType:             new(domain.MailAuthNone),
		Username:             new("u"),
		FromAddress:          new("nexus@example.com"),
		FromName:             new("Шина"),
		HELOHost:             new("nexus.example.com"),
		TimeoutSec:           new(45),
		SkipTLSVerify:        new(true),
		PasswordResetEnabled: new(true),
		PasswordResetTTLMin:  new(120),
	}}

	got := mergeAppSettings(&domain.AppSettings{}, patch).Mail.Resolve()

	assert.True(t, got.Enabled)
	assert.Equal(t, "smtp.example.com", got.Host)
	assert.Equal(t, 2525, got.Port)
	assert.Equal(t, domain.MailEncryptionNone, got.Encryption)
	assert.Equal(t, domain.MailAuthNone, got.AuthType)
	assert.Equal(t, "u", got.Username)
	assert.Equal(t, "nexus@example.com", got.FromAddress)
	assert.Equal(t, "Шина", got.FromName)
	assert.Equal(t, "nexus.example.com", got.HELOHost)
	assert.Equal(t, 45, got.TimeoutSec)
	assert.True(t, got.SkipTLSVerify)
	assert.True(t, got.PasswordResetEnabled)
	assert.Equal(t, 120, got.PasswordResetTTLMin)
}

// Имя секции уходит в reloader.Section кастом строки без маппинга — оно
// обязано совпадать с reloader.SectionMail (§88.2). Сравниваем именно с
// константой: литерал "mail" разошёлся бы с ней незаметно.
func TestChangedSections_Mail(t *testing.T) {
	t.Parallel()

	want := []string{string(reloader.SectionMail)}
	assert.Equal(t, want, changedSections(&domain.AppSettings{
		Mail: domain.MailSettings{Enabled: new(true)},
	}))
	assert.Equal(t, want, changedSections(&domain.AppSettings{
		Mail: domain.MailSettings{PasswordResetTTLMin: new(30)},
	}))
	assert.Empty(t, changedSections(&domain.AppSettings{}))
}

// Сохранение секции обязано публиковать reload-событие: без него признак
// доступности восстановления не обновится на других репликах (§88.2).
// Разворачивание SectionAll проверяется в пакете reloader.
func TestAppSettings_Update_MailPublishesReload(t *testing.T) {
	t.Parallel()

	pub := &fakeReloadPublisher{}
	uc := NewAppSettingsUsecase(
		&fakeAppSettingsRepo{current: &domain.AppSettings{
			Mail: domain.MailSettings{Host: new("smtp.example.com"), FromAddress: new("nexus@example.com")},
		}},
		NewAuditUsecase(&fakeAuditRepo{}, logging.NewNoop()), pub, false, logging.NewNoop())

	require.NoError(t, uc.Update(context.Background(), Actor{UserID: "u"}, &domain.AppSettings{
		Mail: domain.MailSettings{Enabled: new(true)},
	}))
	require.Equal(t, []string{string(reloader.SectionMail)}, pub.sections)
}

func TestAppSettings_Update_MailValidationRejects(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		patch   domain.MailSettings
		wantErr error
	}{
		{"хост со схемой", domain.MailSettings{Host: new("smtp://x")}, domain.ErrMailHostInvalid},
		{"порт вне диапазона", domain.MailSettings{Port: new(0)}, domain.ErrMailPortInvalid},
		{"неизвестное шифрование", domain.MailSettings{Encryption: new("ssl")}, domain.ErrMailEncryptionInvalid},
		{"неизвестный auth", domain.MailSettings{AuthType: new("oauth2")}, domain.ErrMailAuthTypeInvalid},
		{"битый from", domain.MailSettings{FromAddress: new("not-an-email")}, domain.ErrMailFromInvalid},
		{"CRLF в from_name", domain.MailSettings{FromName: new("N\r\nBcc: e@x")}, domain.ErrMailFromNameInvalid},
		{"таймаут вне диапазона", domain.MailSettings{TimeoutSec: new(500)}, domain.ErrMailTimeoutInvalid},
		{"ttl вне диапазона", domain.MailSettings{PasswordResetTTLMin: new(1)}, domain.ErrPasswordResetTTLInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := &fakeAppSettingsRepo{current: &domain.AppSettings{}}
			uc := NewAppSettingsUsecase(repo, NewAuditUsecase(&fakeAuditRepo{}, logging.NewNoop()), nil, false, logging.NewNoop())

			err := uc.Update(context.Background(), Actor{UserID: "u"}, &domain.AppSettings{Mail: tt.patch})

			require.ErrorIs(t, err, tt.wantErr)
			assert.Nil(t, repo.lastSaved, "негодная секция не должна сохраняться")
		})
	}
}

// Межполевые правила считаются по СМЕРЖЕННОЙ секции: патч приносит только
// флаг enabled, а хост и отправитель уже лежат в сохранённых настройках.
func TestAppSettings_Update_MailConsistencyUsesMergedValue(t *testing.T) {
	t.Parallel()

	t.Run("хост из сохранённых — включение проходит", func(t *testing.T) {
		t.Parallel()
		repo := &fakeAppSettingsRepo{current: &domain.AppSettings{Mail: domain.MailSettings{
			Host:        new("smtp.example.com"),
			FromAddress: new("nexus@example.com"),
		}}}
		pub := &fakeReloadPublisher{}
		uc := NewAppSettingsUsecase(repo, NewAuditUsecase(&fakeAuditRepo{}, logging.NewNoop()), pub, false, logging.NewNoop())

		err := uc.Update(context.Background(), Actor{UserID: "u"}, &domain.AppSettings{
			Mail: domain.MailSettings{Enabled: new(true)},
		})

		require.NoError(t, err)
		require.NotNil(t, repo.lastSaved)
		assert.Equal(t, []string{"mail"}, pub.sections)
	})

	t.Run("включение без хоста отклоняется", func(t *testing.T) {
		t.Parallel()
		repo := &fakeAppSettingsRepo{current: &domain.AppSettings{}}
		uc := NewAppSettingsUsecase(repo, NewAuditUsecase(&fakeAuditRepo{}, logging.NewNoop()), nil, false, logging.NewNoop())

		err := uc.Update(context.Background(), Actor{UserID: "u"}, &domain.AppSettings{
			Mail: domain.MailSettings{Enabled: new(true), FromAddress: new("nexus@example.com")},
		})

		require.ErrorIs(t, err, domain.ErrMailHostInvalid)
		assert.Nil(t, repo.lastSaved)
	})

	t.Run("восстановление без включённой почты отклоняется", func(t *testing.T) {
		t.Parallel()
		repo := &fakeAppSettingsRepo{current: &domain.AppSettings{}}
		uc := NewAppSettingsUsecase(repo, NewAuditUsecase(&fakeAuditRepo{}, logging.NewNoop()), nil, false, logging.NewNoop())

		err := uc.Update(context.Background(), Actor{UserID: "u"}, &domain.AppSettings{
			Mail: domain.MailSettings{PasswordResetEnabled: new(true)},
		})

		require.ErrorIs(t, err, domain.ErrMailResetRequiresMail)
		assert.Nil(t, repo.lastSaved)
	})
}

func TestMailAvailabilityProvider(t *testing.T) {
	t.Parallel()

	ready := &domain.AppSettings{
		General: domain.GeneralSettings{PublicBaseURL: new("https://nexus.example.com")},
		Mail: domain.MailSettings{
			Enabled:              new(true),
			Host:                 new("smtp.example.com"),
			FromAddress:          new("nexus@example.com"),
			PasswordResetEnabled: new(true),
		},
	}

	t.Run("до Refresh — false (fail-closed)", func(t *testing.T) {
		t.Parallel()
		p := NewMailAvailabilityProvider(&fakeAppSettingsRepo{current: ready}, logging.NewNoop())
		assert.False(t, p.Get(), "ссылка на форме входа не должна появляться до первого чтения")
	})

	t.Run("nil-провайдер не паникует", func(t *testing.T) {
		t.Parallel()
		var p *MailAvailabilityProvider
		assert.False(t, p.Get())
	})

	t.Run("Refresh включает и выключает признак", func(t *testing.T) {
		t.Parallel()
		repo := &fakeAppSettingsRepo{current: ready}
		p := NewMailAvailabilityProvider(repo, logging.NewNoop())

		require.NoError(t, p.Refresh(context.Background()))
		assert.True(t, p.Get())

		repo.current = &domain.AppSettings{} // почту выключили
		require.NoError(t, p.Refresh(context.Background()))
		assert.False(t, p.Get())
	})

	// Провал одного запроса к БД — не повод убирать со страницы входа
	// работающую функцию.
	t.Run("ошибка чтения не сбрасывает известное значение", func(t *testing.T) {
		t.Parallel()
		repo := &fakeAppSettingsRepo{current: ready}
		p := NewMailAvailabilityProvider(repo, logging.NewNoop())
		require.NoError(t, p.Refresh(context.Background()))
		require.True(t, p.Get())

		repo.getErr = errors.New("db is down")
		require.Error(t, p.Refresh(context.Background()))
		assert.True(t, p.Get(), "признак обязан пережить временный сбой чтения")
	})
}
