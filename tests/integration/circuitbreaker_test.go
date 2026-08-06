//go:build integration

package integration

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/circuitbreaker"
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

// TestCircuitBreaker_HalfOpen_SingleProbe — в half_open проходит ровно один
// пробный запрос; конкурентные Allow после истечения cooldown не пролезают
// «пробными» все разом (атомарность через Lua, Phase AUD.2).
func TestCircuitBreaker_HalfOpen_SingleProbe(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: skipping sleep-based cooldown test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	client, cleanup := startRedis(t, ctx)
	defer cleanup()

	cb := circuitbreaker.New(client, 2, 200*time.Millisecond)
	key := "node-single-probe"

	require.NoError(t, cb.RecordFailure(ctx, key))
	require.NoError(t, cb.RecordFailure(ctx, key))
	time.Sleep(300 * time.Millisecond) // cooldown истёк

	// 20 конкурентных Allow: ровно один должен получить true.
	const n = 20
	results := make(chan bool, n)
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := cb.Allow(ctx, key)
			assert.NoError(t, err)
			results <- ok
		}()
	}
	wg.Wait()
	close(results)

	allowed := 0
	for ok := range results {
		if ok {
			allowed++
		}
	}
	assert.Equal(t, 1, allowed, "после cooldown проходит ровно один пробный запрос")

	st, err := cb.State(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, circuitbreaker.StateHalfOpen, st)

	// Пока судьба пробного не решена — последующие Allow отбрасываются.
	ok, err := cb.Allow(ctx, key)
	require.NoError(t, err)
	assert.False(t, ok, "half_open: до RecordSuccess/Failure новые запросы не проходят")

	// Успех пробного → closed, трафик снова идёт.
	require.NoError(t, cb.RecordSuccess(ctx, key))
	ok, err = cb.Allow(ctx, key)
	require.NoError(t, err)
	assert.True(t, ok)
}

// TestCircuitBreaker_HalfOpen_ProbeFailureReopens — провал пробного запроса
// возвращает breaker в open (и сбрасывает probe для следующего цикла).
func TestCircuitBreaker_HalfOpen_ProbeFailureReopens(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: skipping sleep-based cooldown test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	client, cleanup := startRedis(t, ctx)
	defer cleanup()

	cb := circuitbreaker.New(client, 2, 200*time.Millisecond)
	key := "node-probe-fail"

	require.NoError(t, cb.RecordFailure(ctx, key))
	require.NoError(t, cb.RecordFailure(ctx, key))
	time.Sleep(300 * time.Millisecond)

	ok, err := cb.Allow(ctx, key)
	require.NoError(t, err)
	require.True(t, ok, "пробный должен пройти")

	// Пробный провалился → снова open, cooldown заводится заново.
	require.NoError(t, cb.RecordFailure(ctx, key))
	st, err := cb.State(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, circuitbreaker.StateOpen, st)

	ok, err = cb.Allow(ctx, key)
	require.NoError(t, err)
	assert.False(t, ok, "свежий open → запросы отбрасываются до нового cooldown")

	// Следующий цикл: cooldown истёк → снова ровно один пробный.
	time.Sleep(300 * time.Millisecond)
	ok, err = cb.Allow(ctx, key)
	require.NoError(t, err)
	assert.True(t, ok, "после повторного cooldown probe-счётчик должен быть сброшен")
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

// TestCircuitBreaker_AdminResetE2E — §81.4: оператор снимает блокировку вручную.
// На реальном Redis, потому что здесь важны настоящие pipeline/PTTL, а не их
// эмуляция: Snapshot и Reset выполняются одной транзакцией.
func TestCircuitBreaker_AdminResetE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	client, cleanup := startRedis(t, ctx)
	defer cleanup()

	const (
		threshold = 3
		cooldown  = 30 * time.Second // заведомо не истечёт за время теста
	)
	cb := circuitbreaker.New(client, threshold, cooldown)
	adm := circuitbreaker.NewAdmin(client)

	for range threshold {
		require.NoError(t, cb.RecordFailure(ctx, "node-admin"))
	}
	ok, err := cb.Allow(ctx, "node-admin")
	require.NoError(t, err)
	require.False(t, ok, "предусловие: breaker открыт")

	// Web читает состояние, записанное Sender'ом: тот же ключ, та же политика.
	snap, err := adm.Snapshot(ctx, "node-admin")
	require.NoError(t, err)
	assert.True(t, snap.Exists)
	assert.Equal(t, circuitbreaker.StateOpen, snap.State)
	assert.Equal(t, threshold, snap.Failures)
	assert.Equal(t, threshold, snap.Threshold)
	assert.Equal(t, cooldown, snap.Cooldown)
	assert.Greater(t, snap.RetryAfter, time.Duration(0))
	assert.Greater(t, snap.TTL, time.Duration(0), "ключ живёт под TTL")

	before, err := adm.Reset(ctx, "node-admin")
	require.NoError(t, err)
	assert.Equal(t, circuitbreaker.StateOpen, before.State, "возвращается состояние ДО сброса")

	ok, err = cb.Allow(ctx, "node-admin")
	require.NoError(t, err)
	assert.True(t, ok, "доставка возобновляется немедленно, не дожидаясь cooldown")

	after, err := adm.Snapshot(ctx, "node-admin")
	require.NoError(t, err)
	assert.False(t, after.Exists)
	assert.Equal(t, circuitbreaker.StateClosed, after.State)
}
