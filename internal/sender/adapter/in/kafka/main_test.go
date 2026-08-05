package kafka_test

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain — детектор утечек горутин (CLAUDE.md §8, §79.6).
// Здесь стережём: consumer-группы, DLQ-репроцессор и sweeper паузы обязаны
// останавливаться по отмене ctx: их горутины держат чтение из топиков.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
