package nodecache

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// stepClock — детерминированные часы для проверки TTL без real sleep.
type stepClock struct {
	mu  sync.Mutex
	now time.Time
}

func newStepClock(start time.Time) *stepClock { return &stepClock{now: start} }

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *stepClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestLRU_GetSetTTL(t *testing.T) {
	t.Parallel()

	clk := newStepClock(time.Unix(0, 0))
	c := NewLRU[int](10, 2*time.Second).WithClock(clk)

	c.Set("a", 1)
	v, ok := c.Get("a")
	require.True(t, ok)
	require.Equal(t, 1, v)

	clk.Advance(1500 * time.Millisecond)
	v, ok = c.Get("a")
	require.True(t, ok, "still within TTL")
	require.Equal(t, 1, v)

	clk.Advance(600 * time.Millisecond) // total 2.1s > 2s TTL
	_, ok = c.Get("a")
	require.False(t, ok, "expired")
}

func TestLRU_OverwriteResetsTTL(t *testing.T) {
	t.Parallel()

	clk := newStepClock(time.Unix(0, 0))
	c := NewLRU[int](10, time.Second).WithClock(clk)

	c.Set("a", 1)
	clk.Advance(800 * time.Millisecond)
	c.Set("a", 2) // overwrite resets TTL
	clk.Advance(900 * time.Millisecond)

	v, ok := c.Get("a")
	require.True(t, ok)
	require.Equal(t, 2, v)
}

func TestLRU_EvictsLeastRecentlyUsed(t *testing.T) {
	t.Parallel()

	var evictions atomic.Int64
	clk := newStepClock(time.Unix(0, 0))
	c := NewLRU[int](3, 10*time.Second).WithClock(clk).WithOnEvict(func() {
		evictions.Add(1)
	})

	c.Set("a", 1)
	c.Set("b", 2)
	c.Set("c", 3)

	// touch a → b is now LRU
	_, _ = c.Get("a")
	c.Set("d", 4) // evicts "b"

	_, okA := c.Get("a")
	_, okB := c.Get("b")
	_, okC := c.Get("c")
	_, okD := c.Get("d")
	require.True(t, okA)
	require.False(t, okB, "b should be evicted")
	require.True(t, okC)
	require.True(t, okD)
	require.EqualValues(t, 1, evictions.Load())
}

func TestLRU_GetStaleReturnsExpired(t *testing.T) {
	t.Parallel()

	clk := newStepClock(time.Unix(0, 0))
	c := NewLRU[string](5, time.Second).WithClock(clk)

	c.Set("k", "v")
	clk.Advance(3 * time.Second)

	v, fresh, age, ok := c.GetStale("k")
	require.True(t, ok)
	require.False(t, fresh)
	require.Equal(t, "v", v)
	require.Equal(t, 3*time.Second, age)

	v, fresh, age, ok = c.GetStale("missing")
	require.False(t, ok)
	require.False(t, fresh)
	require.Equal(t, time.Duration(0), age)
	require.Empty(t, v)
}

func TestLRU_Delete(t *testing.T) {
	t.Parallel()
	c := NewLRU[int](5, time.Minute)
	c.Set("a", 1)
	c.Delete("a")
	_, ok := c.Get("a")
	require.False(t, ok)
	require.Equal(t, 0, c.Len())
}

func TestLRU_Concurrent(t *testing.T) {
	t.Parallel()
	c := NewLRU[int](128, time.Minute)
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := range 200 {
				key := string(rune('a' + (id+j)%26))
				c.Set(key, j)
				_, _ = c.Get(key)
			}
		}(i)
	}
	wg.Wait()
	require.LessOrEqual(t, c.Len(), 128)
}

func TestLRU_PanicsOnBadInputs(t *testing.T) {
	t.Parallel()
	require.Panics(t, func() { NewLRU[int](0, time.Second) })
	require.Panics(t, func() { NewLRU[int](10, 0) })
}
