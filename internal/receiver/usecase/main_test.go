package usecase

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain — детектор утечек горутин (CLAUDE.md §8, Phase AUD.3):
// PullerManager и его воркеры обязаны полностью останавливаться при
// отмене ctx (graceful stop, §27.5).
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
