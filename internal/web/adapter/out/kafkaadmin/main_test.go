package kafkaadmin

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain — детектор утечек горутин (CLAUDE.md §8, §79.6).
// Здесь стережём: errgroup пинга брокеров обязан дожидаться всех проб (Ping
// возвращается только после g.Wait).
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
