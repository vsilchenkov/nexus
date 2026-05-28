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

// TestLogReader_CountErrors_E2E (§20.3, Phase F2.3): CountErrors считает только
// записи-ошибки (status>=400 OR status=0 OR done=0) в окне по date_request.
func TestLogReader_CountErrors_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	conn, cfg, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	const table = "vika_logs.count_err"
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
			DateRequest: now, DateResponse: now, Duration: 1, Done: done,
			ChecksumRequest: strings.Repeat("a", 32), ChecksumResponse: strings.Repeat("b", 32),
			Host: "h", IP: "127.0.0.1", Attempts: 1, AttemptsDetails: "[]",
		}
	}
	writer.Write(ctx, table, mk("00000000-0000-0000-0000-000000000001", 200, true))  // ok
	writer.Write(ctx, table, mk("00000000-0000-0000-0000-000000000002", 500, false)) // error (status)
	writer.Write(ctx, table, mk("00000000-0000-0000-0000-000000000003", 200, false)) // error (done=0)
	writer.Write(ctx, table, mk("00000000-0000-0000-0000-000000000004", 0, false))   // error (network)
	require.NoError(t, writer.Flush(ctx))

	// Дожидаемся всех 4 строк.
	deadline := time.Now().Add(20 * time.Second)
	var total uint64
	for time.Now().Before(deadline) {
		require.NoError(t, conn.QueryRow(ctx, "SELECT count() FROM "+table).Scan(&total))
		if total >= 4 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.EqualValues(t, 4, total)

	reader := webch.NewLogReader(provider, logger)
	sinceMs := now.Add(-time.Hour).UnixMilli()
	untilMs := now.Add(time.Hour).UnixMilli()
	n, err := reader.CountErrors(ctx, table, sinceMs, untilMs)
	require.NoError(t, err)
	require.EqualValues(t, 3, n, "3 error rows expected (one ok excluded)")

	// Окно в прошлом — ошибок нет.
	n, err = reader.CountErrors(ctx, table, now.Add(-2*time.Hour).UnixMilli(), now.Add(-time.Hour).UnixMilli())
	require.NoError(t, err)
	require.EqualValues(t, 0, n)
}
