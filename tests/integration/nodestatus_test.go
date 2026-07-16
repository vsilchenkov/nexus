//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/nodestatus"
	webredis "nexus/internal/web/adapter/out/redis"
)

// TestNodeStatus_WriterReader_Redis_E2E (§46, §52): Sender-writer пишет исход
// последнего исходящего вызова узла в Redis, Web-reader читает его тем же ключом.
// Проверяет round-trip всех трёх исходов (ok/degraded/down), согласованность
// формата ключа (nexus:node:last_error:<path>) и legacy-совместимость — это и
// есть механизм, который держит runtime-бейдж после рестарта процесса (ключ в
// Redis переживает рестарт, в отличие от §41-гауджа).
func TestNodeStatus_WriterReader_Redis_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	client, cleanup := startRedis(t, ctx)
	defer cleanup()

	logger := logging.NewNoop()
	writer := nodestatus.NewRedisWriter(client, logger) // Sender-сторона
	reader := webredis.NewNodeStatusReaderRedis(client, logger)

	writer.SetLastOutcome(ctx, "partner/fail", domain.NodeOutcomeDown)      // транспорт/5xx
	writer.SetLastOutcome(ctx, "partner/flaky", domain.NodeOutcomeDegraded) // 3xx/4xx (§50.4)
	writer.SetLastOutcome(ctx, "partner/ok", domain.NodeOutcomeOK)          // 2xx

	got, err := reader.GetLastOutcomes(ctx,
		[]string{"partner/fail", "partner/flaky", "partner/ok", "partner/never"})
	require.NoError(t, err)

	v, ok := got["partner/fail"]
	require.True(t, ok)
	require.Equal(t, domain.NodeOutcomeDown, v, "транспорт/5xx → down")

	v, ok = got["partner/flaky"]
	require.True(t, ok)
	require.Equal(t, domain.NodeOutcomeDegraded, v, "4xx → degraded (узел жив)")

	v, ok = got["partner/ok"]
	require.True(t, ok)
	require.Equal(t, domain.NodeOutcomeOK, v, "2xx → ok")

	_, ok = got["partner/never"]
	require.False(t, ok, "узел без записи не попадает в карту (добор из Prometheus)")

	// Legacy §46: булево "1" старого Sender («любой не-2xx») читается как down —
	// worst case, самоисцелится следующим вызовом узла.
	require.NoError(t,
		client.Set(ctx, nodestatus.Key("partner/legacy"), "1", time.Hour).Err())
	got, err = reader.GetLastOutcomes(ctx, []string{"partner/legacy"})
	require.NoError(t, err)
	require.Equal(t, domain.NodeOutcomeDown, got["partner/legacy"],
		"legacy \"1\" → down")

	// Нераспознанное значение — узел пропускается (fallback на Prometheus).
	require.NoError(t,
		client.Set(ctx, nodestatus.Key("partner/garbage"), "banana", time.Hour).Err())
	got, err = reader.GetLastOutcomes(ctx, []string{"partner/garbage"})
	require.NoError(t, err)
	_, ok = got["partner/garbage"]
	require.False(t, ok, "мусорное значение не попадает в карту")

	// Перезапись: повторный успешный вызов снимает «Down».
	writer.SetLastOutcome(ctx, "partner/fail", domain.NodeOutcomeOK)
	got, err = reader.GetLastOutcomes(ctx, []string{"partner/fail"})
	require.NoError(t, err)
	require.Equal(t, domain.NodeOutcomeOK, got["partner/fail"],
		"перезапись 2xx → больше не Down")

	// Пустой список путей — пустая карта, без обращения к Redis.
	empty, err := reader.GetLastOutcomes(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, empty)
}
