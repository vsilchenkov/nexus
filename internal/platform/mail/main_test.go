package mail

import (
	"testing"

	"nexus/internal/platform/testleak"
)

// TestMain — детектор утечек горутин (CLAUDE.md §8, §79.6).
// Здесь стережём: отправка письма не имеет права оставлять висящее соединение
// с релеем — ни после успеха, ни после отказа, ни после отмены контекста.
func TestMain(m *testing.M) {
	testleak.Verify(m, testleak.HTTPClient()...)
}
