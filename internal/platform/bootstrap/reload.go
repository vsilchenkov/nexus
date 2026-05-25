package bootstrap

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"bus/internal/platform/config"
	"bus/internal/platform/logging"
	"bus/internal/platform/reloader"
	sentrypf "bus/internal/platform/sentry"
)

// SentryReloader возвращает Reloader, который читает свежий overlay из
// app_settings, накладывает на cfg.Sentry и переинициализирует Sentry SDK.
//
// Безопасно вызывать из горутины — sentry-go thread-safe, и Init
// internally заменяет глобальный hub.
func SentryReloader(pool *pgxpool.Pool, cfg *config.Config, projectName, version string, logger logging.Logger) reloader.Reloader {
	return func(ctx context.Context) error {
		o, err := readAppSettings(ctx, pool)
		if err != nil {
			return err
		}
		overlaySentry(cfg, o)
		if err := sentrypf.Reload(&cfg.Sentry, projectName, version); err != nil {
			return err
		}
		logger.Info("sentry reloaded from app_settings",
			logger.Str("env", cfg.Sentry.Environment),
			logger.Any("use", cfg.Sentry.Use))
		return nil
	}
}

// ClickHouseOverlayReloader возвращает Reloader, который перечитывает
// CH overlay в cfg (без пересоздания клиента). Это упрощённый вариант
// hot-reload: фактическое пересоздание chConn / chlog.Writer требует
// глубокой переделки sender pipeline и будет реализовано в Phase 6.3.2.5.
//
// Сейчас reloader полезен для аудита и метрики — оператор видит «settings
// applied», и при следующем рестарте CH-клиент подхватит новые значения.
func ClickHouseOverlayReloader(pool *pgxpool.Pool, cfg *config.Config, logger logging.Logger) reloader.Reloader {
	return func(ctx context.Context) error {
		o, err := readAppSettings(ctx, pool)
		if err != nil {
			return err
		}
		overlayClickHouse(cfg, o)
		logger.Info("clickhouse settings overlay refreshed; full reconnect deferred to restart",
			logger.Str("host", cfg.ClickHouse.Host))
		return nil
	}
}
