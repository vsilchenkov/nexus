//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	webredis "nexus/internal/web/adapter/out/redis"
)

// startRedis поднимает Redis 7 через generic testcontainer и возвращает
// готовый клиент + cleanup. Используется во всех тестах с Redis, чтобы не
// тащить отдельный модуль testcontainers/redis (§10.4 ТЗ).
func startRedis(t *testing.T, ctx context.Context) (*goredis.Client, func()) {
	t.Helper()

	req := testcontainers.ContainerRequest{
		Image:        "redis:7-alpine",
		ExposedPorts: []string{"6379/tcp"},
		WaitingFor: wait.ForLog("Ready to accept connections").
			WithStartupTimeout(30 * time.Second),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	require.NoError(t, err, "start redis")

	host, err := c.Host(ctx)
	require.NoError(t, err)
	port, err := c.MappedPort(ctx, "6379/tcp")
	require.NoError(t, err)

	client := goredis.NewClient(&goredis.Options{
		Addr:        fmt.Sprintf("%s:%s", host, port.Port()),
		DialTimeout: 5 * time.Second,
	})

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	require.NoError(t, client.Ping(pingCtx).Err(), "redis ping")

	cleanup := func() {
		_ = client.Close()
		_ = c.Terminate(ctx)
	}
	return client, cleanup
}

// TestSessionRepo_E2E проверяет полный CRUD-цикл сессий: Create / Get / Touch /
// Delete / DeleteByUser. Покрывает §7.1 ТЗ (Redis-backed sessions).
func TestSessionRepo_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	client, cleanup := startRedis(t, ctx)
	defer cleanup()

	repo := webredis.NewSessionRepoRedis(client)

	now := time.Now()
	s := &domain.Session{
		Token:      "tok-1",
		UserID:     "user-1",
		Role:       domain.UserRoleAdmin,
		Lang:       domain.UserLangEN,
		CreatedAt:  now,
		LastSeenAt: now,
	}
	require.NoError(t, repo.Create(ctx, s, time.Hour))

	got, err := repo.Get(ctx, "tok-1")
	require.NoError(t, err)
	require.Equal(t, "user-1", got.UserID)
	require.Equal(t, domain.UserRoleAdmin, got.Role)

	// Touch продлевает TTL и пересохраняет сессию (включая LastSeenAt,
	// Phase AUD.5 — раньше делал только EXPIRE).
	got.LastSeenAt = now.Add(30 * time.Minute)
	require.NoError(t, repo.Touch(ctx, got, 2*time.Hour))
	touched, err := repo.Get(ctx, "tok-1")
	require.NoError(t, err)
	require.WithinDuration(t, now.Add(30*time.Minute), touched.LastSeenAt, time.Second)

	// Вторая сессия того же пользователя — DeleteByUser должен снести обе.
	s2 := &domain.Session{
		Token:      "tok-2",
		UserID:     "user-1",
		Role:       domain.UserRoleAdmin,
		Lang:       domain.UserLangEN,
		CreatedAt:  now,
		LastSeenAt: now,
	}
	require.NoError(t, repo.Create(ctx, s2, time.Hour))

	// Сессия другого пользователя — должна остаться.
	s3 := &domain.Session{
		Token:      "tok-3",
		UserID:     "user-2",
		Role:       domain.UserRoleViewer,
		Lang:       domain.UserLangEN,
		CreatedAt:  now,
		LastSeenAt: now,
	}
	require.NoError(t, repo.Create(ctx, s3, time.Hour))

	deleted, err := repo.DeleteByUser(ctx, "user-1")
	require.NoError(t, err)
	require.Equal(t, 2, deleted)

	_, err = repo.Get(ctx, "tok-1")
	require.ErrorIs(t, err, domain.ErrSessionNotFound)
	_, err = repo.Get(ctx, "tok-2")
	require.ErrorIs(t, err, domain.ErrSessionNotFound)

	got3, err := repo.Get(ctx, "tok-3")
	require.NoError(t, err)
	require.Equal(t, "user-2", got3.UserID)

	require.NoError(t, repo.Delete(ctx, "tok-3"))
	_, err = repo.Get(ctx, "tok-3")
	require.ErrorIs(t, err, domain.ErrSessionNotFound)
}

// TestNodeCache_E2E проверяет NodeCacheRedis: Set / GetByPath / Invalidate.
// Покрывает §5.4 / §9.2 ТЗ (горячий кеш конфига узлов).
func TestNodeCache_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	client, cleanup := startRedis(t, ctx)
	defer cleanup()

	cache := webredis.NewNodeCacheRedis(client, logging.NewNoop())

	n := &domain.Node{
		ID:               "id-1",
		Path:             "demo/cache",
		RootMethod:       domain.RootMethodRequest,
		URLMode:          domain.URLModeStatic,
		TargetURL:        "https://example.com/hook",
		AuthType:         domain.AuthTypeToken,
		AuthCredentials:  "secret-token",
		IncomingAuthType: domain.IncomingAuthTypeNone,
		Status:           domain.NodeStatusEnabled,
		TimeoutMs:        5000,
	}
	require.NoError(t, cache.Set(ctx, n, time.Minute))

	got, err := cache.GetByPath(ctx, "demo/cache")
	require.NoError(t, err)
	require.Equal(t, "id-1", got.ID)
	require.Equal(t, "https://example.com/hook", got.TargetURL)
	require.Equal(t, "secret-token", got.AuthCredentials)

	_, err = cache.GetByPath(ctx, "no/such/path")
	require.ErrorIs(t, err, domain.ErrNodeNotFound)

	require.NoError(t, cache.InvalidateByPath(ctx, "demo/cache"))
	_, err = cache.GetByPath(ctx, "demo/cache")
	require.ErrorIs(t, err, domain.ErrNodeNotFound)
}

// TestSession_TTLExpires — поведенческий тест: установленный короткий TTL
// действительно истекает. Используется минимальный TTL 1 сек + 2 сек sleep.
func TestSession_TTLExpires(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: skipping sleep-based TTL test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	client, cleanup := startRedis(t, ctx)
	defer cleanup()

	repo := webredis.NewSessionRepoRedis(client)
	now := time.Now()
	s := &domain.Session{
		Token:      "tok-ttl",
		UserID:     "user-ttl",
		Role:       domain.UserRoleViewer,
		Lang:       domain.UserLangEN,
		CreatedAt:  now,
		LastSeenAt: now,
	}
	require.NoError(t, repo.Create(ctx, s, time.Second))

	got, err := repo.Get(ctx, "tok-ttl")
	require.NoError(t, err)
	require.Equal(t, "user-ttl", got.UserID)

	time.Sleep(2 * time.Second)
	_, err = repo.Get(ctx, "tok-ttl")
	require.ErrorIs(t, err, domain.ErrSessionNotFound)
}
