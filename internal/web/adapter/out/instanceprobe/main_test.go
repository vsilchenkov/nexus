package instanceprobe_test

import (
	"testing"

	"nexus/internal/platform/testleak"
)

// TestMain — детектор утечек горутин (CLAUDE.md §8, §79.6).
// Здесь стережём: проба соседних инстансов §73: wg.Go на каждый адрес обязан быть
// дождан, а отменённая проба — не пережить вызов.
func TestMain(m *testing.M) {
	testleak.Verify(m, testleak.HTTPClient()...)
}
