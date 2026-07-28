// Package bootstrap — общий стартовый код для трёх main'ов:
// разбор флагов, загрузка конфига, init Sentry/логгера, подключение к Postgres,
// обработка миграционных флагов.
package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	chdrv "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jinzhu/copier"
	goredis "github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"

	"nexus/internal/platform/build"
	chpf "nexus/internal/platform/clickhouse"
	"nexus/internal/platform/config"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/kafka"
	"nexus/internal/platform/logging"
	otelpf "nexus/internal/platform/otel"
	pgpf "nexus/internal/platform/pg"
	redispf "nexus/internal/platform/redis"
	sentrypf "nexus/internal/platform/sentry"
)

const encryptionKeyEnv = "ENCRYPTION_KEY"

// Init выполняет общие шаги старта: парсит флаги, загружает конфиг,
// инициализирует Sentry и логгер (собственная сборка цепочки хендлеров с
// runtime-уровнем — §51; возвращаемый LogController отдаёт LevelVar и кольцо
// служебных логов). При фатальной ошибке завершает процесс.
func Init(versionInfoData []byte, projectName string) (*build.Option, config.Flags, *config.Config, logging.Logger, *LogController) {
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
	// Версия — единый источник истины: git. build.Version вшивается в бинарь из
	// git describe / git-тега через ldflags (см. Makefile, Dockerfile) и
	// перекрывает config. Если ldflags пуст (сборка без -X) —
	// остаётся значение из конфига (для config_debug это пусто → fallback на
	// versioninfo.json произошёл уже внутри build.NewOption).
	if buildOpt.Version != "" {
		cfg.Build.Version = buildOpt.Version
	}
	// Commit/BuildDate — из git через ldflags (§34.3): отдаются в /api/version.
	if buildOpt.Commit != "" {
		cfg.Build.Commit = buildOpt.Commit
	}
	if buildOpt.BuildDate != "" {
		cfg.Build.BuildDate = buildOpt.BuildDate
	}

	// §70.7: тег instance в событиях. Значение берётся из конфига — сверка с
	// PostgreSQL идёт позже (MustInstanceIdentity), а Sentry нужен уже здесь,
	// чтобы ошибки самого старта не потерялись.
	if err := sentrypf.Init(&cfg.Sentry, projectName, buildOpt.Version, cfg.Instance.ID); err != nil {
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

	// §51: собственная сборка цепочки хендлеров вместо вендорного Initlogger —
	// уровень базового вывода и кольца логов управляется LevelVar в runtime.
	logger, logCtl := buildLogger(&logCfg, &sentryCfg, strings.ToLower(projectName), cfg.Instance.ID)
	fields := []slog.Attr{
		logger.Str("service", projectName),
		logger.Str("version", buildOpt.Version),
		logger.Str("config", path),
	}
	if buildOpt.Commit != "" {
		fields = append(fields, logger.Str("commit", buildOpt.Commit))
	}
	if buildOpt.BuildDate != "" {
		fields = append(fields, logger.Str("build_date", buildOpt.BuildDate))
	}
	// §70.7: на общем ClickHouse (и в общей консоли логов) нужно понимать, чей
	// это процесс. У ноды без идентификатора атрибут не добавляется — вывод
	// действующей ноды не меняется.
	if cfg.Instance.ID != "" {
		fields = append(fields, logger.Str("instance", cfg.Instance.ID))
	}
	logger.Info("starting service", fields...)

	return buildOpt, flags, cfg, logger, logCtl
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

// TryClickHouse подключает ClickHouse, но не падает при ошибке. Используется
// в сервисах, для которых ClickHouse — опциональная зависимость (Web без
// replay/live-tail умеет работать).
func TryClickHouse(ctx context.Context, cfg *config.Config, logger logging.Logger) (chdrv.Conn, error) {
	conn, err := chpf.New(ctx, &cfg.ClickHouse)
	if err != nil {
		logger.Warn("clickhouse connect failed; replay and live-tail will be disabled",
			logger.Err(err))
		return nil, err
	}
	return conn, nil
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

// HandleSetAdminPassword — bootstrap-команда задать пароль admin'у.
// При --set-admin-password: подключается к PG, обновляет users WHERE login='admin',
// сбрасывает must_change_password=false, выходит с exit 0.
// Возвращает true, если флаг был задействован — main должен после этого завершиться.
func HandleSetAdminPassword(ctx context.Context, flags config.Flags, cfg *config.Config, logger logging.Logger) bool {
	if flags.SetAdminPassword == "" {
		return false
	}
	if len(flags.SetAdminPassword) < 8 {
		logger.Error("password must be at least 8 characters")
		os.Exit(1)
	}
	pool := MustPG(ctx, cfg, logger)
	defer pool.Close()

	hashBytes, err := bcrypt.GenerateFromPassword([]byte(flags.SetAdminPassword), bcrypt.DefaultCost)
	if err != nil {
		logger.ErrorWithOp("bcrypt failed", err, "bootstrap.HandleSetAdminPassword")
		os.Exit(1)
	}
	hash := string(hashBytes)
	tag, err := pool.Exec(ctx, `
UPDATE users SET password_hash=$1, must_change_password=false, active=true
WHERE login='admin'`, hash)
	if err != nil {
		logger.ErrorWithOp("update admin password failed", err, "bootstrap.HandleSetAdminPassword")
		os.Exit(1)
	}
	if tag.RowsAffected() == 0 {
		logger.Error("admin user not found — run migrations first")
		os.Exit(1)
	}
	logger.Info("admin password updated; you can now login")
	return true
}

// MustOtel инициализирует OpenTelemetry tracer (§16 ТЗ, Phase 8.2).
// При cfg.Otel.Enable=false возвращает noop-shutdown и nil-error: безопасно
// вызывать defer shutdown(ctx).
//
// Init не делает network round-trip — exporter лениво коннектится при первом
// экспорте, поэтому даже временно недоступный collector не блокирует старт.
func MustOtel(ctx context.Context, cfg *config.Config, projectName string, logger logging.Logger) otelpf.ShutdownFunc {
	shutdown, err := otelpf.Init(ctx, &cfg.Otel, projectName, logger)
	if err != nil {
		logger.ErrorWithOp("otel init failed; continuing without tracing", err, "bootstrap.MustOtel")
		return func(context.Context) error { return nil }
	}
	return shutdown
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
