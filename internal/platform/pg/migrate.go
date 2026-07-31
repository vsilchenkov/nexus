package pg

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres" // драйвер postgres
	"github.com/golang-migrate/migrate/v4/source"
	_ "github.com/golang-migrate/migrate/v4/source/file" // file-source

	"nexus/internal/platform/config"
	"nexus/internal/platform/logging"
)

// Ошибки состояния схемы (§74).
var (
	// ErrDirtySchema — предыдущая миграция оборвалась на середине: в
	// schema_migrations стоит dirty. Состояние схемы неизвестно, работать на ней
	// нельзя; выход — привести схему руками и объявить версию через Force (§74.4).
	ErrDirtySchema = errors.New("pg: schema is dirty")

	// ErrSchemaAhead — версия схемы в БД больше максимальной версии в каталоге
	// миграций этого бинаря. Штатное временное состояние незавершённого отката
	// кода (§74.3): сервис стартует, но признак объявляется логом и метрикой.
	ErrSchemaAhead = errors.New("pg: schema is newer than this build")

	// ErrUnknownMigration — в каталоге нет миграции с такой версией.
	ErrUnknownMigration = errors.New("pg: unknown migration version")
)

// Migrator — обёртка для управления миграциями.
type Migrator struct {
	m      *migrate.Migrate
	src    source.Driver
	logger logging.Logger
}

// State — состояние схемы относительно каталога миграций этого бинаря (§74.3).
type State struct {
	// DBVersion — версия из schema_migrations (0 = миграций не было).
	DBVersion uint
	// MaxLocal — максимальная версия в каталоге миграций бинаря (0 = каталог пуст).
	MaxLocal uint
	// Dirty — предыдущая миграция оборвалась.
	Dirty bool
	// Ahead — схема в БД новее бинаря: миграции не применялись.
	Ahead bool
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
//
// Источник миграций открывается отдельно (source.Open) и хранится в Migrator:
// по нему считается максимальная локальная версия для [Migrator.EnsureUp] и
// проверяется номер в [Migrator.Force] (§74). Закрывается вместе с migrate.
func NewMigratorFromDSN(dsn, migrationsDir string, logger logging.Logger) (*Migrator, error) {
	abs, err := filepath.Abs(migrationsDir)
	if err != nil {
		return nil, fmt.Errorf("abs path: %w", err)
	}
	src, err := source.Open("file://" + filepath.ToSlash(abs))
	if err != nil {
		return nil, fmt.Errorf("open migrations source: %w", err)
	}

	m, err := migrate.NewWithSourceInstance("file", src, dsn)
	if err != nil {
		// NewWithSourceInstance переданный источник при ошибке не закрывает —
		// закрываем сами, иначе на каждой неудачной попытке остаётся открытый
		// драйвер. При успехе его закроет Migrate.Close (он закрывает оба).
		_ = src.Close()
		return nil, fmt.Errorf("migrate.New: %w", err)
	}
	return &Migrator{m: m, src: src, logger: logger}, nil
}

// Up — применить все непримененные миграции. Возвращает nil при успехе или
// если миграций нет (errors.Is migrate.ErrNoChange).
//
// Требует, чтобы версия из schema_migrations существовала в каталоге: на схеме
// новее бинаря вернёт ошибку «no migration found for version N». На старте
// сервисов вместо этого используется [Migrator.EnsureUp] (§74.3).
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

// EnsureUp — стартовый вариант Up с разбором трёх состояний схемы (§74.3):
//
//   - dirty → ошибка ErrDirtySchema: состояние схемы неизвестно, стартовать нельзя;
//   - версия в БД больше максимальной локальной → State.Ahead, миграции НЕ
//     применяются, ошибки нет: это незавершённый откат кода, а схема проекта
//     обратно совместима по контракту §74.2;
//   - иначе → обычный Up.
//
// Сравнение версий выполняется ДО Up: так решение принимается по числам, а не по
// тексту ошибки golang-migrate («no migration found for version N»).
func (mg *Migrator) EnsureUp() (State, error) {
	dbVer, dirty, err := mg.Status()
	if err != nil {
		return State{}, err
	}
	maxLocal, err := mg.MaxLocalVersion()
	if err != nil {
		return State{}, err
	}
	st := State{DBVersion: dbVer, MaxLocal: maxLocal, Dirty: dirty}

	if dirty {
		return st, fmt.Errorf("%w: version %d (fix the schema manually, then --migrate-force <version>)",
			ErrDirtySchema, dbVer)
	}
	if dbVer > maxLocal {
		st.Ahead = true
		return st, nil
	}

	if err := mg.Up(); err != nil {
		return st, err
	}
	// Перечитываем: после применения миграций версия изменилась.
	if v, _, err := mg.Status(); err == nil {
		st.DBVersion = v
	}
	return st, nil
}

// MaxLocalVersion — максимальная версия миграции в каталоге этого бинаря.
// 0 означает «каталог пуст».
func (mg *Migrator) MaxLocalVersion() (uint, error) {
	return maxLocalVersion(mg.src)
}

// maxLocalVersion обходит источник от первой миграции к последней. Вынесена из
// метода, чтобы тестироваться на любом source.Driver без подключения к БД.
func maxLocalVersion(src source.Driver) (uint, error) {
	v, err := src.First()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("first migration: %w", err)
	}
	for {
		next, err := src.Next(v)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return v, nil
			}
			return 0, fmt.Errorf("next migration after %d: %w", v, err)
		}
		v = next
	}
}

// Force объявляет версию схемы version и снимает флаг dirty (§74.4).
// version = -1 означает «миграций нет» (состояние чистой базы).
//
// SQL при этом НЕ выполняется: привести схему в состояние, соответствующее
// заявленной версии, обязан оператор. Номер сверяется с каталогом миграций —
// force на несуществующую версию отклоняется (ErrUnknownMigration), иначе база
// «числилась бы» в состоянии, которого не существует.
func (mg *Migrator) Force(version int) error {
	if version < -1 {
		return fmt.Errorf("migrate force: version must be >= -1, got %d", version)
	}
	if version >= 0 {
		if err := mg.versionExistsLocally(uint(version)); err != nil {
			return err
		}
	}
	if err := mg.m.Force(version); err != nil {
		return fmt.Errorf("migrate force %d: %w", version, err)
	}
	return nil
}

// versionExistsLocally проверяет, что миграция с такой версией есть в каталоге.
// os.ErrExist от драйвера означает «версия существует, но up-файла нет» (только
// down) — для проверки это тоже «существует».
func (mg *Migrator) versionExistsLocally(version uint) error {
	r, _, err := mg.src.ReadUp(version)
	if err == nil {
		return r.Close()
	}
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %d", ErrUnknownMigration, version)
	}
	return fmt.Errorf("read migration %d: %w", version, err)
}

// Close освобождает ресурсы миграционного инстанса (источник и соединение с БД).
func (mg *Migrator) Close() {
	if mg.m != nil {
		_, _ = mg.m.Close()
	}
}
