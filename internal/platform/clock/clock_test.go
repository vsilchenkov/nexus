package clock_test

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/clock"
)

func TestSystemReturnsMovingTime(t *testing.T) {
	t.Parallel()

	c := clock.System()
	before := time.Now()
	got := c.Now()
	after := time.Now()

	assert.False(t, got.Before(before))
	assert.False(t, got.After(after))
}

func TestFixedNeverMoves(t *testing.T) {
	t.Parallel()

	want := time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC)
	c := clock.Fixed(want)

	assert.True(t, want.Equal(c.Now()))
	assert.True(t, want.Equal(c.Now()), "повторный вызов отдаёт то же значение")
}

func TestFuncAdapter(t *testing.T) {
	t.Parallel()

	calls := 0
	c := clock.Func(func() time.Time {
		calls++
		return time.Unix(int64(calls), 0).UTC()
	})

	assert.Equal(t, time.Unix(1, 0).UTC(), c.Now())
	assert.Equal(t, time.Unix(2, 0).UTC(), c.Now())
}

func TestFakeAdvanceAndSet(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f := clock.NewFake(start)

	assert.True(t, start.Equal(f.Now()))

	f.Advance(90 * time.Minute)
	assert.True(t, start.Add(90*time.Minute).Equal(f.Now()))

	other := time.Date(2030, 5, 5, 5, 5, 0, 0, time.UTC)
	f.Set(other)
	assert.True(t, other.Equal(f.Now()))
}

// Фоновые воркеры под тестом читают часы из своих горутин, пока тест двигает
// время — гонки здесь ловятся -race в CI.
func TestFakeIsConcurrencySafe(t *testing.T) {
	t.Parallel()

	f := clock.NewFake(time.Unix(0, 0).UTC())

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 100 {
				_ = f.Now()
			}
		})
		wg.Go(func() {
			for range 100 {
				f.Advance(time.Millisecond)
			}
		})
	}
	wg.Wait()

	require.Equal(t, 800*time.Millisecond, f.Now().Sub(time.Unix(0, 0).UTC()))
}
