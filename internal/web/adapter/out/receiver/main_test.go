package receiver_test

import (
	"testing"

	"nexus/internal/platform/testleak"
)

// TestMain — детектор утечек горутин (CLAUDE.md §8, §79.6).
// Здесь стережём: диспетчер replay: реинъекция через Receiver не оставляет горутин.
func TestMain(m *testing.M) {
	testleak.Verify(m, testleak.HTTPClient()...)
}
