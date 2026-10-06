//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/queuecancel"
)

// TestQueueCancel_CancelThenIsCancelled (§34.4): помеченные ID видны как
// отменённые, неизвестные — нет; TTL проставлен.
func TestQueueCancel_CancelThenIsCancelled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	client, cleanup := startRedis(t, ctx)
	defer cleanup()

	qc := queuecancel.New(client)

	n, err := qc.Cancel(ctx, []string{"id-1", "id-2"}, time.Hour)
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	for _, id := range []string{"id-1", "id-2"} {
		got, err := qc.IsCancelled(ctx, id)
		require.NoError(t, err)
		assert.True(t, got, "id %s должен быть отменён", id)
	}

	// Неизвестный ID — не отменён.
	got, err := qc.IsCancelled(ctx, "id-unknown")
	require.NoError(t, err)
	assert.False(t, got)

	// TTL проставлен (положительный, не persist).
	ttl, err := client.TTL(ctx, "qcancel:id-1").Result()
	require.NoError(t, err)
	assert.Greater(t, ttl, time.Duration(0))
	assert.LessOrEqual(t, ttl, time.Hour)
}

// TestQueueCancel_EmptyInputs (§34.4): пустой список и пустой id — безопасны.
func TestQueueCancel_EmptyInputs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	client, cleanup := startRedis(t, ctx)
	defer cleanup()

	qc := queuecancel.New(client)

	n, err := qc.Cancel(ctx, nil, time.Hour)
	require.NoError(t, err)
	assert.Equal(t, 0, n)

	got, err := qc.IsCancelled(ctx, "")
	require.NoError(t, err)
	assert.False(t, got)
}

// TestQueueCancel_DefaultTTL (§34.4): ttl<=0 → дефолт 7 суток (ключ не вечный).
func TestQueueCancel_DefaultTTL(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	client, cleanup := startRedis(t, ctx)
	defer cleanup()

	qc := queuecancel.New(client)
	_, err := qc.Cancel(ctx, []string{"id-ttl"}, 0)
	require.NoError(t, err)

	ttl, err := client.TTL(ctx, "qcancel:id-ttl").Result()
	require.NoError(t, err)
	assert.Greater(t, ttl, 6*24*time.Hour)
}

// TestQueueCancel_SignalOnlyByBump (§113.3, ревизия §113): на настоящем Redis
// отметки отмены сигнал планировщика очереди паузы не двигают — его двигает
// только Bump, который Web зовёт один раз на операцию у узла на паузе.
func TestQueueCancel_SignalOnlyByBump(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	client, cleanup := startRedis(t, ctx)
	defer cleanup()

	qc := queuecancel.New(client)

	before, err := qc.Signal(ctx)
	require.NoError(t, err)
	assert.Zero(t, before)

	_, err = qc.Cancel(ctx, []string{"sig-1", "sig-2"}, time.Hour)
	require.NoError(t, err)
	got, err := qc.IsCancelled(ctx, "sig-2")
	require.NoError(t, err)
	assert.True(t, got)
	after, err := qc.Signal(ctx)
	require.NoError(t, err)
	assert.Zero(t, after, "отметки сигнал не двигают — его двигает Bump (ревизия §113)")

	bumped, err := qc.Bump(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), bumped)
	now, err := qc.Signal(ctx)
	require.NoError(t, err)
	assert.Equal(t, bumped, now)
}
