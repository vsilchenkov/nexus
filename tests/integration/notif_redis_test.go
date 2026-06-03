//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	rediscache "nexus/internal/web/adapter/out/redis"
)

// TestNotifCheckpointAndLock_E2E (§20.3/20.4, Phase F2.3): checkpoint set/get и
// распределённый SETNX-лок на реальном Redis.
func TestNotifCheckpointAndLock_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, cleanup := startRedis(t, ctx)
	defer cleanup()

	// Checkpoint: пусто → 0; после Set → читается обратно.
	cp := rediscache.NewNotifCheckpoint(client)
	v, err := cp.Get(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 0, v)

	require.NoError(t, cp.Set(ctx, 1717171717000))
	v, err = cp.Get(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1717171717000, v)

	// Lock: первый TryLock — true, второй в пределах TTL — false.
	lock := rediscache.NewNotifLock(client)
	ok, err := lock.TryLock(ctx, 2*time.Second)
	require.NoError(t, err)
	require.True(t, ok, "first lock acquired")

	ok, err = lock.TryLock(ctx, 2*time.Second)
	require.NoError(t, err)
	require.False(t, ok, "second lock within TTL must fail")
}
