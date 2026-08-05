package clickhouse_test

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain — детектор утечек горутин (CLAUDE.md §8, §79.6).
// Здесь стережём: отложенное закрытие соединения (closeWithDelay) и провайдеры
// подключения не должны оставлять горутин после Close.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
