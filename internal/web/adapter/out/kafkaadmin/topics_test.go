package kafkaadmin

import (
	"testing"

	"github.com/stretchr/testify/assert"

	kafkapf "nexus/internal/platform/kafka"
)

// TestSplitGroupLag (§114.6): lag группы делится по наличию commit, а
// партиция, по которой OffsetFetch не ответил, не входит никуда.
func TestSplitGroupLag(t *testing.T) {
	t.Parallel()
	parts := []int{0, 1}
	wm := map[int]kafkapf.Watermark{0: {First: 0, Last: 10}, 1: {First: 0, Last: 5}}
	tests := []struct {
		name      string
		wm        map[int]kafkapf.Watermark
		committed map[int]int64
		want      groupLagSplit
	}{
		{name: "commit на всех партициях", wm: wm, committed: map[int]int64{0: 4, 1: 5},
			want: groupLagSplit{committed: 6, consumes: true}},
		{name: "одна партиция без commit — отдельно, от First", wm: wm, committed: map[int]int64{0: 4, 1: -1},
			want: groupLagSplit{committed: 6, uncommitted: 5, uncommittedParts: 1, consumes: true}},
		{name: "OffsetFetch не ответил по партиции — она не считается вовсе", wm: wm, committed: map[int]int64{0: 4},
			want: groupLagSplit{committed: 6, consumes: true}},
		{name: "commit левее First (retention срезал хвост) — от First",
			wm:        map[int]kafkapf.Watermark{0: {First: 8, Last: 10}},
			committed: map[int]int64{0: 3},
			want:      groupLagSplit{committed: 2, consumes: true}},
		{name: "ни одного commit — группа топик не читает", wm: wm, committed: map[int]int64{0: -1, 1: -1},
			want: groupLagSplit{uncommitted: 15, uncommittedParts: 2}},
		{name: "нет watermarks — commit учтён, lag нет", wm: map[int]kafkapf.Watermark{}, committed: map[int]int64{0: 4, 1: -1},
			want: groupLagSplit{uncommittedParts: 1, consumes: true}},
		{name: "OffsetFetch не ответил вовсе", wm: wm, committed: map[int]int64{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, splitGroupLag(parts, tt.wm, tt.committed))
		})
	}
}
