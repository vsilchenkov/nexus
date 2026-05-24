// Package clickhouse — заглушка-фабрика на Phase 0.
//
// Реальный логгер с буфером, file-fallback и batch-вставкой появится
// в Phase 1 (§4.3 и §9.4 ТЗ).
package clickhouse

import (
	"context"
	"fmt"
	"time"

	chgo "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"bus/internal/platform/config"
	"bus/internal/platform/healthcheck"
)

// New открывает соединение и проверяет Ping.
// Возвращает driver.Conn — нативный клиент.
func New(ctx context.Context, c *config.ClickHouseSection) (driver.Conn, error) {
	addr := fmt.Sprintf("%s:%d", c.Host, c.Port)
	conn, err := chgo.Open(&chgo.Options{
		Addr: []string{addr},
		Auth: chgo.Auth{
			Database: c.Database,
			Username: c.User,
			Password: c.Password,
		},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("clickhouse.Open %s: %w", addr, err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := conn.Ping(pingCtx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("clickhouse ping %s: %w", addr, err)
	}
	return conn, nil
}

// HealthChecker — healthcheck.Checker, который пингует соединение.
func HealthChecker(name string, conn driver.Conn) healthcheck.Checker {
	return healthcheck.CheckerFunc{
		N: name,
		F: func(ctx context.Context) error {
			return conn.Ping(ctx)
		},
	}
}
