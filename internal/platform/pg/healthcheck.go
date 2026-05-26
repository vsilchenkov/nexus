package pg

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"nexus/internal/platform/healthcheck"
)

// HealthChecker — healthcheck.Checker, который пингует пул.
func HealthChecker(name string, pool *pgxpool.Pool) healthcheck.Checker {
	return healthcheck.CheckerFunc{
		N: name,
		F: func(ctx context.Context) error {
			return pool.Ping(ctx)
		},
	}
}
