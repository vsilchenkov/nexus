package usecase

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
)

// TestAppSettings_PrepareImport — §110.7: поле, не прошедшее проверку на цели,
// сбрасывается с предупреждением, остальное переносится; секция clickhouse и
// version_override не переносятся никогда; в хранилище ничего не пишется.
func TestAppSettings_PrepareImport(t *testing.T) {
	t.Parallel()

	const bodyCap = 10 << 20
	tests := []struct {
		name        string
		patch       func() *domain.AppSettings
		wantSkipped []string
		check       func(t *testing.T, merged *domain.AppSettings, sections []string)
	}{
		{
			name: "всё валидно — переносится целиком, clickhouse отброшен",
			patch: func() *domain.AppSettings {
				return &domain.AppSettings{
					Sentry:     domain.SentrySettings{Environment: new("prod")},
					ClickHouse: domain.ClickHouseSettings{Host: new("ch.old"), Password: new("pw")},
				}
			},
			check: func(t *testing.T, merged *domain.AppSettings, sections []string) {
				assert.Equal(t, "prod", *merged.Sentry.Environment)
				assert.Nil(t, merged.ClickHouse.Host, "адрес ClickHouse из файла не переносится")
				assert.Equal(t, []string{"sentry"}, sections)
			},
		},
		{
			name: "лимит тела выше потолка цели — поле сброшено, остальное перенесено",
			patch: func() *domain.AppSettings {
				return &domain.AppSettings{General: domain.GeneralSettings{
					MaxBodyBytes:     new(bodyCap * 2),
					MetricsRefetchMs: new(15000),
				}}
			},
			wantSkipped: []string{"general.max_body_bytes"},
			check: func(t *testing.T, merged *domain.AppSettings, _ []string) {
				assert.Nil(t, merged.General.MaxBodyBytes)
				require.NotNil(t, merged.General.MetricsRefetchMs)
				assert.Equal(t, 15000, *merged.General.MetricsRefetchMs)
			},
		},
		{
			name: "version_override на боевой ноде — сброшен",
			patch: func() *domain.AppSettings {
				return &domain.AppSettings{General: domain.GeneralSettings{VersionOverride: new("v9")}}
			},
			wantSkipped: []string{"general.version_override"},
			check: func(t *testing.T, merged *domain.AppSettings, sections []string) {
				assert.Nil(t, merged.General.VersionOverride)
				assert.Empty(t, sections)
			},
		},
		{
			name: "битый cron Telegram — секция уведомлений сброшена целиком",
			patch: func() *domain.AppSettings {
				return &domain.AppSettings{Notifications: domain.NotificationsSettings{
					Telegram: domain.TelegramSettings{Cron: new("не cron"), ChatID: new("42")},
				}}
			},
			wantSkipped: []string{"notifications.telegram"},
			check: func(t *testing.T, merged *domain.AppSettings, _ []string) {
				assert.Nil(t, merged.Notifications.Telegram.ChatID)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := &fakeAppSettingsRepo{current: &domain.AppSettings{}}
			uc := NewAppSettingsUsecase(repo, NewAuditUsecase(&fakeAuditRepo{}, logging.NewNoop()),
				nil, false, bodyCap, bodyCap, logging.NewNoop())

			merged, sections, skipped, err := uc.PrepareImport(context.Background(), tt.patch())
			require.NoError(t, err)

			var fields []string
			for _, s := range skipped {
				fields = append(fields, s.Field)
				assert.Error(t, s.Err)
			}
			assert.Equal(t, tt.wantSkipped, fields)
			tt.check(t, merged, sections)
			assert.Nil(t, repo.lastSaved, "PrepareImport ничего не записывает")
		})
	}
}

// TestAppSettings_UpdateRejectsWhatImportSkips — регресс рефакторинга: те же
// проверки, что импорт превращает в пропуск, форма «Настройки» по-прежнему
// превращает в отказ.
func TestAppSettings_UpdateRejectsWhatImportSkips(t *testing.T) {
	t.Parallel()
	repo := &fakeAppSettingsRepo{current: &domain.AppSettings{}}
	uc := NewAppSettingsUsecase(repo, NewAuditUsecase(&fakeAuditRepo{}, logging.NewNoop()),
		nil, false, 1<<20, 1<<20, logging.NewNoop())

	err := uc.Update(context.Background(), Actor{UserID: "u"}, &domain.AppSettings{
		General: domain.GeneralSettings{MaxBodyBytes: new(2 << 20)},
	})
	require.Error(t, err)
	assert.Nil(t, repo.lastSaved)
}
