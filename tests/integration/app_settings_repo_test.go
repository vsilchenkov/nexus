//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
	pgrepo "nexus/internal/web/adapter/out/postgres"
)

// mustTestCipher — общий шифр для integration-тестов app_settings (§90.1).
func mustTestCipher(t *testing.T) *crypto.Cipher {
	t.Helper()
	c, err := crypto.NewCipher(testEncryptionKey)
	require.NoError(t, err)
	return c
}

// TestAppSettingsRepo_NotificationsRoundTrip_E2E (§20, Phase F2.1): защита от
// «молчаливой потери» — AppSettingsRepoPg.Update должен сериализовать секцию
// notifications, а Get — прочитать её обратно. Если Update снова начнёт
// marshal'ить только {sentry, clickhouse}, тест упадёт.
func TestAppSettingsRepo_NotificationsRoundTrip_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	repo := pgrepo.NewAppSettingsRepoPg(pool, mustTestCipher(t), logging.NewNoop())

	enabled := true
	chatID := "-1001234567890"
	token := "123456:bot-token"
	cronExpr := "*/15 * * * *"
	in := &domain.AppSettings{
		Notifications: domain.NotificationsSettings{Telegram: domain.TelegramSettings{
			Enabled: &enabled, ChatID: &chatID, BotToken: &token, Cron: &cronExpr,
		}},
	}
	require.NoError(t, repo.Update(ctx, in))

	got, err := repo.Get(ctx)
	require.NoError(t, err)
	tg := got.Notifications.Telegram
	require.NotNil(t, tg.Enabled)
	require.True(t, *tg.Enabled)
	require.NotNil(t, tg.ChatID)
	require.Equal(t, chatID, *tg.ChatID)
	require.NotNil(t, tg.BotToken)
	require.Equal(t, token, *tg.BotToken)
	require.NotNil(t, tg.Cron)
	require.Equal(t, cronExpr, *tg.Cron)
}

// TestAppSettingsRepo_GeneralRoundTrip_E2E (§28, QA-2026-02 / П8, П13): защита от
// регрессии — AppSettingsRepoPg.Update обязан сериализовать секцию general
// (public_base_url). Раньше struct в Update пропускал General, поэтому
// сохранённый публичный адрес «терялся», а Get возвращал general:{}.
func TestAppSettingsRepo_GeneralRoundTrip_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	repo := pgrepo.NewAppSettingsRepoPg(pool, mustTestCipher(t), logging.NewNoop())

	publicURL := "https://nexus.example.com"
	in := &domain.AppSettings{
		General: domain.GeneralSettings{PublicBaseURL: &publicURL},
	}
	require.NoError(t, repo.Update(ctx, in))

	got, err := repo.Get(ctx)
	require.NoError(t, err)
	require.NotNil(t, got.General.PublicBaseURL, "public_base_url должен сохраниться")
	require.Equal(t, publicURL, *got.General.PublicBaseURL)
}

// TestAppSettingsRepo_SecurityRoundTrip_E2E (§34.2): защита от той же регрессии
// для секции security — Update обязан сериализовать session_ttl_seconds, иначе
// Get вернёт security:{} (баг, пойманный на живом стенде: struct в Update
// пропускал Security).
func TestAppSettingsRepo_SecurityRoundTrip_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	repo := pgrepo.NewAppSettingsRepoPg(pool, mustTestCipher(t), logging.NewNoop())

	ttl := 600
	in := &domain.AppSettings{
		Security: domain.SecuritySettings{SessionTTLSeconds: &ttl},
	}
	require.NoError(t, repo.Update(ctx, in))

	got, err := repo.Get(ctx)
	require.NoError(t, err)
	require.NotNil(t, got.Security.SessionTTLSeconds, "session_ttl_seconds должен сохраниться")
	require.Equal(t, 600, *got.Security.SessionTTLSeconds)
}

// TestAppSettingsRepo_LoggingRoundTrip_E2E (§51): та же регрессия для секции
// logging — Update обязан сериализовать logging.level (баг реально пойман
// сценарием TestServiceLogs_ReloadLevelAcrossServices: struct в Update
// пропускал Logging → уровень «сохранялся», но терялся при записи).
func TestAppSettingsRepo_LoggingRoundTrip_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	repo := pgrepo.NewAppSettingsRepoPg(pool, mustTestCipher(t), logging.NewNoop())

	level := 5
	in := &domain.AppSettings{
		Logging: domain.LoggingSettings{Level: &level},
	}
	require.NoError(t, repo.Update(ctx, in))

	got, err := repo.Get(ctx)
	require.NoError(t, err)
	require.NotNil(t, got.Logging.Level, "logging.level должен сохраниться")
	require.Equal(t, 5, *got.Logging.Level)
}

// TestAppSettingsRepo_MailRoundTrip_E2E (§88.2): та же регрессия для секции
// mail. Пропуск секции в анонимной структуре Update означал бы, что настройки
// релея «сохраняются» в UI и молча исчезают из БД — а обнаружилось бы это
// только неотправленным письмом восстановления пароля.
//
// Проверяются все поля секции: забытое поле теряется ровно так же, как
// забытая секция.
func TestAppSettingsRepo_MailRoundTrip_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	repo := pgrepo.NewAppSettingsRepoPg(pool, mustTestCipher(t), logging.NewNoop())

	in := &domain.AppSettings{Mail: domain.MailSettings{
		Enabled:              new(true),
		Host:                 new("smtp.example.com"),
		Port:                 new(465),
		Encryption:           new(domain.MailEncryptionTLS),
		AuthType:             new(domain.MailAuthLogin),
		Username:             new("noreply@example.com"),
		Password:             new("smtp-secret"),
		FromAddress:          new("nexus@example.com"),
		FromName:             new("Шина"),
		HELOHost:             new("nexus.example.com"),
		TimeoutSec:           new(42),
		SkipTLSVerify:        new(true),
		PasswordResetEnabled: new(true),
		PasswordResetTTLMin:  new(30),
	}}
	require.NoError(t, repo.Update(ctx, in))

	got, err := repo.Get(ctx)
	require.NoError(t, err)

	m := got.Mail.Resolve()
	require.True(t, m.Enabled, "секция mail должна пережить сохранение")
	require.Equal(t, "smtp.example.com", m.Host)
	require.Equal(t, 465, m.Port)
	require.Equal(t, domain.MailEncryptionTLS, m.Encryption)
	require.Equal(t, domain.MailAuthLogin, m.AuthType)
	require.Equal(t, "noreply@example.com", m.Username)
	require.Equal(t, "smtp-secret", m.Password, "репозиторий отдаёт пароль как есть; маскирует usecase")
	require.Equal(t, "nexus@example.com", m.FromAddress)
	require.Equal(t, "Шина", m.FromName)
	require.Equal(t, "nexus.example.com", m.HELOHost)
	require.Equal(t, 42, m.TimeoutSec)
	require.True(t, m.SkipTLSVerify)
	require.True(t, m.PasswordResetEnabled)
	require.Equal(t, 30, m.PasswordResetTTLMin)
}

// §90.1: секреты обязаны лежать в JSONB зашифрованными. Проверка идёт по сырому
// значению колонки — unit-тесты хелперов этого не видят, а именно дамп БД и был
// тем каналом утечки, ради которого всё делалось.
func TestAppSettingsRepo_SecretsEncryptedAtRest_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	repo := pgrepo.NewAppSettingsRepoPg(pool, mustTestCipher(t), logging.NewNoop())

	const (
		dsn      = "https://key@sentry.example.com/52"
		chPwd    = "clickhouse-secret"
		mailPwd  = "smtp-secret"
		botToken = "123456:bot-token"
	)
	in := &domain.AppSettings{
		Sentry:        domain.SentrySettings{DSN: new(dsn)},
		ClickHouse:    domain.ClickHouseSettings{Password: new(chPwd), Host: new("ch1")},
		Mail:          domain.MailSettings{Password: new(mailPwd)},
		Notifications: domain.NotificationsSettings{Telegram: domain.TelegramSettings{BotToken: new(botToken)}},
	}
	require.NoError(t, repo.Update(ctx, in))

	var raw string
	require.NoError(t, pool.QueryRow(ctx, "SELECT value::text FROM app_settings WHERE id = 1").Scan(&raw))
	for _, secret := range []string{dsn, chPwd, mailPwd, botToken} {
		assert.NotContains(t, raw, secret, "секрет не должен читаться в дампе БД")
	}
	assert.Contains(t, raw, "v1:", "секреты хранятся в формате v1:nonce:ct:tag")
	assert.Contains(t, raw, "ch1", "несекретные поля остаются открытыми")

	// Наружу репозиторий отдаёт plaintext — usecase и overlay работают как раньше.
	got, err := repo.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, dsn, *got.Sentry.DSN)
	require.Equal(t, chPwd, *got.ClickHouse.Password)
	require.Equal(t, mailPwd, *got.Mail.Password)
	require.Equal(t, botToken, *got.Notifications.Telegram.BotToken)
}

// §90.1: до этой фичи секреты лежали открытым текстом, миграции данных нет.
// Такие значения обязаны читаться как есть и дозревать при первом сохранении.
func TestAppSettingsRepo_LegacyPlaintextLazyUpgrade_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	repo := pgrepo.NewAppSettingsRepoPg(pool, mustTestCipher(t), logging.NewNoop())

	// Пишем мимо репозитория — ровно так выглядит боевая строка до обновления.
	const legacyDSN = "https://legacy@sentry.example.com/1"
	_, err := pool.Exec(ctx,
		`UPDATE app_settings SET value = $1::jsonb WHERE id = 1`,
		`{"sentry":{"dsn":"`+legacyDSN+`"},"clickhouse":{"password":"legacy-ch"}}`)
	require.NoError(t, err)

	got, err := repo.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, legacyDSN, *got.Sentry.DSN, "старое значение читается без ошибки")
	require.Equal(t, "legacy-ch", *got.ClickHouse.Password)

	// Первое же сохранение переводит значения в шифротекст.
	require.NoError(t, repo.Update(ctx, got))

	var raw string
	require.NoError(t, pool.QueryRow(ctx, "SELECT value::text FROM app_settings WHERE id = 1").Scan(&raw))
	assert.NotContains(t, raw, legacyDSN)
	assert.Equal(t, 2, strings.Count(raw, "v1:"), "оба секрета дозрели")

	after, err := repo.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, legacyDSN, *after.Sentry.DSN)
	require.Equal(t, "legacy-ch", *after.ClickHouse.Password)
}
