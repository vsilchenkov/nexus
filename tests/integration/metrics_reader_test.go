//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/clickhouse"
	"nexus/internal/platform/logging"
	"nexus/internal/sender/adapter/out/chlog"
	webch "nexus/internal/web/adapter/out/clickhouse"
)

// TestMetricsReader_NodeKPI_E2E (§21): NodeKPI считает total/delivered/errors
// и перцентили длительности из таблицы логов узла.
func TestMetricsReader_NodeKPI_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	conn, cfg, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	const table = "nexus_default.metrics_kpi"
	createNodeLogTable(t, ctx, conn, table)

	logger := logging.NewNoop()
	provider := clickhouse.StaticProvider(conn)
	writer := chlog.New(provider, cfg, logger)
	defer writer.Stop(ctx)

	now := time.Now().UTC().Truncate(time.Second)
	mk := func(id string, status, dur int32, done bool) *domain.LogRecord {
		return &domain.LogRecord{
			ID: id, Type: domain.RootMethodRequest, URL: "https://x", Method: "POST",
			Request: "{}", Response: "{}", Status: status, DateCreate: now,
			DateRequest: now, DateResponse: now, Duration: dur, Done: done,
			ChecksumRequest: strings.Repeat("a", 32), ChecksumResponse: strings.Repeat("b", 32),
			Host: "h", IP: "127.0.0.1", Attempts: 1, AttemptsDetails: "[]",
		}
	}
	writer.Write(ctx, table, mk("00000000-0000-0000-0000-000000000001", 200, 50, true))  // delivered
	writer.Write(ctx, table, mk("00000000-0000-0000-0000-000000000002", 200, 100, true)) // delivered
	writer.Write(ctx, table, mk("00000000-0000-0000-0000-000000000003", 200, 150, true)) // delivered
	writer.Write(ctx, table, mk("00000000-0000-0000-0000-000000000004", 500, 30, false)) // error (status)
	writer.Write(ctx, table, mk("00000000-0000-0000-0000-000000000005", 0, 0, false))    // error (network)
	require.NoError(t, writer.Flush(ctx))

	// Дожидаемся всех 5 строк.
	deadline := time.Now().Add(20 * time.Second)
	var total uint64
	for time.Now().Before(deadline) {
		require.NoError(t, conn.QueryRow(ctx, "SELECT count() FROM "+table).Scan(&total))
		if total >= 5 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.EqualValues(t, 5, total)

	reader := webch.NewMetricsReader(provider, logger)
	sinceMs := now.Add(-time.Hour).UnixMilli()
	untilMs := now.Add(time.Hour).UnixMilli()

	kpi, err := reader.NodeKPI(ctx, table, sinceMs, untilMs)
	require.NoError(t, err)
	require.EqualValues(t, 5, kpi.Total)
	require.EqualValues(t, 3, kpi.Delivered)
	require.EqualValues(t, 2, kpi.Errors)
	require.GreaterOrEqual(t, kpi.P95ms, 100.0, "p95 should reflect higher durations")
	require.LessOrEqual(t, kpi.P95ms, 150.0)

	// Окно в прошлом — данных нет, перцентили = 0 (NaN нормализован).
	kpi, err = reader.NodeKPI(ctx, table, now.Add(-3*time.Hour).UnixMilli(), now.Add(-2*time.Hour).UnixMilli())
	require.NoError(t, err)
	require.EqualValues(t, 0, kpi.Total)
	require.EqualValues(t, 0, kpi.P95ms)
}

// TestMetricsReader_NodeSeries_E2E (§21): NodeSeries возвращает ровно buckets
// точек, а сумма по ряду равна числу записей в окне.
func TestMetricsReader_NodeSeries_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	conn, cfg, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	const table = "nexus_default.metrics_series"
	createNodeLogTable(t, ctx, conn, table)

	logger := logging.NewNoop()
	provider := clickhouse.StaticProvider(conn)
	writer := chlog.New(provider, cfg, logger)
	defer writer.Stop(ctx)

	now := time.Now().UTC().Truncate(time.Second)
	mk := func(id string, status int32, done bool) *domain.LogRecord {
		return &domain.LogRecord{
			ID: id, Type: domain.RootMethodRequest, URL: "https://x", Method: "POST",
			Request: "{}", Response: "{}", Status: status, DateCreate: now,
			DateRequest: now, DateResponse: now, Duration: 10, Done: done,
			ChecksumRequest: strings.Repeat("a", 32), ChecksumResponse: strings.Repeat("b", 32),
			Host: "h", IP: "127.0.0.1", Attempts: 1, AttemptsDetails: "[]",
		}
	}
	writer.Write(ctx, table, mk("00000000-0000-0000-0000-000000000001", 200, true))
	writer.Write(ctx, table, mk("00000000-0000-0000-0000-000000000002", 200, true))
	writer.Write(ctx, table, mk("00000000-0000-0000-0000-000000000003", 500, false)) // error
	require.NoError(t, writer.Flush(ctx))

	deadline := time.Now().Add(20 * time.Second)
	var total uint64
	for time.Now().Before(deadline) {
		require.NoError(t, conn.QueryRow(ctx, "SELECT count() FROM "+table).Scan(&total))
		if total >= 3 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.EqualValues(t, 3, total)

	reader := webch.NewMetricsReader(provider, logger)
	const buckets = 4
	series, err := reader.NodeSeries(ctx, table,
		now.Add(-time.Hour).UnixMilli(), now.Add(time.Hour).UnixMilli(), buckets)
	require.NoError(t, err)
	require.Len(t, series, buckets, "ряд должен содержать ровно buckets точек")

	var sumCount, sumErr uint64
	for _, p := range series {
		sumCount += p.Count
		sumErr += p.Errors
	}
	require.EqualValues(t, 3, sumCount)
	require.EqualValues(t, 1, sumErr)
}
