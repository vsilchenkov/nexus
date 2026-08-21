package redislock_test

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/redislock"
)

func newClient(t *testing.T) (*goredis.Client, *miniredis.Miniredis) {
	t.Helper()
	srv := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: srv.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return client, srv
}

// TestLockTryLock (§93.7): цикл достаётся первому, второй его не получает.
func TestLockTryLock(t *testing.T) {
	t.Parallel()

	client, _ := newClient(t)
	first := redislock.New(client, "job")
	second := redislock.New(client, "job")
	ctx := context.Background()

	ok, err := first.TryLock(ctx, time.Minute)
	require.NoError(t, err)
	assert.True(t, ok, "свободный лок берётся")

	ok, err = second.TryLock(ctx, time.Minute)
	require.NoError(t, err)
	assert.False(t, ok, "занятый лок второй реплике не достаётся")
}

// TestLockExpires (§93.7): лок снимается по TTL — вручную его не отпускают,
// чтобы не удалить чужой после долгой паузы.
func TestLockExpires(t *testing.T) {
	t.Parallel()

	client, srv := newClient(t)
	lock := redislock.New(client, "job")
	ctx := context.Background()

	ok, err := lock.TryLock(ctx, time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	srv.FastForward(2 * time.Minute)

	ok, err = redislock.New(client, "job").TryLock(ctx, time.Minute)
	require.NoError(t, err)
	assert.True(t, ok, "после истечения TTL лок свободен")
}

// TestLockWithoutClient (§93.7): без Redis цикл разрешён.
//
// Иначе выключенный Redis тихо остановил бы всю периодическую уборку — отказ,
// который никак себя не проявляет, пока не кончится место на диске.
func TestLockWithoutClient(t *testing.T) {
	t.Parallel()

	ok, err := redislock.New(nil, "job").TryLock(context.Background(), time.Minute)
	require.NoError(t, err)
	assert.True(t, ok)
}

// TestLeaseAcquireAndRenew (§93.8): владелец продлевает свою аренду, а чужой её
// не получает.
func TestLeaseAcquireAndRenew(t *testing.T) {
	t.Parallel()

	client, srv := newClient(t)
	mine := redislock.NewLease(client, "web-1")
	theirs := redislock.NewLease(client, "web-2")
	ctx := context.Background()

	ok, err := mine.Acquire(ctx, "node:1", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	// Чужая реплика не отбирает.
	ok, err = theirs.Acquire(ctx, "node:1", time.Minute)
	require.NoError(t, err)
	assert.False(t, ok)

	// Своя — продлевает: сдвигаем время почти к границе, продлеваем, и после
	// исходного срока аренда всё ещё наша.
	srv.FastForward(50 * time.Second)
	ok, err = mine.Acquire(ctx, "node:1", time.Minute)
	require.NoError(t, err)
	require.True(t, ok, "владелец продлевает свою аренду")

	srv.FastForward(30 * time.Second)
	ok, err = theirs.Acquire(ctx, "node:1", time.Minute)
	require.NoError(t, err)
	assert.False(t, ok, "продлённая аренда не истекла на исходном сроке")
}

// TestLeaseExpiresToOtherReplica (§93.8): упавший ведущий отдаёт узел соседу
// не позже TTL.
func TestLeaseExpiresToOtherReplica(t *testing.T) {
	t.Parallel()

	client, srv := newClient(t)
	ctx := context.Background()

	ok, err := redislock.NewLease(client, "web-1").Acquire(ctx, "node:1", 30*time.Second)
	require.NoError(t, err)
	require.True(t, ok)

	srv.FastForward(31 * time.Second)

	ok, err = redislock.NewLease(client, "web-2").Acquire(ctx, "node:1", 30*time.Second)
	require.NoError(t, err)
	assert.True(t, ok, "после TTL узел подхватывает соседняя реплика")
}

// TestLeaseReleaseOnlyOwn (§93.8): отдать можно только свою аренду.
//
// Слепой DEL снёс бы чужую: наша могла истечь, пока мы останавливались, и узел
// уже перехватил сосед — удалив его аренду, мы отдали бы узел третьей реплике
// (или себе же, которая уже уходит).
func TestLeaseReleaseOnlyOwn(t *testing.T) {
	t.Parallel()

	client, _ := newClient(t)
	mine := redislock.NewLease(client, "web-1")
	theirs := redislock.NewLease(client, "web-2")
	ctx := context.Background()

	ok, err := mine.Acquire(ctx, "node:1", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	// Чужой Release не трогает наш ключ.
	require.NoError(t, theirs.Release(ctx, "node:1"))
	ok, err = theirs.Acquire(ctx, "node:1", time.Minute)
	require.NoError(t, err)
	assert.False(t, ok, "чужой Release не должен освобождать аренду")

	// Свой — освобождает сразу, не дожидаясь TTL.
	require.NoError(t, mine.Release(ctx, "node:1"))
	ok, err = theirs.Acquire(ctx, "node:1", time.Minute)
	require.NoError(t, err)
	assert.True(t, ok, "после отдачи аренду забирает сосед")
}

// TestLeaseWithoutOwner (§93.8): в одиночной установке (владелец не определён)
// аренда всегда наша и ключей не создаёт.
func TestLeaseWithoutOwner(t *testing.T) {
	t.Parallel()

	client, srv := newClient(t)
	lease := redislock.NewLease(client, "")
	ctx := context.Background()

	ok, err := lease.Acquire(ctx, "node:1", time.Minute)
	require.NoError(t, err)
	assert.True(t, ok)
	require.NoError(t, lease.Release(ctx, "node:1"))

	assert.Empty(t, srv.Keys(), "без владельца лок в Redis не пишется")
}

// TestLeaseNilClient (§93.8): без Redis работа не блокируется.
func TestLeaseNilClient(t *testing.T) {
	t.Parallel()

	lease := redislock.NewLease(nil, "web-1")
	ok, err := lease.Acquire(context.Background(), "node:1", time.Minute)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.NoError(t, lease.Release(context.Background(), "node:1"))
}
