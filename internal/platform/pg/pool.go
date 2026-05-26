// Package pg — pgxpool factory + миграции через golang-migrate.
//
// pgx/v5 — нативный драйвер PostgreSQL, выбран по §17.4 ТЗ вместо
// database/sql + lib/pq.
package pg

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"nexus/internal/platform/config"
)

// New создаёт pgxpool с настройками из конфига и проверяет соединение Ping.
func New(ctx context.Context, c *config.PostgresSection) (*pgxpool.Pool, error) {
	dsn := DSN(c)

	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	if c.MaxOpenConns > 0 {
		poolCfg.MaxConns = int32(c.MaxOpenConns)
	}
	if c.MaxIdleConns > 0 {
		poolCfg.MinConns = int32(c.MaxIdleConns)
	}
	if c.ConnMaxLifetimeMin > 0 {
		poolCfg.MaxConnLifetime = time.Duration(c.ConnMaxLifetimeMin) * time.Minute
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("pgxpool.NewWithConfig: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pgxpool ping: %w", err)
	}
	return pool, nil
}

// DSN собирает строку подключения для pgx.
func DSN(c *config.PostgresSection) string {
	return fmt.Sprintf(
		"postgres://%s:%s@%s:%d/%s?sslmode=disable",
		c.User, c.Password, c.Host, c.Port, c.Database,
	)
}
