//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bus/internal/platform/circuitbreaker"
)

// TestCircuitBreaker_HappyPath_Closed — свежий ключ → state=closed, Allow=true.
func TestCircuitBreaker_HappyPath_Closed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	client, cleanup := startRedis(t, ctx)
	defer cleanup()

	cb := circuitbreaker.New(client, 3, 200*time.Millisecond)

	ok, err := cb.Allow(ctx, "node-fresh")
	require.NoError(t, err)
	assert.True(t, ok, "свежий ключ — closed по умолчанию, Allow=true")

	st, err := cb.State(ctx, "node-fresh")
	require.NoError(t, err)
	assert.Equal(t, circuitbreaker.StateClosed, st)
}

// TestCircuitBreaker_OpensOnThreshold — после threshold failures breaker
// переходит в open и Allow возвращает false до истечения cooldown.
func TestCircuitBreaker_OpensOnThreshold(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	client, cleanup := startRedis(t, ctx)
	defer cleanup()

	cb := circuitbreaker.New(client, 3, 500*time.Millisecond)
	key := "node-open"

	// 2 failures — пока ниже threshold → state остаётся closed.
	require.NoError(t, cb.RecordFailure(ctx, key))
	require.NoError(t, cb.RecordFailure(ctx, key))
	st, err := cb.State(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, circuitbreaker.StateClosed, st, "2 < threshold=3 → closed")

	// 3-я failure → open.
	require.NoError(t, cb.RecordFailure(ctx, key))
	st, err = cb.State(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, circuitbreaker.StateOpen, st)

	// Allow в open + cooldown ещё не истёк → false.
	allow, err := cb.Allow(ctx, key)
	require.NoError(t, err)
	assert.False(t, allow, "open + cooldown активен → Allow=false")
}

// TestCircuitBreaker_HalfOpenAfterCooldown — по истечении cooldown первый
// Allow переводит state в half_open и возвращает true.
func TestCircuitBreaker_HalfOpenAfterCooldown(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: skipping sleep-based cooldown test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	client, cleanup := startRedis(t, ctx)
	defer cleanup()

	cb := circuitbreaker.New(client, 2, 200*time.Millisecond)
	key := "node-half"

	// Открываем breaker.
	require.NoError(t, cb.RecordFailure(ctx, key))
	require.NoError(t, cb.RecordFailure(ctx, key))

	// Ждём cooldown + чуть-чуть.
	time.Sleep(300 * time.Millisecond)

	allow, err := cb.Allow(ctx, key)
	require.NoError(t, err)
	assert.True(t, allow, "cooldown прошёл → Allow=true (half_open проба)")

	st, err := cb.State(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, circuitbreaker.StateHalfOpen, st)
}

// TestCircuitBreaker_RecordSuccessClosesBreaker — Success в любом состоянии
// возвращает breaker в closed и сбрасывает failures.
func TestCircuitBreaker_RecordSuccessClosesBreaker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	client, cleanup := startRedis(t, ctx)
	defer cleanup()

	cb := circuitbreaker.New(client, 2, time.Second)
	key := "node-success"

	require.NoError(t, cb.RecordFailure(ctx, key))
	require.NoError(t, cb.RecordFailure(ctx, key))

	st, err := cb.State(ctx, key)
	require.NoError(t, err)
	require.Equal(t, circuitbreaker.StateOpen, st, "должен быть open перед Success")

	require.NoError(t, cb.RecordSuccess(ctx, key))

	st, err = cb.State(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, circuitbreaker.StateClosed, st)

	allow, err := cb.Allow(ctx, key)
	require.NoError(t, err)
	assert.True(t, allow, "после Success — Allow=true немедленно (без cooldown)")
}

// TestCircuitBreaker_KeysIsolated — два разных ключа не влияют друг на друга.
func TestCircuitBreaker_KeysIsolated(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	client, cleanup := startRedis(t, ctx)
	defer cleanup()

	cb := circuitbreaker.New(client, 2, time.Second)

	require.NoError(t, cb.RecordFailure(ctx, "node-A"))
	require.NoError(t, cb.RecordFailure(ctx, "node-A"))

	// node-B должен оставаться closed.
	st, err := cb.State(ctx, "node-B")
	require.NoError(t, err)
	assert.Equal(t, circuitbreaker.StateClosed, st)

	allowB, err := cb.Allow(ctx, "node-B")
	require.NoError(t, err)
	assert.True(t, allowB)

	// node-A — open.
	stA, err := cb.State(ctx, "node-A")
	require.NoError(t, err)
	assert.Equal(t, circuitbreaker.StateOpen, stA)
}

// TestCircuitBreaker_StateOnUnknownKey — для ключа, к которому ничего не
// записывали, State возвращает closed (а не error / пустую строку).
func TestCircuitBreaker_StateOnUnknownKey(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	client, cleanup := startRedis(t, ctx)
	defer cleanup()

	cb := circuitbreaker.New(client, 3, time.Second)

	st, err := cb.State(ctx, "never-seen-before")
	require.NoError(t, err)
	assert.Equal(t, circuitbreaker.StateClosed, st)
}
