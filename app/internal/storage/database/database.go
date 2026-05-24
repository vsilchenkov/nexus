package database

import (
	"bus/app/internal/storage"
	"bus/app/internal/storage/database/mssql"
	"bus/app/internal/storage/database/postgres"
	"context"
	"database/sql"

	"github.com/jmoiron/sqlx"
	"github.com/vsilchenkov/logging"

	"github.com/cockroachdb/errors"
)

type DataBase struct {
	db     *sqlx.DB
	dbType string
	config *storage.Config
	logger logging.Logger
}

func New(c *storage.Config, l logging.Logger) (*DataBase, error) {

	var db *sqlx.DB
	var err error

	switch c.DBType {
	case "sqlserver":
		db, err = mssql.Open(c)
	case "postgres":
		db, err = postgres.Open(c)
	default:
		err = errors.New("unknown database type")
	}

	if err != nil {
		return nil, err
	}

	DB := &DataBase{
		db:     db,
		dbType: c.DBType,
		config: c,
		logger: l,
	}

	return DB, nil
}

func (d DataBase) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	query = d.db.Rebind(query)
	return d.db.QueryRowContext(ctx, query, args...)
}

func (d DataBase) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	query = d.db.Rebind(query)
	return d.db.QueryContext(ctx, query, args...)
}

func (d DataBase) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	query = d.db.Rebind(query)
	return d.db.ExecContext(ctx, query, args...)
}

func (d DataBase) PingContext(ctx context.Context) error {
	return d.db.PingContext(ctx)
}

func (d DataBase) Close() error {
	return d.db.Close()
}

func (d DataBase) IsPostgres() bool {
	return d.dbType == "postgres"
}

func (d DataBase) IsMSSql() bool {
	return d.dbType == "sqlserver"
}

func (d DataBase) GetDB() *sql.DB {
	return d.db.DB
}

func (d DataBase) DBType() string {
	return d.dbType
}
