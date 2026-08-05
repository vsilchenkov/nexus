package grpcsender_test

import (
	"testing"

	"nexus/internal/platform/testleak"
)

// TestMain — детектор утечек горутин (CLAUDE.md §8, §79.6).
// Здесь стережём: gRPC-клиент к Sender'у: соединение закрывается вместе с тестом
// (фон keepalive/controlBuffer — внутренний для grpc-go, см. testleak).
func TestMain(m *testing.M) {
	testleak.Verify(m, testleak.GRPC()...)
}
