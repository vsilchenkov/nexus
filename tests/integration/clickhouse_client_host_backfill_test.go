//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/clickhouse"
	"nexus/internal/platform/logging"
	senderuc "nexus/internal/sender/usecase"
)

type backfillTables []string

func (t backfillTables) ListClickHouseTables(context.Context) ([]string, error) { return t, nil }

type backfillResolver map[string]string

func (r backfillResolver) ResolveNow(_ context.Context, ip string) string { return r[ip] }

// TestClickHouse_ClientHostBackfill — §106.3 на настоящем ClickHouse: одна
// мутация transform() дописывает имя строкам с пустым client_host и найденным
// именем; непустое имя, неразрешённый IP и пустой IP не трогаются; повторный
// цикл новых мутаций не ставит; таблица без колонки client_host (деплой-окно)
// пропускается без ошибки.
func TestClickHouse_ClientHostBackfill(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	conn, _, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	const (
		db     = "nexus_default"
		name   = "test_client_host_backfill"
		table  = db + "." + name
		legacy = db + ".test_client_host_backfill_legacy"
	)
	createNodeLogTable(t, ctx, conn, table)
	require.NoError(t, conn.Exec(ctx, fmt.Sprintf(
		"CREATE TABLE %s (ID String, IP String, date_create Date) ENGINE = MergeTree ORDER BY ID", legacy)))

	now := time.Now().UTC()
	insert := fmt.Sprintf(
		"INSERT INTO %s (ID, date_create, date_request, date_response, IP, client_host) VALUES (?, ?, ?, ?, ?, ?)", table)
	rows := []struct{ id, ip, host string }{
		{"r1", "192.168.2.5", ""},
		{"r2", "192.168.2.5", ""},
		{"r3", "192.168.2.6", ""},        // имени нет ни в PTR, ни в NetBIOS
		{"r4", "192.168.2.5", "keep.me"}, // уже заполнено — не трогаем
		{"r5", "", ""},                   // pull-узел без IP
	}
	for _, r := range rows {
		require.NoError(t, conn.Exec(ctx, insert, r.id, now, now, now, r.ip, r.host), "insert %s", r.id)
	}

	bf := senderuc.NewClientHostBackfill(clickhouse.StaticProvider(conn),
		backfillTables{table, legacy},
		backfillResolver{"192.168.2.5": "SRV-D-1C.vz78.vozovoz.ru"},
		logging.NewNoop())

	require.NoError(t, bf.RunOnce(ctx))
	waitCHMutations(t, ctx, conn, db, name)

	got := map[string]string{}
	res, err := conn.Query(ctx, fmt.Sprintf("SELECT ID, client_host FROM %s", table))
	require.NoError(t, err)
	for res.Next() {
		var id, host string
		require.NoError(t, res.Scan(&id, &host))
		got[id] = host
	}
	require.NoError(t, res.Err())
	require.NoError(t, res.Close())
	assert.Equal(t, map[string]string{
		"r1": "SRV-D-1C.vz78.vozovoz.ru",
		"r2": "SRV-D-1C.vz78.vozovoz.ru",
		"r3": "",
		"r4": "keep.me",
		"r5": "",
	}, got)

	countMutations := func(tbl string) uint64 {
		var n uint64
		require.NoError(t, conn.QueryRow(ctx,
			"SELECT count() FROM system.mutations WHERE database = ? AND table = ?", db, tbl).Scan(&n))
		return n
	}
	assert.EqualValues(t, 1, countMutations(name), "одна мутация на таблицу")
	assert.Zero(t, countMutations("test_client_host_backfill_legacy"), "таблица без client_host пропущена")

	// Повторный цикл: у разрешимых IP пустых строк не осталось — мутаций нет.
	require.NoError(t, bf.RunOnce(ctx))
	assert.EqualValues(t, 1, countMutations(name), "повторный цикл не ставит мутаций")
}
