package http

import (
	"testing"

	"nexus/internal/platform/testleak"
)

// TestMain — детектор утечек горутин (CLAUDE.md §8, §79.6).
// Здесь стережём: входные handler'ы Receiver'а: маршрутизация и rate-limit работают
// синхронно, фоновых горутин у них нет.
func TestMain(m *testing.M) {
	testleak.Verify(m, testleak.HTTPClient()...)
}
