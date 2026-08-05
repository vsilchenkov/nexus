package safego_test

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain — детектор утечек горутин (CLAUDE.md §8, §79.6).
// Здесь стережём: safego.Go порождает горутину и обязан закрывать её канал завершения;
// Await не имеет права оставлять её висеть после возврата.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
