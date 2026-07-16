package logsink

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type shipCall struct {
	key   string
	lines [][]byte
	capN  int64
	ttl   time.Duration
}

// fakeWriter — записывающий BatchWriter: копит вызовы, умеет возвращать
// ошибку и паниковать один раз.
type fakeWriter struct {
	mu        sync.Mutex
	calls     []shipCall
	err       error
	panicOnce bool
}

func (w *fakeWriter) Ship(_ context.Context, key string, lines [][]byte, capN int64, ttl time.Duration) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.panicOnce {
		w.panicOnce = false
		panic("writer boom")
	}
	if w.err != nil {
		return w.err
	}
	cp := make([][]byte, len(lines))
	copy(cp, lines)
	w.calls = append(w.calls, shipCall{key: key, lines: cp, capN: capN, ttl: ttl})
	return nil
}

func (w *fakeWriter) snapshot() []shipCall {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]shipCall, len(w.calls))
	copy(out, w.calls)
	return out
}

func (w *fakeWriter) totalLines() int {
	n := 0
	for _, c := range w.snapshot() {
		n += len(c.lines)
	}
	return n
}

// runShipper запускает шиппер в горутине и гарантирует его останов к концу
// теста (goleak).
func runShipper(t *testing.T, s *Shipper) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Run(ctx)
	}()
	stop = func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("shipper did not stop after ctx cancel")
		}
	}
	t.Cleanup(stop)
	return stop
}

func entry(msg string) Entry {
	return Entry{TS: time.Unix(1700000000, 0).UTC(), Level: "info", Service: "web", Msg: msg}
}

func TestShipper_BatchesBySize(t *testing.T) {
	t.Parallel()
	w := &fakeWriter{}
	ch := make(chan Entry, 16)
	s := NewShipper(w, ch, "web", WithBatchSize(3), WithFlushInterval(time.Hour))
	runShipper(t, s)

	for _, m := range []string{"a", "b", "c"} {
		ch <- entry(m)
	}
	require.Eventually(t, func() bool { return len(w.snapshot()) == 1 }, 3*time.Second, 10*time.Millisecond)
	assert.Len(t, w.snapshot()[0].lines, 3)
}

func TestShipper_FlushesByInterval(t *testing.T) {
	t.Parallel()
	w := &fakeWriter{}
	ch := make(chan Entry, 16)
	s := NewShipper(w, ch, "web", WithBatchSize(1000), WithFlushInterval(20*time.Millisecond))
	runShipper(t, s)

	ch <- entry("a")
	ch <- entry("b")
	require.Eventually(t, func() bool { return w.totalLines() == 2 }, 3*time.Second, 10*time.Millisecond)
}

func TestShipper_PassesKeyCapTTL(t *testing.T) {
	t.Parallel()
	w := &fakeWriter{}
	ch := make(chan Entry, 4)
	s := NewShipper(w, ch, "receiver",
		WithBatchSize(1), WithKeyCap(50), WithTTL(time.Minute))
	runShipper(t, s)

	ch <- entry("a")
	require.Eventually(t, func() bool { return len(w.snapshot()) == 1 }, 3*time.Second, 10*time.Millisecond)

	call := w.snapshot()[0]
	assert.Equal(t, "nexus:logs:receiver", call.key)
	assert.Equal(t, int64(50), call.capN)
	assert.Equal(t, time.Minute, call.ttl)
}

func TestShipper_StopsOnCtxCancelAndDrainsTail(t *testing.T) {
	t.Parallel()
	w := &fakeWriter{}
	ch := make(chan Entry, 16)
	// Большой батч и редкий тик: до отмены ctx ничего не уедет само.
	s := NewShipper(w, ch, "web", WithBatchSize(1000), WithFlushInterval(time.Hour))
	stop := runShipper(t, s)

	for _, m := range []string{"a", "b", "c", "d"} {
		ch <- entry(m)
	}
	stop() // отмена ctx: шиппер обязан дослать хвост и завершиться

	assert.Equal(t, 4, w.totalLines(), "tail must be flushed on shutdown")
}

func TestShipper_SurvivesWriterPanic(t *testing.T) {
	t.Parallel()
	w := &fakeWriter{panicOnce: true}
	ch := make(chan Entry, 16)
	s := NewShipper(w, ch, "web", WithBatchSize(1), WithFlushInterval(time.Hour))
	runShipper(t, s)

	ch <- entry("boom") // первый батч — паника писателя
	require.Eventually(t, func() bool { return s.ShipErrors() == 1 }, 3*time.Second, 10*time.Millisecond)

	ch <- entry("after") // шиппер жив, следующий батч доставлен
	require.Eventually(t, func() bool { return w.totalLines() == 1 }, 3*time.Second, 10*time.Millisecond)
	assert.Equal(t, uint64(1), s.ShipErrors())
}

func TestShipper_WriterErrorDoesNotBlock(t *testing.T) {
	t.Parallel()
	w := &fakeWriter{err: errors.New("redis down")}
	ch := make(chan Entry, 4)
	s := NewShipper(w, ch, "web", WithBatchSize(1), WithFlushInterval(time.Hour))
	runShipper(t, s)

	// Записей больше ёмкости канала: живой шиппер обязан выгребать канал
	// даже при постоянной ошибке писателя — отправка не блокируется.
	for i := range 20 {
		select {
		case ch <- entry("m"):
		case <-time.After(3 * time.Second):
			t.Fatalf("send %d blocked: shipper stopped draining on writer errors", i)
		}
	}
	require.Eventually(t, func() bool { return s.ShipErrors() >= 20 }, 3*time.Second, 10*time.Millisecond)
	assert.Empty(t, w.snapshot())
}
