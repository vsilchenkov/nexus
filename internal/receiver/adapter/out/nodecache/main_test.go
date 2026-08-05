package nodecache

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain — детектор утечек горутин (CLAUDE.md §8, §79.6).
// Здесь стережём: write-back кеша узлов — единственная фоновая горутина адаптера;
// в unit-тестах она не запускается, и этот сторож фиксирует факт.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
