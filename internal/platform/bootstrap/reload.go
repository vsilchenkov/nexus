package bootstrap

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"nexus/internal/domain"
	chpf "nexus/internal/platform/clickhouse"
	"nexus/internal/platform/config"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/reloader"
	sentrypf "nexus/internal/platform/sentry"
)

// SentryReloader возвращает Reloader, который читает свежий overlay из
// app_settings, накладывает на cfg.Sentry и переинициализирует Sentry SDK.
//
// Безопасно вызывать из горутины — sentry-go thread-safe, и Init
// internally заменяет глобальный hub.
func SentryReloader(pool *pgxpool.Pool, cfg *config.Config, projectName, version string, cipher *crypto.Cipher, logger logging.Logger) reloader.Reloader {
	return func(ctx context.Context) error {
		o, err := readAppSettings(ctx, pool, cipher, logger)
		if err != nil {
			return err
		}
		overlaySentry(cfg, o)
		// §70.7: instance.id неизменяем после старта — берём из cfg.
		// §93.6: имя реплики тоже неизменяемо (hostname процесса).
		if err := sentrypf.Reload(&cfg.Sentry, sentrypf.Identity{
			Project:  projectName,
			Version:  version,
			Instance: cfg.Instance.ID,
			Replica:  ReplicaName(),
		}); err != nil {
			return err
		}
		logger.Info("sentry reloaded from app_settings",
			logger.Str("env", cfg.Sentry.Environment),
			logger.Any("use", cfg.Sentry.Use))
		return nil
	}
}

// ClickHouseOverlayReloader возвращает Reloader, который только
// перечитывает CH overlay в cfg, без пересоздания клиента. Используется
// в инстансах, у которых нет ClickHouse-клиента (например Receiver),
// — чтобы overlay в их config'е оставался свежим и при добавлении
// CH-зависимости в будущем не было рассинхрона.
func ClickHouseOverlayReloader(pool *pgxpool.Pool, cfg *config.Config, cipher *crypto.Cipher, logger logging.Logger) reloader.Reloader {
	return func(ctx context.Context) error {
		o, err := readAppSettings(ctx, pool, cipher, logger)
		if err != nil {
			return err
		}
		overlayClickHouse(cfg, o)
		logger.Info("clickhouse settings overlay refreshed",
			logger.Str("host", cfg.ClickHouse.Host))
		return nil
	}
}

// LogLevelReloader возвращает Reloader, который перечитывает
// app_settings.logging.level и применяет его к LevelVar контроллера (§51):
// валидный уровень (2..5) → установка; nil/невалидный → откат на YAML-уровень.
// Тот же Reloader используется как сид стартового значения (первый вызов
// сразу после создания подписчика в app.New каждого сервиса).
func LogLevelReloader(pool *pgxpool.Pool, ctl *LogController, cipher *crypto.Cipher, logger logging.Logger) reloader.Reloader {
	return func(ctx context.Context) error {
		o, err := readAppSettings(ctx, pool, cipher, logger)
		if err != nil {
			return err
		}
		applyLogLevelFromOverlay(o, ctl)
		logger.Info("log level applied from app_settings",
			logger.Str("level", ctl.Level.Level().String()))
		return nil
	}
}

// applyLogLevelFromOverlay — чистый применятель уровня: валидное значение
// из overlay → LevelVar; nil или вне 2..5 → откат на YAML-уровень (fallback).
func applyLogLevelFromOverlay(o *appSettingsOverlay, ctl *LogController) {
	if ctl == nil || ctl.Level == nil {
		return
	}
	if o != nil && o.Logging.Level != nil {
		if lvl, ok := intLevel(*o.Logging.Level); ok {
			ctl.Level.Set(lvl)
			return
		}
	}
	ctl.Level.Set(slogLevelFromInt(ctl.fallbackLevel))
}

// RejectLogSwitch — переключатель сбора журнала отказов (§94.5). Реализуется
// rejectlog.Collector; интерфейс объявлен здесь, чтобы bootstrap не зависел от
// пакетов Receiver.
type RejectLogSwitch interface {
	SetEnabled(v bool)
}

// RejectLogReloader возвращает Reloader, который перечитывает срок хранения
// журнала отказов и включает либо выключает сбор (§94.5).
//
// Ноль дней — это «журнал выключен», а не «хранить вечно»: одна ручка отвечает
// и за срок, и за выключение. Значение nil означает «оператор ничего не задал»
// и разворачивается в дефолт домена, то есть сбор идёт.
//
// Тот же Reloader используется как сид стартового значения — первый вызов
// сразу после создания подписчика.
func RejectLogReloader(pool *pgxpool.Pool, sw RejectLogSwitch, cipher *crypto.Cipher, logger logging.Logger) reloader.Reloader {
	return func(ctx context.Context) error {
		o, err := readAppSettings(ctx, pool, cipher, logger)
		if err != nil {
			return err
		}
		var days *int
		if o != nil {
			days = o.General.RejectedRetentionDays
		}
		retention := domain.RejectedRetentionOrDefault(days)
		sw.SetEnabled(retention > 0)
		logger.Info("reject log collection applied from app_settings",
			logger.Int("retention_days", retention))
		return nil
	}
}

// BodyLimitsSwitch — узкий интерфейс, который реализует
// receiver/usecase.BodyLimitsProvider. Объявлен здесь, чтобы bootstrap не
// зависел от пакетов Receiver.
type BodyLimitsSwitch interface {
	Set(syncBytes, asyncBytes int)
}

// BodyLimitsReloader возвращает Reloader, который перечитывает рабочие лимиты
// размера тела и применяет их без рестарта (§97).
//
// Конфиг задаёт ПОТОЛОК (под него настроены nginx, gRPC, Kafka и память
// сервисов), а эти значения — сколько администратор разрешает сегодня. nil
// означает «оператор ничего не задал» и разворачивается в дефолт домена;
// зажатие потолком и инвариант async ≤ sync — забота провайдера.
//
// Тот же Reloader используется как сид стартового значения — первый вызов
// сразу после создания подписчика.
func BodyLimitsReloader(pool *pgxpool.Pool, sw BodyLimitsSwitch, cipher *crypto.Cipher, logger logging.Logger) reloader.Reloader {
	return func(ctx context.Context) error {
		o, err := readAppSettings(ctx, pool, cipher, logger)
		if err != nil {
			return err
		}
		var syncPtr, asyncPtr *int
		if o != nil {
			syncPtr = o.General.MaxBodyBytes
			asyncPtr = o.General.MaxAsyncBodyBytes
		}
		syncBytes := domain.BodyLimitOrDefault(syncPtr)
		asyncBytes := domain.BodyLimitOrDefault(asyncPtr)
		sw.Set(syncBytes, asyncBytes)
		logger.Info("body limits applied from app_settings",
			logger.Int("max_body_bytes", syncBytes),
			logger.Int("max_async_body_bytes", asyncBytes))
		return nil
	}
}

// WriterReloader — узкий интерфейс, который реализует chlog.WriterManager
// (sender) и в будущем — любой другой держатель CH-зависимостей. Объявлен
// в bootstrap'е, чтобы избежать import cycle с sender/chlog (config →
// bootstrap; bootstrap не импортирует sender).
type WriterReloader interface {
	Reload(ctx context.Context) error
}

// ClickHouseReloader возвращает Reloader, который полностью применяет
// изменения CH-настроек без рестарта (§8.4 / §14.5 / §9 ТЗ, Phase 6.3.2.5):
//
//  1. перечитывает overlay из app_settings и накладывает на cfg;
//  2. вызывает chpf.Manager.Reload — открывает новое соединение с актуальным
//     адресом/учёткой, делает Ping и атомарно подменяет внутренний conn;
//  3. для каждого WriterReloader (chlog.WriterManager) вызывает Reload —
//     старый Writer flush'ится и останавливается, поднимается новый
//     с актуальными BufferMaxSize/Workers/BatchSize.
//
// Если open/ping нового conn'а упал — старый conn остаётся живым, writer
// не пересоздаётся, ошибка возвращается в reloader.Subscriber.handle
// и логируется. Это гарантирует, что битые настройки UI не «убьют» поток
// логов.
func ClickHouseReloader(pool *pgxpool.Pool, cfg *config.Config, mgr *chpf.Manager, writers []WriterReloader, cipher *crypto.Cipher, logger logging.Logger) reloader.Reloader {
	return func(ctx context.Context) error {
		o, err := readAppSettings(ctx, pool, cipher, logger)
		if err != nil {
			return err
		}
		overlayClickHouse(cfg, o)

		if mgr != nil {
			if err := mgr.Reload(ctx); err != nil {
				return err
			}
		}
		for _, w := range writers {
			if w == nil {
				continue
			}
			if err := w.Reload(ctx); err != nil {
				return err
			}
		}
		logger.Info("clickhouse hot-reload applied",
			logger.Str("host", cfg.ClickHouse.Host),
			logger.Int("port", cfg.ClickHouse.Port),
			logger.Str("db", cfg.ClickHouse.Database),
			logger.Int("writers_recreated", len(writers)))
		return nil
	}
}
