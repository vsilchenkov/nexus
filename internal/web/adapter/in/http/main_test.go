package http

import (
	"testing"

	"nexus/internal/platform/testleak"
)

// TestMain — детектор утечек горутин (CLAUDE.md §8, §79.6).
// Здесь стережём: handler'ы Web: SSE live-tail закрывает поток по отмене запроса,
// прокси Receiver'а не оставляет висящих соединений.
func TestMain(m *testing.M) {
	testleak.Verify(m, testleak.HTTPClient()...)
}
