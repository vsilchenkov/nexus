// Package redis — фабрика *redis.Client с настройками из конфига.
//
// Доменные интерфейсы (NodeCache, SessionRepo, RateLimiter, …) живут
// в usecase/port каждого сервиса; здесь — только тонкий слой над go-redis.
package redis

import (
	"context"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"bus/internal/platform/config"
	"bus/internal/platform/healthcheck"
)

// New создаёт *redis.Client и проверяет соединение Ping.
func New(ctx context.Context, c *config.RedisSection) (*goredis.Client, error) {
	addr := fmt.Sprintf("%s:%d", c.Host, c.Port)
	client := goredis.NewClient(&goredis.Options{
		Addr:         addr,
		Password:     c.Password,
		DB:           c.DB,
		PoolSize:     c.PoolSize,
		MinIdleConns: c.MinIdleConns,
		DialTimeout:  time.Duration(c.DialTimeoutMs) * time.Millisecond,
		ReadTimeout:  time.Duration(c.ReadTimeoutMs) * time.Millisecond,
		WriteTimeout: time.Duration(c.WriteTimeoutMs) * time.Millisecond,
	})

	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redis ping %s: %w", addr, err)
	}
	return client, nil
}

// HealthChecker — healthcheck.Checker, который пингует клиент.
func HealthChecker(name string, client *goredis.Client) healthcheck.Checker {
	return healthcheck.CheckerFunc{
		N: name,
		F: func(ctx context.Context) error {
			return client.Ping(ctx).Err()
		},
	}
}
