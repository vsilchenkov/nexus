package telegram

import (
	"testing"

	"nexus/internal/platform/testleak"
)

// TestMain — детектор утечек горутин (CLAUDE.md §8, §79.6).
// Здесь стережём: отправка уведомлений: ошибка доставки не имеет права оставлять
// висящую горутину ретрая.
func TestMain(m *testing.M) {
	testleak.Verify(m, testleak.HTTPClient()...)
}
