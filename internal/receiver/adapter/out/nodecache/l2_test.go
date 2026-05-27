package nodecache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/receiver/usecase/port"
)

// fakeReader реализует port.NodeReader. Хранит ответы по path и счётчик вызовов.
type fakeReader struct {
	mu    sync.Mutex
	calls int
	resp  map[string]*domain.Node
	err   error
}

var _ port.NodeReader = (*fakeReader)(nil)

func (f *fakeReader) Get(_ context.Context, _, path string) (*domain.Node, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if n, ok := f.resp[path]; ok {
		return n, nil
	}
	return nil, domain.ErrNodeNotFound
}

func (f *fakeReader) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type stubMetrics struct {
	fresh, stale, miss, evict atomic.Int64
	size                      atomic.Int64
}

func (s *stubMetrics) IncL2Hit(kind string) {
	if kind == "fresh" {
		s.fresh.Add(1)
	} else {
		s.stale.Add(1)
	}
}
func (s *stubMetrics) IncL2Miss()      { s.miss.Add(1) }
func (s *stubMetrics) IncL2Eviction()  { s.evict.Add(1) }
func (s *stubMetrics) SetL2Size(n int) { s.size.Store(int64(n)) }

func TestL2_DisabledReturnsInner(t *testing.T) {
	t.Parallel()
	inner := &fakeReader{}
	got := NewL2(inner, L2Config{Enabled: false}, logging.NewNoop(), nil)
	require.Same(t, port.NodeReader(inner), got, "Enabled=false → inner returned as-is")
}

func TestL2_FreshHit(t *testing.T) {
	t.Parallel()
	inner := &fakeReader{resp: map[string]*domain.Node{
		"foo": {Path: "foo"},
	}}
	mx := &stubMetrics{}
	r := NewL2(inner, L2Config{Enabled: true, Size: 10, TTL: time.Minute, StaleTTL: time.Minute}, logging.NewNoop(), mx)

	n, err := r.Get(context.Background(), "", "foo")
	require.NoError(t, err)
	require.Equal(t, "foo", n.Path)
	require.Equal(t, 1, inner.Calls(), "first call hits inner")

	for i := 0; i < 5; i++ {
		n, err := r.Get(context.Background(), "", "foo")
		require.NoError(t, err)
		require.Equal(t, "foo", n.Path)
	}
	require.Equal(t, 1, inner.Calls(), "subsequent calls served from L2")
	require.EqualValues(t, 5, mx.fresh.Load())
	require.EqualValues(t, 1, mx.miss.Load())
	require.EqualValues(t, 1, mx.size.Load())
}

func TestL2_NotFoundIsNotCached(t *testing.T) {
	t.Parallel()
	inner := &fakeReader{} // empty resp → ErrNodeNotFound
	mx := &stubMetrics{}
	r := NewL2(inner, L2Config{Enabled: true, Size: 10, TTL: time.Minute, StaleTTL: time.Minute}, logging.NewNoop(), mx)

	for i := 0; i < 3; i++ {
		_, err := r.Get(context.Background(), "", "missing")
		require.ErrorIs(t, err, domain.ErrNodeNotFound)
	}
	require.Equal(t, 3, inner.Calls(), "ErrNodeNotFound must NOT be cached")
}

func TestL2_StaleFallbackOnDownstreamError(t *testing.T) {
	t.Parallel()
	clk := newStepClock(time.Unix(0, 0))
	inner := &fakeReader{resp: map[string]*domain.Node{
		"hot": {Path: "hot"},
	}}
	mx := &stubMetrics{}
	cfg := L2Config{Enabled: true, Size: 10, TTL: time.Second, StaleTTL: 30 * time.Second}
	rPort := NewL2(inner, cfg, logging.NewNoop(), mx)
	r := rPort.(*L2Reader)
	r.cache.WithClock(clk)

	// 1) Warm-up — fresh hit caches the entry.
	_, err := r.Get(context.Background(), "", "hot")
	require.NoError(t, err)

	// 2) Advance past TTL so the next call sees a miss in fresh cache,
	// AND simulate downstream outage (PG+Redis both down).
	clk.Advance(2 * time.Second)
	inner.err = errors.New("redis+pg both down")

	got, err := r.Get(context.Background(), "", "hot")
	require.NoError(t, err, "stale-fallback kicks in")
	require.Equal(t, "hot", got.Path)
	require.EqualValues(t, 1, mx.stale.Load())

	// 3) When stale entry exceeds StaleTTL, fallback no longer fires.
	clk.Advance(40 * time.Second)
	_, err = r.Get(context.Background(), "", "hot")
	require.Error(t, err)
}

func TestL2_NoStaleFallbackWhenStaleTTLIsZero(t *testing.T) {
	t.Parallel()
	clk := newStepClock(time.Unix(0, 0))
	inner := &fakeReader{resp: map[string]*domain.Node{
		"hot": {Path: "hot"},
	}}
	mx := &stubMetrics{}
	cfg := L2Config{Enabled: true, Size: 10, TTL: time.Second, StaleTTL: 0}
	rPort := NewL2(inner, cfg, logging.NewNoop(), mx)
	r := rPort.(*L2Reader)
	r.cache.WithClock(clk)

	_, err := r.Get(context.Background(), "", "hot")
	require.NoError(t, err)

	clk.Advance(2 * time.Second)
	inner.err = errors.New("boom")

	_, err = r.Get(context.Background(), "", "hot")
	require.Error(t, err, "StaleTTL=0 → no fallback")
	require.EqualValues(t, 0, mx.stale.Load())
}
