package kafka

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestInPlaceRetry_BackoffDoublesUpToMax: паузы между попытками удваиваются от
// min и упираются в max — лежащую зависимость не долбят раз в секунду часами.
func TestInPlaceRetry_BackoffDoublesUpToMax(t *testing.T) {
	t.Parallel()

	var waits []time.Duration
	calls := 0
	ok := inPlaceRetry{min: time.Millisecond, max: 4 * time.Millisecond}.run(context.Background(),
		func() bool { calls++; return calls == 5 },
		func(_ int, wait time.Duration) { waits = append(waits, wait) })

	assert.True(t, ok)
	assert.Equal(t, 5, calls)
	assert.Equal(t, []time.Duration{
		time.Millisecond, 2 * time.Millisecond, 4 * time.Millisecond, 4 * time.Millisecond,
	}, waits)
}

// TestInPlaceRetry_CtxCancelled: отмена ctx прерывает ожидание и возвращает
// false — вызывающий НЕ коммитит сообщение.
func TestInPlaceRetry_CtxCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	ok := inPlaceRetry{min: time.Hour, max: time.Hour}.run(ctx, func() bool { return false }, nil)

	assert.False(t, ok)
	assert.Less(t, time.Since(start), time.Second, "отменённый ctx не ждёт паузу")
}

// TestInPlaceRetry_ZeroMinNoBusyLoop: нулевой min не превращается в busy-loop.
func TestInPlaceRetry_ZeroMinNoBusyLoop(t *testing.T) {
	t.Parallel()

	var waits []time.Duration
	calls := 0
	inPlaceRetry{}.run(context.Background(),
		func() bool { calls++; return calls == 2 },
		func(_ int, wait time.Duration) { waits = append(waits, wait) })

	assert.Equal(t, []time.Duration{time.Millisecond}, waits)
}
