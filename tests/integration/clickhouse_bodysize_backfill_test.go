//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"nexus/internal/platform/clickhouse"
	"nexus/internal/platform/logging"
)

// TestClickHouse_BodySizeBackfill — миграция §42-доп на таблице СТАРОЙ схемы
// (без request_size/response_size): EnsureBodySizeColumns добавляет колонки,
// BackfillBodySizes заполняет исторические строки байтовой длиной сохранённой
// копии тела (length(), байты — не руны). Повторный запуск guard'ом находит
// 0 строк и новых мутаций не планирует.
func TestClickHouse_BodySizeBackfill(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	conn, _, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	const (
		db    = "nexus_default"
		name  = "test_bodysize_backfill"
		table = db + "." + name
	)

	// Таблица старой схемы — как createNodeLogTable ДО §42-доп (без size-колонок).
	ddl := fmt.Sprintf(`CREATE TABLE %s (
		ID String,
		type String,
		http_method String,
		url String,
		method String,
		parameters String,
		request String,
		response String,
		status Int32,
		reason String,
		date_create Date,
		date_request DateTime,
		date_response DateTime,
		duration Int32,
		done UInt8,
		checksum_request FixedString(32),
		checksum_response FixedString(32),
		Host String,
		IP String,
		attempts Int32,
		attempts_details String,
		node_id String
	) ENGINE = MergeTree
	PARTITION BY toYYYYMM(date_create)
	ORDER BY (date_create, date_request, method)`, table)
	require.NoError(t, conn.Exec(ctx, ddl), "create legacy log table")

	// Две исторические строки: с телами (response — кириллица: 3 руны, 6 байт,
	// проверяем именно байтовую семантику length()) и совсем без тел.
	now := time.Now().UTC()
	insert := fmt.Sprintf(`INSERT INTO %s
		(ID, type, http_method, url, method, parameters, request, response,
		 status, reason, date_create, date_request, date_response, duration,
		 done, checksum_request, checksum_response, Host, IP, attempts,
		 attempts_details, node_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, table)
	for _, r := range []struct {
		id, request, response string
	}{
		{"log-1", "hello", "мир"},
		{"log-2", "", ""},
	} {
		require.NoError(t, conn.Exec(ctx, insert,
			r.id, "webhook", "POST", "http://example.test", "", "",
			r.request, r.response, int32(200), "OK", now, now, now, int32(10),
			uint8(1), "00000000000000000000000000000000", "00000000000000000000000000000000",
			"example.test", "127.0.0.1", int32(1), "", "node-1",
		), "insert legacy row %s", r.id)
	}

	logger := logging.NewNoop()
	tables := []string{table, table} // дубль — проверка дедупа
	clickhouse.EnsureBodySizeColumns(ctx, conn, tables, logger)
	clickhouse.BackfillBodySizes(ctx, conn, tables, logger)

	// Мутации асинхронные — ждём завершения.
	waitMutations := func() {
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			var pending uint64
			require.NoError(t, conn.QueryRow(ctx,
				"SELECT count() FROM system.mutations WHERE database = ? AND table = ? AND is_done = 0",
				db, name).Scan(&pending))
			if pending == 0 {
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatal("mutations not finished in time")
	}
	waitMutations()

	var reqSize, respSize int64
	require.NoError(t, conn.QueryRow(ctx,
		fmt.Sprintf("SELECT request_size, response_size FROM %s WHERE ID = 'log-1'", table)).
		Scan(&reqSize, &respSize))
	require.Equal(t, int64(5), reqSize, "request_size = length('hello')")
	require.Equal(t, int64(6), respSize, "response_size = байты 'мир', не руны")

	require.NoError(t, conn.QueryRow(ctx,
		fmt.Sprintf("SELECT request_size, response_size FROM %s WHERE ID = 'log-2'", table)).
		Scan(&reqSize, &respSize))
	require.Equal(t, int64(0), reqSize, "пустое тело остаётся 0")
	require.Equal(t, int64(0), respSize, "пустое тело остаётся 0")

	// Повторный запуск: guard находит 0 подходящих строк → новых мутаций нет.
	var before uint64
	require.NoError(t, conn.QueryRow(ctx,
		"SELECT count() FROM system.mutations WHERE database = ? AND table = ?",
		db, name).Scan(&before))
	clickhouse.BackfillBodySizes(ctx, conn, []string{table}, logger)
	var after uint64
	require.NoError(t, conn.QueryRow(ctx,
		"SELECT count() FROM system.mutations WHERE database = ? AND table = ?",
		db, name).Scan(&after))
	require.Equal(t, before, after, "повторный backfill не планирует мутаций")
}
