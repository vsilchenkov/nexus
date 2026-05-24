package storage

import (
	"context"
	"database/sql"
)

type DB interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	PingContext(ctx context.Context) error
	Close() error

	IsPostgres() bool
	IsMSSql() bool
}

type Config struct {
	DBType   string // sqlserver/postgres
	Host     string
	Port     int
	DBName   string
	User     string
	Password string
}

type Storage struct {
	db DB
}
