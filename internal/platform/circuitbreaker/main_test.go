package circuitbreaker_test

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain — детектор утечек горутин (CLAUDE.md §8, §79.6).
// Здесь стережём: клиент Redis и его фоновые задачи закрываются вместе с тестом.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
