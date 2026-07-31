package ratelimit_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/ratelimit"
)

// newLimiter поднимает in-memory Redis (miniredis) и лимитер поверх него.
// Реальный Redis для этих проверок не нужен: используются только INCR/EXPIRE.
func newLimiter(t *testing.T, opts ...ratelimit.Option) (*ratelimit.Limiter, *miniredis.Miniredis) {
	t.Helper()

	srv := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: srv.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	return ratelimit.New(client, opts...), srv
}

// countingSink считает вызовы IncRateLimitCheckError — реализация ErrorSink
// на стороне теста (в проде это metrics.Metrics).
type countingSink struct {
	scopes []string
}

func (s *countingSink) IncRateLimitCheckError(scope string) { s.scopes = append(s.scopes, scope) }

func TestAllow_WithinAndOverLimit(t *testing.T) {
	t.Parallel()

	l, _ := newLimiter(t)
	ctx := context.Background()

	// limit=3: первые три запроса проходят, четвёртый — нет.
	for i := 1; i <= 3; i++ {
		ok, err := l.Allow(ctx, "login:user1", 3)
		require.NoError(t, err)
		assert.True(t, ok, "запрос %d должен пройти", i)
	}

	ok, err := l.Allow(ctx, "login:user1", 3)
	require.NoError(t, err)
	assert.False(t, ok, "четвёртый запрос в окне обязан быть отклонён")
}

func TestAllow_ZeroLimitMeansUnlimited(t *testing.T) {
	t.Parallel()

	l, srv := newLimiter(t)
	ctx := context.Background()

	for range 100 {
		ok, err := l.Allow(ctx, "replay:user1", 0)
		require.NoError(t, err)
		require.True(t, ok)
	}
	// limit<=0 не должен даже трогать Redis.
	assert.Empty(t, srv.Keys(), "при limit=0 ключи в Redis не создаются")
}

func TestAllow_KeysAreIsolatedAndExpire(t *testing.T) {
	t.Parallel()

	l, srv := newLimiter(t)
	ctx := context.Background()

	// Разные ключи не мешают друг другу.
	okA, err := l.Allow(ctx, "login:userA", 1)
	require.NoError(t, err)
	okB, err := l.Allow(ctx, "login:userB", 1)
	require.NoError(t, err)
	assert.True(t, okA)
	assert.True(t, okB)

	minute := time.Now().UTC().Unix() / 60
	key := fmt.Sprintf("ratelimit:login:userA:%d", minute)
	require.Contains(t, srv.Keys(), key)

	// TTL 65 с: чуть больше окна, чтобы счётчик пережил стык минуты и не
	// остался в Redis навсегда.
	assert.Equal(t, 65*time.Second, srv.TTL(key))
}

func TestAllow_WindowResetsWithMinute(t *testing.T) {
	t.Parallel()

	l, srv := newLimiter(t)
	ctx := context.Background()

	ok, err := l.Allow(ctx, "kafka:peek", 1)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = l.Allow(ctx, "kafka:peek", 1)
	require.NoError(t, err)
	require.False(t, ok, "лимит исчерпан в текущем окне")

	// Ключ окна содержит номер минуты — следующая минута начинает счёт заново.
	// Эмулируем истечение ключа (miniredis двигает своё время сам).
	srv.FastForward(70 * time.Second)
	ok, err = l.Allow(ctx, "kafka:peek", 1)
	require.NoError(t, err)
	assert.True(t, ok, "после смены окна лимит начинается заново")
}

// §9.4: при недоступном Redis лимиты fail-open — запрос пропускается, но факт
// учитывается в ErrorSink, иначе отключение лимитов остаётся невидимым.
func TestAllow_FailOpenOnRedisDown(t *testing.T) {
	t.Parallel()

	sink := &countingSink{}
	l, srv := newLimiter(t, ratelimit.WithErrorSink(sink))
	srv.Close() // Redis «упал»

	ok, err := l.Allow(context.Background(), "login:user1", 1)
	assert.True(t, ok, "fail-open: при сбое Redis запрос пропускается")
	require.Error(t, err, "ошибка возвращается вызывающему, а не проглатывается")
	assert.Equal(t, []string{"login"}, sink.scopes,
		"в метрику идёт scope (префикс до ':'), а не весь ключ — иначе кардинальность метрики растёт по пользователям")
}

func TestAllow_FailOpenWithoutSinkDoesNotPanic(t *testing.T) {
	t.Parallel()

	l, srv := newLimiter(t) // без WithErrorSink
	srv.Close()

	ok, err := l.Allow(context.Background(), "login:user1", 1)
	assert.True(t, ok)
	assert.Error(t, err)
}
