package rejectlog_test

import (
	"testing"

	"nexus/internal/platform/testleak"
)

// TestMain — детектор утечек горутин (CLAUDE.md §8, §79.6).
// Здесь стережём фоновый агрегатор коллектора: Run обязан завершаться по
// ctx.Done, дописав накопленное, а не оставаться висеть после теста.
func TestMain(m *testing.M) {
	testleak.Verify(m)
}
