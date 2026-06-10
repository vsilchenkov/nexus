package chlog_test

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain — детектор утечек горутин (CLAUDE.md §8, Phase AUD.3):
// каждый Writer/WriterManager/fallbackStore обязан останавливать свои
// горутины в Stop; регрессия здесь провалит весь пакет.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
