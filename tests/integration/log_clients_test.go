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
	webport "nexus/internal/web/usecase/port"
)

// TestLogReader_DistinctClients_E2E (§105.3) — фасет «Клиент» настоящим
// ClickHouse-SQL.
//
// Главное — записи без client_host засчитываются к своему IP. На бою у
// vika/yandex-delivery 10 записей из 294 потеряли имя (истёк L1-кеш резолвера),
// и фасет по client_host их не видел. Группировка по IP обязана дать одну
// строку с полным числом и самым свежим именем. Остальное: IP без PTR виден
// (host=""), окно периода отсекает старые записи и чужой узел, строки с пустым
// IP (внешние таблицы §64) делятся по client_host, усечение по limit.
func TestLogReader_DistinctClients_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	conn, cfg, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	const (
		table  = "nexus_default.log_clients"
		nodeID = "00000000-0000-0000-0000-00000000c001"
		node3  = "SRV-P-1C-NODE3.vz78.vozovoz.ru"
	)
	createNodeLogTable(t, ctx, conn, table)

	logger := logging.NewNoop()
	provider := clickhouse.StaticProvider(conn)
	writer := chlog.New(provider, cfg, logger)
	defer writer.Stop(ctx)

	now := time.Now().UTC().Truncate(time.Second)
	mk := func(id, ip, host string, at time.Time, node string) *domain.LogRecord {
		return &domain.LogRecord{
			ID: id, Type: domain.RootMethodRequest, URL: "https://x", HTTPMethod: "POST",
			Request: "{}", Response: "{}", Status: 200, DateCreate: at,
			DateRequest: at, DateResponse: at, Duration: 10, Done: true,
			ChecksumRequest: strings.Repeat("a", 32), ChecksumResponse: strings.Repeat("b", 32),
			Host: "h", IP: ip, ClientHost: host, Attempts: 1, AttemptsDetails: "[]", NodeID: node,
		}
	}
	rows := []*domain.LogRecord{
		// 192.168.86.246: 3 записи (4 строки). a2 записана без имени, a1 — под
		// старым именем, a3 — свежая с повтором (2 строки, 1 ID).
		mk("00000000-0000-0000-0000-0000000000a1", "192.168.86.246", "old-name.example", now.Add(-2*time.Hour), nodeID),
		mk("00000000-0000-0000-0000-0000000000a2", "192.168.86.246", "", now.Add(-time.Hour), nodeID),
		mk("00000000-0000-0000-0000-0000000000a3", "192.168.86.246", node3, now.Add(-30*time.Minute), nodeID),
		mk("00000000-0000-0000-0000-0000000000a3", "192.168.86.246", node3, now.Add(-29*time.Minute), nodeID),
		// 192.168.2.5 — IP без PTR: 2 записи, имени нет ни у одной.
		mk("00000000-0000-0000-0000-0000000000b1", "192.168.2.5", "", now.Add(-3*time.Hour), nodeID),
		mk("00000000-0000-0000-0000-0000000000b2", "192.168.2.5", "", now.Add(-4*time.Hour), nodeID),
		// Пустой IP (внешняя таблица §64): группы делятся по client_host.
		mk("00000000-0000-0000-0000-0000000000e1", "", "gate-1.example", now.Add(-time.Hour), nodeID),
		mk("00000000-0000-0000-0000-0000000000e2", "", "", now.Add(-time.Hour), nodeID),
		// Вне окна (3 суток назад) и чужой узел — в фасет не попадают.
		mk("00000000-0000-0000-0000-0000000000f1", "10.9.9.9", "old.example", now.Add(-72*time.Hour), nodeID),
		mk("00000000-0000-0000-0000-0000000000f2", "10.7.7.7", "other.example", now.Add(-time.Hour),
			"00000000-0000-0000-0000-00000000c002"),
	}
	for _, r := range rows {
		writer.Write(ctx, table, r)
	}
	require.NoError(t, writer.Flush(ctx))
	require.Eventually(t, func() bool {
		var n uint64
		_ = conn.QueryRow(ctx, "SELECT count() FROM "+table).Scan(&n)
		return n == uint64(len(rows))
	}, 20*time.Second, 200*time.Millisecond)

	reader := webch.NewLogReader(provider, logger)
	q := webport.LogQuery{
		Table:             table,
		NodeID:            nodeID,
		SinceMs:           now.Add(-24 * time.Hour).UnixMilli(),
		UntilMs:           now.Add(time.Hour).UnixMilli(),
		DateCreateAligned: true,
	}
	want := []webport.LogClient{
		{IP: "192.168.86.246", Host: node3, Count: 3},
		{IP: "192.168.2.5", Host: "", Count: 2},
		{IP: "", Host: "", Count: 1},
		{IP: "", Host: "gate-1.example", Count: 1},
	}

	t.Run("по IP: записи без имени считаются к своему IP", func(t *testing.T) {
		items, truncated, err := reader.DistinctClients(ctx, q, 0)
		require.NoError(t, err)
		require.False(t, truncated)
		require.Equal(t, want, items)
	})

	t.Run("фильтры журнала фасет не сужают", func(t *testing.T) {
		withFilters := q
		withFilters.IP = "192.168.2.5"
		withFilters.Status = "err"
		items, _, err := reader.DistinctClients(ctx, withFilters, 0)
		require.NoError(t, err)
		require.Equal(t, want, items)
	})

	t.Run("окно без date_create-сужения считает то же", func(t *testing.T) {
		unaligned := q
		unaligned.DateCreateAligned = false
		items, _, err := reader.DistinctClients(ctx, unaligned, 0)
		require.NoError(t, err)
		require.Equal(t, want, items)
	})

	t.Run("широкое окно видит старую запись", func(t *testing.T) {
		wide := q
		wide.SinceMs = now.Add(-96 * time.Hour).UnixMilli()
		items, _, err := reader.DistinctClients(ctx, wide, 0)
		require.NoError(t, err)
		require.Len(t, items, 5)
		require.Contains(t, items, webport.LogClient{IP: "10.9.9.9", Host: "old.example", Count: 1})
	})

	t.Run("truncated при limit меньше числа значений", func(t *testing.T) {
		items, truncated, err := reader.DistinctClients(ctx, q, 2)
		require.NoError(t, err)
		require.True(t, truncated)
		require.Equal(t, want[:2], items)

		items, truncated, err = reader.DistinctClients(ctx, q, 4)
		require.NoError(t, err)
		require.False(t, truncated, "limit ровно по числу значений — усечения нет")
		require.Len(t, items, 4)
	})

	t.Run("таблица без client_host (§64 до ALTER) — только IP", func(t *testing.T) {
		const legacy = "nexus_default.log_clients_legacy"
		createNodeLogTable(t, ctx, conn, legacy)
		require.NoError(t, conn.Exec(ctx, "ALTER TABLE "+legacy+" DROP COLUMN client_host"))
		at := now.Add(-time.Hour)
		for _, id := range []string{"00000000-0000-0000-0000-0000000000d1", "00000000-0000-0000-0000-0000000000d2"} {
			require.NoError(t, conn.Exec(ctx,
				"INSERT INTO "+legacy+" (ID, date_create, date_request, date_response, IP, node_id) VALUES (?, ?, ?, ?, ?, ?)",
				id, at, at, at, "10.1.1.1", nodeID))
		}
		lq := q
		lq.Table = legacy
		items, truncated, err := reader.DistinctClients(ctx, lq, 0)
		require.NoError(t, err, "отсутствие колонки — деградация, не ошибка")
		require.False(t, truncated)
		require.Equal(t, []webport.LogClient{{IP: "10.1.1.1", Count: 2}}, items)
	})
}
