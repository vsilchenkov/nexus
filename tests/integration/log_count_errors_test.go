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

	const table = "nexus_default.count_err"
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
	n, err := reader.CountErrors(ctx, table, "", sinceMs, untilMs)
	require.NoError(t, err)
	require.EqualValues(t, 3, n, "3 error rows expected (one ok excluded)")

	// Окно в прошлом — ошибок нет.
	n, err = reader.CountErrors(ctx, table, "", now.Add(-2*time.Hour).UnixMilli(), now.Add(-time.Hour).UnixMilli())
	require.NoError(t, err)
	require.EqualValues(t, 0, n)

	// §35: CountFailed считает СТРОГО done=0 (исключает 500/done=1, которого для
	// этих данных нет, но семантика уже): из 4 строк done=0 у трёх (id 2,3,4).
	nf, err := reader.CountFailed(ctx, table, "", sinceMs, untilMs)
	require.NoError(t, err)
	require.EqualValues(t, 3, nf, "3 done=0 rows expected")

	nf, err = reader.CountFailed(ctx, table, "", now.Add(-2*time.Hour).UnixMilli(), now.Add(-time.Hour).UnixMilli())
	require.NoError(t, err)
	require.EqualValues(t, 0, nf)
}

// TestLogReader_NodeKPI_Chart_E2E (§21): ТОЧНЫЕ per-node KPI и временной ряд из
// ClickHouse-логов. Сценарий с репроцессором: одно сообщение упало и потом
// доставлено (2 строки, 1 уникальный ID), одно только упало, одно доставлено
// сразу. KPI считается по УНИКАЛЬНЫМ запросам (ID), ряд — по сырым строкам.
func TestLogReader_NodeKPI_Chart_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	conn, cfg, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	const table = "nexus_default.node_kpi"
	createNodeLogTable(t, ctx, conn, table)

	logger := logging.NewNoop()
	provider := clickhouse.StaticProvider(conn)
	writer := chlog.New(provider, cfg, logger)
	defer writer.Stop(ctx)

	now := time.Now().UTC().Truncate(time.Second)
	mk := func(id string, status int32, done bool, dur int32) *domain.LogRecord {
		return &domain.LogRecord{
			ID: id, Type: domain.RootMethodRequestAsync, URL: "https://x", Method: "POST",
			Status: status, DateCreate: now, DateRequest: now, DateResponse: now,
			Duration: dur, Done: done,
			ChecksumRequest: strings.Repeat("a", 32), ChecksumResponse: strings.Repeat("b", 32),
			Host: "h", IP: "127.0.0.1", Attempts: 1, AttemptsDetails: "[]",
		}
	}
	// A: упал (500) затем доставлен (200) — 2 строки, 1 уникальный ID, доставлен.
	writer.Write(ctx, table, mk("00000000-0000-0000-0000-0000000000a1", 500, false, 10))
	writer.Write(ctx, table, mk("00000000-0000-0000-0000-0000000000a1", 200, true, 20))
	// B: только упал — не доставлен.
	writer.Write(ctx, table, mk("00000000-0000-0000-0000-0000000000b2", 0, false, 5))
	// C: доставлен сразу.
	writer.Write(ctx, table, mk("00000000-0000-0000-0000-0000000000c3", 200, true, 30))
	require.NoError(t, writer.Flush(ctx))

	deadline := time.Now().Add(20 * time.Second)
	var total uint64
	for time.Now().Before(deadline) {
		require.NoError(t, conn.QueryRow(ctx, "SELECT count() FROM "+table).Scan(&total))
		if total >= 4 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.EqualValues(t, 4, total, "4 строки записаны")

	reader := webch.NewLogReader(provider, logger)
	sinceMs := now.Add(-time.Hour).UnixMilli()
	untilMs := now.Add(time.Hour).UnixMilli()

	// KPI — по уникальным запросам (ID): Total=3 (A,B,C), Delivered=2 (A,C), Errors=1 (B).
	kpi, err := reader.NodeKPI(ctx, table, "", sinceMs, untilMs)
	require.NoError(t, err)
	require.EqualValues(t, 3, kpi.Total, "3 уникальных запроса (A,B,C)")
	require.EqualValues(t, 2, kpi.Delivered, "A и C хотя бы раз доставлены")
	require.EqualValues(t, 1, kpi.Errors, "B так и не доставлен")
	require.Greater(t, kpi.P95ms, 0.0, "p95 длительности > 0")

	// Ряд — по сырым строкам: всего 4, ошибок (done=0) 2 (A-fail + B). Плотный ряд
	// (нули в пустых бакетах), сумма по бакетам = сырые счётчики.
	series, err := reader.NodeChart(ctx, table, "", sinceMs, untilMs, 48)
	require.NoError(t, err)
	require.NotEmpty(t, series)
	var sumCnt, sumErr uint64
	for _, p := range series {
		sumCnt += p.Count
		sumErr += p.Errors
	}
	require.EqualValues(t, 4, sumCnt, "4 строки всего")
	require.EqualValues(t, 2, sumErr, "2 строки done=0 (A-fail, B)")

	// Окно в прошлом — пусто.
	past, err := reader.NodeKPI(ctx, table, "", now.Add(-2*time.Hour).UnixMilli(), now.Add(-time.Hour).UnixMilli())
	require.NoError(t, err)
	require.Zero(t, past.Total)
}

// TestLogReader_NodeIDFilter_E2E (§37): несколько узлов в ОДНОЙ таблице
// различаются по node_id. Все per-node чтения/удаления фильтруют
// (node_id = ? OR node_id = ''); legacy-записи (node_id='') видны любому узлу.
// Критично: DeleteFailed одного узла НЕ трогает записи другого.
func TestLogReader_NodeIDFilter_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	conn, cfg, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	const table = "nexus_default.shared_node_id"
	createNodeLogTable(t, ctx, conn, table)

	logger := logging.NewNoop()
	provider := clickhouse.StaticProvider(conn)
	writer := chlog.New(provider, cfg, logger)
	defer writer.Stop(ctx)

	now := time.Now().UTC().Truncate(time.Second)
	mk := func(id, nodeID string) *domain.LogRecord {
		return &domain.LogRecord{
			ID: id, Type: domain.RootMethodRequestAsync, URL: "https://x", Method: "POST",
			Request: "{}", Response: "{}", Status: 0, DateCreate: now,
			DateRequest: now, DateResponse: now, Duration: 1, Done: false, // все done=0
			ChecksumRequest: strings.Repeat("a", 32), ChecksumResponse: strings.Repeat("b", 32),
			Host: "h", IP: "127.0.0.1", Attempts: 1, AttemptsDetails: "[]", NodeID: nodeID,
		}
	}
	const nodeA, nodeB = "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"
	// nodeA: 2 записи; nodeB: 3; legacy (node_id=''): 1.
	writer.Write(ctx, table, mk("00000000-0000-0000-0000-0000000000a1", nodeA))
	writer.Write(ctx, table, mk("00000000-0000-0000-0000-0000000000a2", nodeA))
	writer.Write(ctx, table, mk("00000000-0000-0000-0000-0000000000b1", nodeB))
	writer.Write(ctx, table, mk("00000000-0000-0000-0000-0000000000b2", nodeB))
	writer.Write(ctx, table, mk("00000000-0000-0000-0000-0000000000b3", nodeB))
	writer.Write(ctx, table, mk("00000000-0000-0000-0000-00000000000c", "")) // legacy
	require.NoError(t, writer.Flush(ctx))

	deadline := time.Now().Add(20 * time.Second)
	var total uint64
	for time.Now().Before(deadline) {
		require.NoError(t, conn.QueryRow(ctx, "SELECT count() FROM "+table).Scan(&total))
		if total >= 6 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.EqualValues(t, 6, total)

	reader := webch.NewLogReader(provider, logger)

	// CountFailed: nodeA = 2 свои + 1 legacy = 3; nodeB = 3 свои + 1 legacy = 4.
	na, err := reader.CountFailed(ctx, table, nodeA, 0, 0)
	require.NoError(t, err)
	require.EqualValues(t, 3, na, "nodeA: свои 2 + legacy 1")
	nb, err := reader.CountFailed(ctx, table, nodeB, 0, 0)
	require.NoError(t, err)
	require.EqualValues(t, 4, nb, "nodeB: свои 3 + legacy 1")

	// FailedIDs nodeA — только a1,a2 + legacy c (НЕ b*).
	ids, _, err := reader.FailedIDs(ctx, table, nodeA, 0, 0, 1000)
	require.NoError(t, err)
	require.Len(t, ids, 3)
	require.NotContains(t, ids, "00000000-0000-0000-0000-0000000000b1")

	// NodeKPI nodeA — 3 уникальных (2 свои + legacy), все ошибки.
	kpi, err := reader.NodeKPI(ctx, table, nodeA, 0, 0)
	require.NoError(t, err)
	require.EqualValues(t, 3, kpi.Total)
	require.EqualValues(t, 3, kpi.Errors)

	// DeleteFailed nodeA — удаляет 3 (свои + legacy); записи nodeB НЕ трогает.
	deleted, err := reader.DeleteFailed(ctx, table, nodeA, 0, 0)
	require.NoError(t, err)
	require.EqualValues(t, 3, deleted)
	require.Eventually(t, func() bool {
		var remain uint64
		_ = conn.QueryRow(ctx, "SELECT count() FROM "+table).Scan(&remain)
		return remain == 3 // выжили только 3 записи nodeB
	}, 20*time.Second, 500*time.Millisecond, "только записи nodeB должны выжить")

	// nodeB по-прежнему видит свои 3 (legacy уже удалён вместе с nodeA).
	nb2, err := reader.CountFailed(ctx, table, nodeB, 0, 0)
	require.NoError(t, err)
	require.EqualValues(t, 3, nb2, "nodeB не задет очисткой nodeA")
}
