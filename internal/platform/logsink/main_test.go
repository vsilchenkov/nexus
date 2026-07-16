package logsink

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain — детектор утечек горутин (CLAUDE.md §8): шиппер обязан завершаться
// по отмене ctx, тестовые горутины Handle — по окончании теста.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
