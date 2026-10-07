package kafka

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/clock"
	kafkapf "nexus/internal/platform/kafka"
	"nexus/internal/platform/logging"
)

// fakeInspector — брокер глазами сторожа: позиции и назначения задаёт тест.
type fakeInspector struct {
	mu        sync.Mutex
	pos       []kafkapf.PartitionPosition
	posErr    error
	assigned  map[string][]int
	assignErr error
}

func (f *fakeInspector) GroupPositions(context.Context, string, string) ([]kafkapf.PartitionPosition, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]kafkapf.PartitionPosition(nil), f.pos...), f.posErr
}

func (f *fakeInspector) GroupAssignment(context.Context, string, string) (map[string][]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.assigned, f.assignErr
}

func (f *fakeInspector) set(pos ...kafkapf.PartitionPosition) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pos = pos
}

func part(p int, committed, last int64) kafkapf.PartitionPosition {
	return kafkapf.PartitionPosition{Partition: p, Watermark: kafkapf.Watermark{First: 0, Last: last}, Committed: committed}
}

// fakeTarget — экземпляр группы: client.id, «ждёт с», прочитанные позиции
// (партиция → Offset+1 последнего полученного) и счётчик перезапусков.
type fakeTarget struct {
	id       string
	waiting  time.Time
	fetched  map[int]int64
	restarts int
}

func (f *fakeTarget) ClientID() string        { return f.id }
func (f *fakeTarget) WaitingSince() time.Time { return f.waiting }
func (f *fakeTarget) FetchedNext(p int) (int64, bool) {
	v, ok := f.fetched[p]
	return v, ok
}
func (f *fakeTarget) RequestRestart() bool {
	if f.waiting.IsZero() {
		return false
	}
	f.restarts++
	return true
}

type restartCounter struct{ byReason map[string]int }

func (r *restartCounter) IncKafkaConsumerRestart(_, reason string) {
	if r.byReason == nil {
		r.byReason = map[string]int{}
	}
	r.byReason[reason]++
}

type watchdogFixture struct {
	clk     *clock.Fake
	insp    *fakeInspector
	a, b    *fakeTarget
	metrics *restartCounter
	w       *stallWatchdog
}

const testStallAfter = 2 * time.Minute

// newWatchdogFixture: два своих экземпляра (a — партиция 0, b — партиция 1),
// партиция 2 — у другой реплики. Оба ждут в fetch с момента старта.
func newWatchdogFixture() *watchdogFixture {
	start := time.Unix(10_000, 0)
	f := &watchdogFixture{
		clk:     clock.NewFake(start),
		insp:    &fakeInspector{assigned: map[string][]int{"s1-0-g1": {0}, "s1-1-g1": {1}, "s2-0-g1": {2}}},
		a:       &fakeTarget{id: "s1-0-g1", waiting: start},
		b:       &fakeTarget{id: "s1-1-g1", waiting: start},
		metrics: &restartCounter{},
	}
	guard := StallGuard{Inspector: f.insp, After: testStallAfter, Metrics: f.metrics, Clock: f.clk}
	f.w = newStallWatchdog("g", "t", guard, []stallTarget{f.a, f.b}, logging.NewNoop())
	return f
}

// check двигает часы и делает проверку.
func (f *watchdogFixture) check(advance time.Duration) {
	f.clk.Advance(advance)
	f.w.Check(context.Background())
}

// TestStallWatchdog_RestartsOnlyStalledOwner (§113.6): партиция a стоит с lag,
// a ждёт в fetch дольше порога → перезапуск только a. b без lag не трогается.
func TestStallWatchdog_RestartsOnlyStalledOwner(t *testing.T) {
	t.Parallel()
	f := newWatchdogFixture()
	f.insp.set(part(0, 10, 50), part(1, 20, 20), part(2, 0, 0))

	f.check(0) // первая точка отсчёта
	f.check(time.Minute)
	assert.Zero(t, f.a.restarts, "порог ещё не прошёл")
	f.check(time.Minute + time.Second)

	assert.Equal(t, 1, f.a.restarts)
	assert.Zero(t, f.b.restarts)
	assert.Equal(t, 1, f.metrics.byReason[stallReasonFetch])
}

// TestStallWatchdog_NoRestart — каждая строка снимает одно из условий
// одновременности: без неё перезапуска быть не должно.
func TestStallWatchdog_NoRestart(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		setup func(f *watchdogFixture)
		step  func(f *watchdogFixture) // между двумя проверками
	}{
		{name: "committed движется", step: func(f *watchdogFixture) {
			f.insp.set(part(0, 11, 50), part(1, 20, 20))
		}},
		{name: "lag нет", setup: func(f *watchdogFixture) {
			f.insp.set(part(0, 50, 50), part(1, 20, 20))
		}},
		{name: "стоит партиция другой реплики", setup: func(f *watchdogFixture) {
			f.insp.set(part(0, 50, 50), part(1, 20, 20), part(2, 0, 900))
		}},
		{name: "экземпляр занят обработкой", setup: func(f *watchdogFixture) {
			f.a.waiting = time.Time{}
		}},
		{name: "экземпляр ждёт меньше порога", step: func(f *watchdogFixture) {
			f.a.waiting = f.clk.Now().Add(-time.Second)
		}},
		{name: "позиции недоступны", step: func(f *watchdogFixture) {
			f.insp.posErr = errors.New("dial")
		}},
		{name: "назначения недоступны", step: func(f *watchdogFixture) {
			f.insp.assignErr = errors.New("NOT_COORDINATOR")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newWatchdogFixture()
			f.insp.set(part(0, 10, 50), part(1, 20, 20))
			if tt.setup != nil {
				tt.setup(f)
			}
			f.check(0)
			f.clk.Advance(testStallAfter + time.Second)
			if tt.step != nil {
				tt.step(f)
			}
			f.check(0)
			assert.Zero(t, f.a.restarts+f.b.restarts)
		})
	}
}

// TestStallWatchdog_OrphanPartitionRestartsIdleReaders: партиция с lag не
// назначена никому (группа застряла в ребалансе) — перезапускаются все свои
// экземпляры, ждущие в fetch.
func TestStallWatchdog_OrphanPartitionRestartsIdleReaders(t *testing.T) {
	t.Parallel()
	f := newWatchdogFixture()
	f.insp.assigned = map[string][]int{}
	f.insp.set(part(0, 10, 50), part(1, 20, 20))

	f.check(0)
	f.check(testStallAfter + time.Second)

	assert.Equal(t, 1, f.a.restarts)
	assert.Equal(t, 1, f.b.restarts)
}

// TestStallWatchdog_Cooldown: после перезапуска следующий — не раньше чем
// через stallCooldown, даже если партиция снова стоит.
func TestStallWatchdog_Cooldown(t *testing.T) {
	t.Parallel()
	f := newWatchdogFixture()
	f.insp.set(part(0, 10, 50))

	f.check(0)
	f.check(testStallAfter + time.Second)
	require.Equal(t, 1, f.a.restarts)

	f.a.waiting = f.clk.Now() // новый reader снова висит
	f.check(testStallAfter + time.Second)
	assert.Equal(t, 1, f.a.restarts, "внутри паузы между перезапусками")

	f.check(stallCooldown)
	assert.Equal(t, 2, f.a.restarts)
}

// TestOwnOrOrphanLagging: партиции с lag — свои и ничьи; ничья поднимает orphan.
func TestOwnOrOrphanLagging(t *testing.T) {
	t.Parallel()
	pos := []kafkapf.PartitionPosition{part(0, 0, 5), part(1, 0, 7), part(2, 0, 11), part(3, 4, 4)}
	assigned := map[string][]int{"me": {0, 3}, "other": {1}}

	got, orphan := ownOrOrphanLagging(pos, assigned, "me")
	assert.True(t, orphan)
	assert.EqualValues(t, 5+11, sumLag(got), "партиция 3 без lag не входит")

	got, orphan = ownOrOrphanLagging(pos, map[string][]int{"me": {0, 2}, "other": {1}}, "me")
	assert.False(t, orphan)
	assert.EqualValues(t, 5+11, sumLag(got))
}

// TestStallWatchdog_CaughtUpNotRestarted (§114.2): экземпляр, получивший всё
// до конца лога, при стоящем commit не перезапускается — стоит сохранение
// позиции, а не чтение. Перезапуск остаётся, если прочитано не всё, если
// позиции нет (новый reader ещё ничего не получил) и при ничьей партиции.
func TestStallWatchdog_CaughtUpNotRestarted(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		fetched  map[int]int64
		assigned map[string][]int
		restarts int
	}{
		{name: "дочитал: брокер отвергает commit", fetched: map[int]int64{0: 50}},
		{name: "получил дальше снимка", fetched: map[int]int64{0: 53}},
		{name: "прочитал не всё", fetched: map[int]int64{0: 30}, restarts: 1},
		{name: "из партиции ничего не получал", fetched: map[int]int64{1: 20}, restarts: 1},
		{name: "ничья партиция", fetched: map[int]int64{0: 50},
			assigned: map[string][]int{}, restarts: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newWatchdogFixture()
			if tt.assigned != nil {
				f.insp.assigned = tt.assigned
			}
			f.a.fetched = tt.fetched
			f.insp.set(part(0, 10, 50), part(1, 20, 20)) // committed 10, конец лога 50

			f.check(0)
			f.check(testStallAfter + time.Second)
			assert.Equal(t, tt.restarts, f.a.restarts)
			// При ничьей партиции перезапускается и b — метрика считает всех.
			assert.Equal(t, f.a.restarts+f.b.restarts, f.metrics.byReason[stallReasonFetch])
		})
	}
}

// TestStallWatchdog_CaughtUpThenNewMessage: дочитавший экземпляр со стоящим
// commit не трогается, пока в партицию не придёт новое сообщение; если он его
// не получает (конец лога ушёл дальше прочитанного), это уже зависание.
func TestStallWatchdog_CaughtUpThenNewMessage(t *testing.T) {
	t.Parallel()
	f := newWatchdogFixture()
	f.a.fetched = map[int]int64{0: 50}
	f.insp.set(part(0, 10, 50))

	f.check(0)
	f.check(testStallAfter + time.Second)
	f.check(stallCooldown + time.Second)
	assert.Zero(t, f.a.restarts, "дочитал — не перезапускается и через паузу")

	f.insp.set(part(0, 10, 51)) // новое сообщение, а экземпляр его не получил
	f.check(stallCheckEvery)
	assert.Equal(t, 1, f.a.restarts)
}
