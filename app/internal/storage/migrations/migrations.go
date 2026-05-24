package migrations

import (
	"bus/app/internal/lib/logging"
	"bus/app/internal/storage/database"
	"fmt"
	"path/filepath"

	"github.com/cockroachdb/errors"
	"github.com/golang-migrate/migrate/v4"
	migrateDB "github.com/golang-migrate/migrate/v4/database"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/database/sqlserver"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

const (
	migrationsPath = "./app/internal/storage/migrations/"
	pathPostgres   = "postgres"
	pathMSSQL      = "mssql"
)

type Migration struct {
	migrate *migrate.Migrate
	logger  logging.Logger
}

func New(db *database.DataBase, logger logging.Logger) (*Migration, error) {

	const op = "storage.migrations.New"

	var driver migrateDB.Driver
	var source string
	var err error

	switch {
	case db.IsPostgres():
		driver, err = postgres.WithInstance(db.GetDB(), &postgres.Config{})
		source = filepath.Join(migrationsPath, pathPostgres)
	case db.IsMSSql():
		driver, err = sqlserver.WithInstance(db.GetDB(), &sqlserver.Config{})
		source = filepath.Join(migrationsPath, pathMSSQL)
	default:
		return nil, errors.Newf("unsupported type DB %s, %s", db.DBType(), op)
	}

	if err != nil {
		return nil, errors.WithMessage(err, "error create driver")
	}

	absPath, _ := filepath.Abs(source)
	sourceURL := filepath.ToSlash(absPath)
	m, err := migrate.NewWithDatabaseInstance(
		fmt.Sprintf("file://%s", sourceURL),
		db.DBType(),
		driver)

	if err != nil {
		return nil, err
	}

	return &Migration{
		migrate: m,
		logger:  logger,
	}, nil
}

func (m Migration) Up() error {

	if err := m.migrate.Up(); err != nil && err != migrate.ErrNoChange {
		return err
	}

	m.logger.Info("Migrations up applied")
	return nil
}

func (m Migration) Down() error {

	// m.Forse(1)

	if err := m.migrate.Down(); err != nil && err != migrate.ErrNoChange {
		return err
	}

	m.logger.Info("Migrations down applied")
	return nil
}

func (m Migration) Close() {
	m.migrate.Close()
}

func (m Migration) Forse(version int) error {
	return m.migrate.Force(version)
}
