package kafka

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	kafkapf "nexus/internal/platform/kafka"
	"nexus/internal/platform/logging"
)

// fakePassSlot — проходной reader: client.id и счётчик пересозданий.
type fakePassSlot struct {
	id        string
	recreated int
}

func (f *fakePassSlot) ClientID() string { return f.id }
func (f *fakePassSlot) Recreate(string)  { f.recreated++ }

func newTestPassGuard(insp *fakeInspector, slot *fakePassSlot, m *restartCounter) *passStallGuard {
	guard := StallGuard{Inspector: insp, After: time.Minute, Metrics: m}
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			insp := &fakeInspector{pos: tt.pos, posErr: tt.posErr, assigned: tt.assigned}
			slot := &fakePassSlot{id: "me"}
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

// TestPassStallGuard_Disabled: выключенный сторож — nil, и вызов на nil безопасен.
func TestPassStallGuard_Disabled(t *testing.T) {
	t.Parallel()
	g := newPassStallGuard("g", "t", StallGuard{Inspector: &fakeInspector{}}, &fakePassSlot{}, logging.NewNoop())
	assert.Nil(t, g)
	g.PassDone(context.Background(), 0)
}
