package chlogretry

import "testing"

func TestChunkLimit(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		maxytes int
		want    int
	}{
		{"zero_passthrough", 0, 0},
		{"negative_passthrough", -1, -1},
		{"small_no_reserve", 800, 800},
		{"equal_reserve_no_subtract", framingReserve, framingReserve},
		{"above_reserve_subtracts", framingReserve + 1, 1},
		{"ten_mib_leaves_headroom", 10 * 1024 * 1024, 10*1024*1024 - framingReserve},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := chunkLimit(c.maxytes); got != c.want {
				t.Fatalf("chunkLimit(%d) = %d, want %d", c.maxytes, got, c.want)
			}
		})
	}
}
