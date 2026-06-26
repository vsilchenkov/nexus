//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"nexus/internal/platform/logging"
	"nexus/internal/platform/nodestatus"
	webredis "nexus/internal/web/adapter/out/redis"
)

// TestNodeStatus_WriterReader_Redis_E2E (§46): Sender-writer пишет исход
// последнего исходящего вызова узла в Redis, Web-reader читает его тем же ключом.
// Проверяет round-trip и согласованность формата ключа (nexus:node:last_error:<path>)
// между писателем и читателем — это и есть механизм, который держит статус «Down»
// после рестарта процесса (ключ в Redis переживает рестарт, в отличие от §41-гауджа).
func TestNodeStatus_WriterReader_Redis_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	client, cleanup := startRedis(t, ctx)
	defer cleanup()

	logger := logging.NewNoop()
	writer := nodestatus.NewRedisWriter(client, logger) // Sender-сторона
	reader := webredis.NewNodeStatusReaderRedis(client, logger)

	writer.SetLastError(ctx, "partner/fail", true) // последний вызов — ошибка
	writer.SetLastError(ctx, "partner/ok", false)  // последний вызов — 2xx

	got, err := reader.GetLastErrors(ctx, []string{"partner/fail", "partner/ok", "partner/never"})
	require.NoError(t, err)

	v, ok := got["partner/fail"]
	require.True(t, ok)
	require.True(t, v, "последний вызов — ошибка → Down")

	v, ok = got["partner/ok"]
	require.True(t, ok)
	require.False(t, v, "последний вызов — 2xx → не Down")

	_, ok = got["partner/never"]
	require.False(t, ok, "узел без записи не попадает в карту (добор из Prometheus)")

	// Перезапись: повторный успешный вызов снимает «Down».
	writer.SetLastError(ctx, "partner/fail", false)
	got, err = reader.GetLastErrors(ctx, []string{"partner/fail"})
	require.NoError(t, err)
	require.False(t, got["partner/fail"], "перезапись 2xx → больше не Down")

	// Пустой список путей — пустая карта, без обращения к Redis.
	empty, err := reader.GetLastErrors(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, empty)
}
