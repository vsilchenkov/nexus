package prometheus

import (
	"testing"

	"nexus/internal/platform/testleak"
)

// TestMain — детектор утечек горутин (CLAUDE.md §8, §79.6).
// Здесь стережём: клиент Prometheus: запросы обязаны укладываться в ctx вызова.
func TestMain(m *testing.M) {
	testleak.Verify(m, testleak.HTTPClient()...)
}
