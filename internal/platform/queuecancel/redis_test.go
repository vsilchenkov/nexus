package queuecancel_test

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/queuecancel"
)

func newCancelSet(t *testing.T) (*queuecancel.Redis, *miniredis.Miniredis) {
	t.Helper()

	srv := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: srv.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	return queuecancel.New(client), srv
}

func TestCancelThenIsCancelled(t *testing.T) {
	t.Parallel()

	cs, _ := newCancelSet(t)
	ctx := context.Background()

	n, err := cs.Cancel(ctx, []string{"id-1", "id-2"}, time.Hour)
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	for _, id := range []string{"id-1", "id-2"} {
		cancelled, err := cs.IsCancelled(ctx, id)
		require.NoError(t, err)
		assert.True(t, cancelled, "id %s должен быть отменён", id)
	}

	// Чужой ID набор не трогает.
	cancelled, err := cs.IsCancelled(ctx, "id-3")
	require.NoError(t, err)
	assert.False(t, cancelled)
}

// TTL tombstone'а = retention топика: ключ обязан самоистечь вместе с
// физическим устареванием сообщения в Kafka, иначе набор растёт бесконечно.
func TestCancelTTL(t *testing.T) {
	t.Parallel()

	cs, srv := newCancelSet(t)
	ctx := context.Background()

	_, err := cs.Cancel(ctx, []string{"id-ttl"}, 2*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, 2*time.Hour, srv.TTL("qcancel:id-ttl"))

	// ttl<=0 → дефолт 7 суток (а не «навсегда»).
	_, err = cs.Cancel(ctx, []string{"id-default"}, 0)
	require.NoError(t, err)
	assert.Equal(t, 7*24*time.Hour, srv.TTL("qcancel:id-default"))

	srv.FastForward(3 * time.Hour)
	cancelled, err := cs.IsCancelled(ctx, "id-ttl")
	require.NoError(t, err)
	assert.False(t, cancelled, "после истечения TTL сообщение снова доставляемо")
}

func TestCancelIsIdempotentAndSkipsEmptyIDs(t *testing.T) {
	t.Parallel()

	cs, srv := newCancelSet(t)
	ctx := context.Background()

	_, err := cs.Cancel(ctx, []string{"id-1", "", "id-1"}, time.Hour)
	require.NoError(t, err)

	assert.Equal(t, []string{"qcancel:id-1"}, srv.Keys(),
		"пустой ID не должен создавать ключ, повтор — дублировать его")
}

func TestEmptyInputs(t *testing.T) {
	t.Parallel()

	cs, srv := newCancelSet(t)
	ctx := context.Background()

	n, err := cs.Cancel(ctx, nil, time.Hour)
	require.NoError(t, err)
	assert.Zero(t, n)

	cancelled, err := cs.IsCancelled(ctx, "")
	require.NoError(t, err)
	assert.False(t, cancelled)
	assert.Empty(t, srv.Keys())
}

// При недоступном Redis обе операции возвращают ошибку вызывающему: Sender
// трактует её как fail-open и доставляет сообщение (§9.4) — но молча
// «не отменено» возвращать нельзя, иначе сбой Redis выглядит как решение.
func TestRedisDownReturnsError(t *testing.T) {
	t.Parallel()

	cs, srv := newCancelSet(t)
	srv.Close()
	ctx := context.Background()

	_, err := cs.Cancel(ctx, []string{"id-1"}, time.Hour)
	assert.Error(t, err)

	cancelled, err := cs.IsCancelled(ctx, "id-1")
	assert.Error(t, err)
	assert.False(t, cancelled)
}
