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
//
// Принимает ConnProvider, а не driver.Conn напрямую: после hot-reload
// (Phase 6.3.2.5) Manager подменяет внутренний conn, и healthcheck должен
// смотреть на актуальный, а не на закрытый старый.
func HealthChecker(name string, provider ConnProvider) healthcheck.Checker {
	return healthcheck.CheckerFunc{
		N: name,
		F: func(ctx context.Context) error {
			conn := provider.Conn()
			if conn == nil {
				return fmt.Errorf("clickhouse conn is nil")
			}
			return conn.Ping(ctx)
		},
	}
}

// StaticProvider оборачивает уже открытый driver.Conn в ConnProvider —
// для кода, который не нуждается в hot-reload (тесты, утилиты).
func StaticProvider(c driver.Conn) ConnProvider {
	return staticProvider{c: c}
}

type staticProvider struct{ c driver.Conn }

func (s staticProvider) Conn() driver.Conn { return s.c }
