package usecase

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain — детектор утечек горутин (CLAUDE.md §8, Phase AUD.3):
// AsyncProcessor/SendUsecase/CHHousekeeping не должны оставлять горутины
// после завершения тестов (в т.ч. прерываемое ожидание paused-узла).
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
