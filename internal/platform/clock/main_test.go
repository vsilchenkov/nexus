package clock_test

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain — детектор утечек горутин (CLAUDE.md §8, §79.6).
// Здесь стережём: тикеры и таймеры фейкового времени обязаны останавливаться вместе с тестом.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
