package kafka

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"nexus/internal/platform/clock"
	kafkapf "nexus/internal/platform/kafka"
	"nexus/internal/platform/logging"
)

// fakePassSlot — проходной reader: client.id, прочитанные позиции и счётчик
// пересозданий.
type fakePassSlot struct {
	id        string
	fetched   map[int]int64
	recreated int
}

func (f *fakePassSlot) ClientID() string { return f.id }
func (f *fakePassSlot) Recreate(string)  { f.recreated++ }
func (f *fakePassSlot) FetchedNext(p int) (int64, bool) {
	v, ok := f.fetched[p]
	return v, ok
}

func newTestPassGuard(insp *fakeInspector, slot *fakePassSlot, m *restartCounter) *passStallGuard {
	return newTestPassGuardClock(insp, slot, m, clock.NewFake(time.Unix(10_000, 0)))
}

func newTestPassGuardClock(insp *fakeInspector, slot *fakePassSlot, m *restartCounter, clk clock.Clock) *passStallGuard {
	guard := StallGuard{Inspector: insp, After: time.Minute, Metrics: m, Clock: clk}
	return newPassStallGuard("g-paused", "nexus.async.paused", guard, slot, logging.NewNoop())
}

// TestPassStallGuard (§113.6): reader проходов пересоздаётся после
// passIdleLimit пустых проходов подряд, только если стоит его собственная (или
// ничья) партиция.
func TestPassStallGuard(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		pos       []kafkapf.PartitionPosition
		assigned  map[string][]int
		fetched   map[int]int64
		posErr    error
		passes    []int
		recreated int
	}{
		{name: "своя партиция стоит", pos: []kafkapf.PartitionPosition{part(3, 10, 970)},
			assigned: map[string][]int{"me": {3}}, passes: []int{0, 0, 0}, recreated: 1},
		{name: "ничья партиция стоит", pos: []kafkapf.PartitionPosition{part(3, 10, 970)},
			assigned: map[string][]int{}, passes: []int{0, 0, 0}, recreated: 1},
		{name: "меньше порога", pos: []kafkapf.PartitionPosition{part(3, 10, 970)},
			assigned: map[string][]int{"me": {3}}, passes: []int{0, 0}},
		{name: "непустой проход сбрасывает счёт", pos: []kafkapf.PartitionPosition{part(3, 10, 970)},
			assigned: map[string][]int{"me": {3}}, passes: []int{0, 0, 5, 0, 0}},
		{name: "lag у другой реплики", pos: []kafkapf.PartitionPosition{part(3, 10, 970), part(0, 4, 4)},
			assigned: map[string][]int{"me": {0}, "other": {3}}, passes: []int{0, 0, 0}},
		{name: "очередь пуста", pos: []kafkapf.PartitionPosition{part(3, 970, 970)},
			assigned: map[string][]int{"me": {3}}, passes: []int{0, 0, 0}},
		{name: "брокер недоступен", posErr: errors.New("dial"),
			assigned: map[string][]int{"me": {3}}, passes: []int{0, 0, 0}},
		// §114.2: всё получено до конца лога — стоит commit, а не чтение.
		{name: "дочитал: commit отвергается", pos: []kafkapf.PartitionPosition{part(3, 10, 970)},
			assigned: map[string][]int{"me": {3}}, fetched: map[int]int64{3: 970}, passes: []int{0, 0, 0}},
		{name: "дочитал не всё", pos: []kafkapf.PartitionPosition{part(3, 10, 970)},
			assigned: map[string][]int{"me": {3}}, fetched: map[int]int64{3: 500}, passes: []int{0, 0, 0}, recreated: 1},
		{name: "своя дочитана, ничья стоит", pos: []kafkapf.PartitionPosition{part(3, 10, 970), part(1, 0, 5)},
			assigned: map[string][]int{"me": {3}}, fetched: map[int]int64{3: 970}, passes: []int{0, 0, 0}, recreated: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			insp := &fakeInspector{pos: tt.pos, posErr: tt.posErr, assigned: tt.assigned}
			slot := &fakePassSlot{id: "me", fetched: tt.fetched}
			m := &restartCounter{}
			g := newTestPassGuard(insp, slot, m)
			for _, n := range tt.passes {
				g.PassDone(context.Background(), n)
			}
			assert.Equal(t, tt.recreated, slot.recreated)
			assert.Equal(t, tt.recreated, m.byReason[stallReasonPassIdle])
		})
	}
}

// TestPassStallGuard_Cooldown (§114.2): после пересоздания следующее — не
// раньше чем через stallCooldown; до §114 reader пересоздавался каждые три
// пустых прохода.
func TestPassStallGuard_Cooldown(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Unix(10_000, 0))
	insp := &fakeInspector{pos: []kafkapf.PartitionPosition{part(3, 10, 970)}, assigned: map[string][]int{"me": {3}}}
	slot := &fakePassSlot{id: "me"}
	g := newTestPassGuardClock(insp, slot, &restartCounter{}, clk)
	idle := func() {
		for range passIdleLimit {
			g.PassDone(context.Background(), 0)
		}
	}

	idle()
	assert.Equal(t, 1, slot.recreated)
	clk.Advance(time.Minute)
	idle()
	assert.Equal(t, 1, slot.recreated, "внутри паузы между пересозданиями")
	clk.Advance(stallCooldown)
	idle()
	assert.Equal(t, 2, slot.recreated)
}

// TestPassStallGuard_Disabled: выключенный сторож — nil, и вызов на nil безопасен.
func TestPassStallGuard_Disabled(t *testing.T) {
	t.Parallel()
	g := newPassStallGuard("g", "t", StallGuard{Inspector: &fakeInspector{}}, &fakePassSlot{}, logging.NewNoop())
	assert.Nil(t, g)
	g.PassDone(context.Background(), 0)
}
