package usecase

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain — детектор утечек горутин (CLAUDE.md §8, Phase AUD.3):
// прежде всего SSE live-tail (LogsUsecase.Subscribe) — его polling-горутина
// обязана завершаться по отмене ctx подписчика.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
