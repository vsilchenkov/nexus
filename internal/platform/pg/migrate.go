package pg

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres" // драйвер postgres
	_ "github.com/golang-migrate/migrate/v4/source/file"       // file-source

	"bus/internal/platform/config"
	"bus/internal/platform/logging"
)

// migrator — обёртка для управления миграциями.
type Migrator struct {
	m      *migrate.Migrate
	logger logging.Logger
}

// NewMigrator создаёт миграционный инстанс. Использует database/sql под
// капотом (через драйвер postgres golang-migrate), а не pgx — потому что
// golang-migrate написан под database/sql.
//
// Concurrency-safe: golang-migrate использует advisory lock postgres,
// несколько одновременно стартующих инстансов не накатывают миграции
// параллельно (§5.1 ТЗ).
func NewMigrator(c *config.PostgresSection, migrationsDir string, logger logging.Logger) (*Migrator, error) {
	return NewMigratorFromDSN(DSN(c), migrationsDir, logger)
}

// NewMigratorFromDSN — вариант для тестов: принимает готовый DSN
// (без секции config.PostgresSection). Используется в integration-тестах
// с testcontainers (§10.1).
func NewMigratorFromDSN(dsn, migrationsDir string, logger logging.Logger) (*Migrator, error) {
	abs, err := filepath.Abs(migrationsDir)
	if err != nil {
		return nil, fmt.Errorf("abs path: %w", err)
	}
	source := "file://" + filepath.ToSlash(abs)

	m, err := migrate.New(source, dsn)
	if err != nil {
		return nil, fmt.Errorf("migrate.New: %w", err)
	}
	return &Migrator{m: m, logger: logger}, nil
}

// Up — применить все непримененные миграции. Возвращает nil при успехе или
// если миграций нет (errors.Is migrate.ErrNoChange).
func (mg *Migrator) Up() error {
	if err := mg.m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}

// Down — откатить N последних миграций.
func (mg *Migrator) Down(n int) error {
	if n <= 0 {
		return errors.New("migrate down: n must be > 0")
	}
	if err := mg.m.Steps(-n); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate down %d: %w", n, err)
	}
	return nil
}

// Status — текущая версия схемы и dirty-флаг.
func (mg *Migrator) Status() (version uint, dirty bool, err error) {
	v, d, err := mg.m.Version()
	if err != nil && !errors.Is(err, migrate.ErrNilVersion) {
		return 0, false, fmt.Errorf("migrate version: %w", err)
	}
	return v, d, nil
}

// Close освобождает ресурсы миграционного инстанса.
func (mg *Migrator) Close() {
	if mg.m != nil {
		_, _ = mg.m.Close()
	}
}
