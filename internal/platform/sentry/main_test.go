package sentry

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain — детектор утечек горутин (CLAUDE.md §8, §79.6).
// Здесь стережём: инициализация не имеет права оставлять фоновый транспорт, когда
// Sentry выключен (Use=false) — а это дефолт тестов.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
