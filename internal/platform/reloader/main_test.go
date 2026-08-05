package reloader_test

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain — детектор утечек горутин (CLAUDE.md §8, §79.6).
// Здесь стережём: подписчик Redis pub/sub обязан завершаться по отмене ctx (иначе
// hot-reload конфига оставляет горутину на каждый рестарт подписки).
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
