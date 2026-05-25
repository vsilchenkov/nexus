// Package bootstrap — общий стартовый код для трёх main'ов:
// разбор флагов, загрузка конфига, init Sentry/логгера, подключение к Postgres,
// обработка миграционных флагов.
package bootstrap

import (
	"context"
	"fmt"
	"os"
	"time"

	chdrv "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jinzhu/copier"
	goredis "github.com/redis/go-redis/v9"

	"bus/internal/platform/build"
	chpf "bus/internal/platform/clickhouse"
	"bus/internal/platform/config"
	"bus/internal/platform/crypto"
	"bus/internal/platform/kafka"
	"bus/internal/platform/logging"
	pgpf "bus/internal/platform/pg"
	redispf "bus/internal/platform/redis"
	sentrypf "bus/internal/platform/sentry"
)

const encryptionKeyEnv = "ENCRYPTION_KEY"

// Init выполняет общие шаги старта: парсит флаги, загружает конфиг,
// инициализирует Sentry и логгер.
// При фатальной ошибке завершает процесс.
func Init(versionInfoData []byte, projectName string) (*build.Option, config.Flags, *config.Config, logging.Logger) {
	buildOpt, err := build.NewOption(versionInfoData, projectName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "build option: %v\n", err)
		os.Exit(1)
	}

	flags := config.ParseFlags(buildOpt.Version)

	cfg, path, err := config.Load(flags, buildOpt.WorkingDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}
	if cfg.Build.ProjectName == "" {
		cfg.Build.ProjectName = projectName
	}
	if cfg.Build.Version == "" {
		cfg.Build.Version = buildOpt.Version
	}

	if err := sentrypf.Init(&cfg.Sentry, projectName, buildOpt.Version); err != nil {
		fmt.Fprintf(os.Stderr, "sentry init: %v\n", err)
		os.Exit(1)
	}

	// vsilchenkov/logging.Config и .SentryConfig содержат набор полей,
	// часть из которых совпадает по именам с buildOpt и нашими секциями.
	// copier пройдётся по совпадающим именам и заполнит их —
	// это то же, что делал старый main и сохраняет совместимость
	// при обновлении внешнего пакета.
	logCfg := logging.Config{}
	if err := copier.Copy(&logCfg, buildOpt); err != nil {
		fmt.Fprintf(os.Stderr, "copy build into logging config: %v\n", err)
		os.Exit(1)
	}
	if err := copier.Copy(&logCfg, cfg.Logging); err != nil {
		fmt.Fprintf(os.Stderr, "copy logging section: %v\n", err)
		os.Exit(1)
	}

	sentryCfg := logging.SentryConfig{}
	if err := copier.Copy(&sentryCfg, buildOpt); err != nil {
		fmt.Fprintf(os.Stderr, "copy build into sentry config: %v\n", err)
		os.Exit(1)
	}
	if err := copier.Copy(&sentryCfg, cfg.Sentry); err != nil {
		fmt.Fprintf(os.Stderr, "copy sentry section: %v\n", err)
		os.Exit(1)
	}

	logger := logging.Init(&logCfg, &sentryCfg)
	logger.Info("starting service",
		logger.Str("service", projectName),
		logger.Str("version", buildOpt.Version),
		logger.Str("config", path))

	return buildOpt, flags, cfg, logger
}

// Shutdown — defer-friendly: flush Sentry, recover panic.
func Shutdown(logger logging.Logger) {
	if r := recover(); r != nil {
		logger.Error("panic recovered",
			logger.Any("panic", r))
	}
	sentrypf.Flush(2 * time.Second)
}

// MustPG подключает pgxpool. Завершает процесс при ошибке.
func MustPG(ctx context.Context, cfg *config.Config, logger logging.Logger) *pgxpool.Pool {
	pool, err := pgpf.New(ctx, &cfg.Postgres)
	if err != nil {
		logger.ErrorWithOp("postgres connect failed", err, "bootstrap.MustPG")
		os.Exit(1)
	}
	return pool
}

// MustRedis подключает Redis. Завершает процесс при ошибке.
func MustRedis(ctx context.Context, cfg *config.Config, logger logging.Logger) *goredis.Client {
	client, err := redispf.New(ctx, &cfg.Redis)
	if err != nil {
		logger.ErrorWithOp("redis connect failed", err, "bootstrap.MustRedis")
		os.Exit(1)
	}
	return client
}

// MustKafkaDialer — Phase 0: только TCP-dialer для healthcheck.
func MustKafkaDialer(cfg *config.Config) *kafka.Dialer {
	return kafka.NewDialer(cfg.Kafka.Brokers)
}

// MustEnsureKafkaTopics создаёт перечисленные топики на старте
// (если не существуют). Параметры — из cfg.Kafka.Topic (§5.3).
// На ошибке завершает процесс — без топиков async не работает.
func MustEnsureKafkaTopics(ctx context.Context, cfg *config.Config, logger logging.Logger, topics ...string) {
	if err := kafka.EnsureTopics(ctx, cfg, logger, topics...); err != nil {
		logger.ErrorWithOp("ensure kafka topics failed", err, "bootstrap.MustEnsureKafkaTopics")
		os.Exit(1)
	}
}

// MustClickHouse подключает ClickHouse. Завершает процесс при ошибке.
func MustClickHouse(ctx context.Context, cfg *config.Config, logger logging.Logger) chdrv.Conn {
	conn, err := chpf.New(ctx, &cfg.ClickHouse)
	if err != nil {
		logger.ErrorWithOp("clickhouse connect failed", err, "bootstrap.MustClickHouse")
		os.Exit(1)
	}
	return conn
}

// MustCipher читает ENCRYPTION_KEY из окружения и создаёт AES-256-GCM cipher.
// При пустом / неверной длине / невалидном ключе — exit 1 (§5.5 ТЗ:
// «лучше не подняться, чем работать со сломанным шифрованием»).
func MustCipher(logger logging.Logger) *crypto.Cipher {
	key := os.Getenv(encryptionKeyEnv)
	c, err := crypto.NewCipher(key)
	if err != nil {
		logger.ErrorWithOp("encryption key invalid", err, "bootstrap.MustCipher",
			logger.Str("env", encryptionKeyEnv))
		os.Exit(1)
	}
	return c
}

// HandleMigrateFlags обрабатывает --migrate-up/--migrate-down/--migrate-status.
// Возвращает true, если флаг был задействован — main должен после этого завершиться.
func HandleMigrateFlags(flags config.Flags, cfg *config.Config, logger logging.Logger) bool {
	if !flags.MigrateUp && flags.MigrateDownN == 0 && !flags.MigrateStatus {
		return false
	}

	dir := "migrations"
	mg, err := pgpf.NewMigrator(&cfg.Postgres, dir, logger)
	if err != nil {
		logger.ErrorWithOp("migrator init failed", err, "bootstrap.HandleMigrateFlags",
			logger.Str("dir", dir))
		os.Exit(1)
	}
	defer mg.Close()

	switch {
	case flags.MigrateUp:
		if err := mg.Up(); err != nil {
			logger.ErrorWithOp("migrate up failed", err, "bootstrap.HandleMigrateFlags")
			os.Exit(1)
		}
		logger.Info("migrations applied")
	case flags.MigrateDownN > 0:
		if err := mg.Down(flags.MigrateDownN); err != nil {
			logger.ErrorWithOp("migrate down failed", err, "bootstrap.HandleMigrateFlags",
				logger.Int("n", flags.MigrateDownN))
			os.Exit(1)
		}
		logger.Info("migrations rolled back", logger.Int("n", flags.MigrateDownN))
	case flags.MigrateStatus:
		v, dirty, err := mg.Status()
		if err != nil {
			logger.ErrorWithOp("migrate status failed", err, "bootstrap.HandleMigrateFlags")
			os.Exit(1)
		}
		fmt.Printf("schema version: %d dirty: %v\n", v, dirty)
	}
	return true
}

// AutoMigrate накатывает все непримененные миграции при старте.
// Падение → exit 1 (см. §5.1 ТЗ — лучше пустой ответ, чем работа на старой схеме).
func AutoMigrate(cfg *config.Config, logger logging.Logger) {
	mg, err := pgpf.NewMigrator(&cfg.Postgres, "migrations", logger)
	if err != nil {
		logger.ErrorWithOp("migrator init failed", err, "bootstrap.AutoMigrate")
		os.Exit(1)
	}
	defer mg.Close()
	if err := mg.Up(); err != nil {
		logger.ErrorWithOp("auto-migrate failed", err, "bootstrap.AutoMigrate")
		os.Exit(1)
	}
}
