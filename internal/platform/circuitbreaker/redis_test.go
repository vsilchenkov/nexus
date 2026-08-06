package circuitbreaker_test

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/circuitbreaker"
)

// Unit-тесты breaker'а поверх miniredis. Half-open-проба живёт в Lua-скрипте
// (атомарность обязательна: иначе пробных запросов проходит больше одного) —
// miniredis исполняет его встроенным gopher-lua, поэтому проверка возможна без
// настоящего Redis. Сценарии с несколькими инстансами Sender остаются в
// integration-тестах.
func newBreaker(t *testing.T, threshold int, cooldown time.Duration) (*circuitbreaker.Breaker, *miniredis.Miniredis) {
	t.Helper()

	srv := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: srv.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	return circuitbreaker.New(client, threshold, cooldown), srv
}

func TestAllow_ClosedByDefault(t *testing.T) {
	t.Parallel()

	b, _ := newBreaker(t, 3, time.Minute)
	ok, err := b.Allow(context.Background(), "node/a")
	require.NoError(t, err)
	assert.True(t, ok, "незнакомый ключ = closed")
}

func TestOpensAtThreshold(t *testing.T) {
	t.Parallel()

	b, _ := newBreaker(t, 3, time.Minute)
	ctx := context.Background()

	// Две ошибки порога не достигают.
	require.NoError(t, b.RecordFailure(ctx, "node/a", domain.BreakerPolicy{}))
	require.NoError(t, b.RecordFailure(ctx, "node/a", domain.BreakerPolicy{}))
	ok, err := b.Allow(ctx, "node/a")
	require.NoError(t, err)
	assert.True(t, ok, "ниже порога breaker закрыт")

	// Третья открывает.
	require.NoError(t, b.RecordFailure(ctx, "node/a", domain.BreakerPolicy{}))
	ok, err = b.Allow(ctx, "node/a")
	require.NoError(t, err)
	assert.False(t, ok, "на пороге breaker открывается и режет запросы")

	open, err := b.IsOpen(ctx, "node/a")
	require.NoError(t, err)
	assert.True(t, open)
}

func TestRecordSuccessCloses(t *testing.T) {
	t.Parallel()

	b, _ := newBreaker(t, 2, time.Minute)
	ctx := context.Background()

	require.NoError(t, b.RecordFailure(ctx, "node/a", domain.BreakerPolicy{}))
	require.NoError(t, b.RecordFailure(ctx, "node/a", domain.BreakerPolicy{}))
	require.NoError(t, b.RecordSuccess(ctx, "node/a"))

	ok, err := b.Allow(ctx, "node/a")
	require.NoError(t, err)
	assert.True(t, ok)

	open, err := b.IsOpen(ctx, "node/a")
	require.NoError(t, err)
	assert.False(t, open)
}

// Ключевое свойство: после cooldown проходит РОВНО ОДИН пробный запрос.
// Пропусти скрипт двоих — на мёртвый адрес полетит лавина ретраев.
func TestHalfOpenLetsExactlyOneProbe(t *testing.T) {
	t.Parallel()

	const cooldown = time.Minute
	b, srv := newBreaker(t, 1, cooldown)
	ctx := context.Background()

	require.NoError(t, b.RecordFailure(ctx, "node/a", domain.BreakerPolicy{}))
	ok, err := b.Allow(ctx, "node/a")
	require.NoError(t, err)
	require.False(t, ok)

	// Cooldown ещё не истёк — по-прежнему закрыто наглухо.
	srv.FastForward(cooldown / 2)
	ok, err = b.Allow(ctx, "node/a")
	require.NoError(t, err)
	assert.False(t, ok, "до истечения cooldown пробы нет")

	// miniredis двигает только своё время; breaker сравнивает time.Now() с
	// opened_at, поэтому «истечение» эмулируем самим значением opened_at.
	srv.HSet("circuit:node/a", "opened_at", "0")

	ok, err = b.Allow(ctx, "node/a")
	require.NoError(t, err)
	assert.True(t, ok, "первый запрос после cooldown идёт пробным")

	for i := range 5 {
		ok, err = b.Allow(ctx, "node/a")
		require.NoError(t, err)
		assert.False(t, ok, "запрос %d после пробы обязан ждать её результата", i+2)
	}

	// IsOpen в half_open отвечает false: адрес не «мёртв прямо сейчас».
	open, err := b.IsOpen(ctx, "node/a")
	require.NoError(t, err)
	assert.False(t, open)
}

// §9.4: сбой Redis не должен резать трафик — breaker fail-open, но ошибку
// возвращает (её учитывают вызывающие).
func TestFailOpenOnRedisDown(t *testing.T) {
	t.Parallel()

	b, srv := newBreaker(t, 1, time.Minute)
	ctx := context.Background()
	require.NoError(t, b.RecordFailure(ctx, "node/a", domain.BreakerPolicy{}))
	srv.Close()

	ok, err := b.Allow(ctx, "node/a")
	assert.True(t, ok, "при недоступном Redis запросы проходят")
	assert.Error(t, err)

	open, err := b.IsOpen(ctx, "node/a")
	assert.False(t, open, "недоступный Redis не считается открытым breaker'ом")
	assert.Error(t, err)
}
