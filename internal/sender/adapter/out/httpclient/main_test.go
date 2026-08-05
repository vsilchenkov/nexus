package httpclient

import (
	"testing"

	"nexus/internal/platform/testleak"
)

// TestMain — детектор утечек горутин (CLAUDE.md §8, §79.6).
// Здесь стережём: исходящий HTTP-клиент Sender'а: таймауты, ретраи и разбор ответа
// не должны оставлять своих горутин (фон keep-alive — чужой, см. testleak).
func TestMain(m *testing.M) {
	testleak.Verify(m, testleak.HTTPClient()...)
}
