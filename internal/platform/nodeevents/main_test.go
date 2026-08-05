package nodeevents

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain — детектор утечек горутин (CLAUDE.md §8, §79.6).
// Здесь стережём: подписчик инвалидации кеша узлов обязан завершаться по отмене ctx.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
