//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	pgrepo "nexus/internal/web/adapter/out/postgres"
)

// TestAppSettingsRepo_NotificationsRoundTrip_E2E (§20, Phase F2.1): защита от
// «молчаливой потери» — AppSettingsRepoPg.Update должен сериализовать секцию
// notifications, а Get — прочитать её обратно. Если Update снова начнёт
// marshal'ить только {sentry, clickhouse}, тест упадёт.
func TestAppSettingsRepo_NotificationsRoundTrip_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	repo := pgrepo.NewAppSettingsRepoPg(pool, logging.NewNoop())

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

	repo := pgrepo.NewAppSettingsRepoPg(pool, logging.NewNoop())

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

	repo := pgrepo.NewAppSettingsRepoPg(pool, logging.NewNoop())

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
