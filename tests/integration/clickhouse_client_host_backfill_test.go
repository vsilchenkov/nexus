//go:build integration

package integration

import (
	"context"
	"fmt"
	"sort"
	"sync"
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

// backfillResolver отвечает из карты и запоминает, какие IP у него спросили.
type backfillResolver struct {
	names map[string]string
	mu    sync.Mutex
	asked map[string]bool
}

func (r *backfillResolver) ResolveNow(_ context.Context, ip string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.asked == nil {
		r.asked = map[string]bool{}
	}
	r.asked[ip] = true
	return r.names[ip]
}

func (r *backfillResolver) askedIPs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.asked))
	for ip := range r.asked {
		out = append(out, ip)
	}
	sort.Strings(out)
	return out
}

// TestClickHouse_ClientHostBackfill — §106.3 на настоящем ClickHouse: одна
// мутация transform() дописывает имя строкам с пустым client_host и найденным
// именем; непустое имя, неразрешённый IP и пустой IP не трогаются; не-IP и
// loopback до резолвера не доходят; пока мутация не завершена, вторая поверх
// неё не ставится (признак нашей мутации совпадает с тем, как ClickHouse
// форматирует команду в system.mutations); повторный цикл после завершения
// новых мутаций не ставит; таблица без колонки client_host (деплой-окно)
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
		{"r6", "rabbitmq://mq:5672/vh", ""},
		{"r7", "127.0.0.1", ""},
	}
	for _, r := range rows {
		require.NoError(t, conn.Exec(ctx, insert, r.id, now, now, now, r.ip, r.host), "insert %s", r.id)
	}

	resolver := &backfillResolver{names: map[string]string{"192.168.2.5": "SRV-D-1C.vz78.vozovoz.ru"}}
	bf := senderuc.NewClientHostBackfill(clickhouse.StaticProvider(conn),
		backfillTables{table, legacy}, resolver, logging.NewNoop())

	countMutations := func(tbl string) uint64 {
		var n uint64
		require.NoError(t, conn.QueryRow(ctx,
			"SELECT count() FROM system.mutations WHERE database = ? AND table = ?", db, tbl).Scan(&n))
		return n
	}

	// Мутации исполняет фоновый планировщик слияний: пока он остановлен,
	// поставленная мутация висит незавершённой — как долгая мутация на бою.
	require.NoError(t, conn.Exec(ctx, "SYSTEM STOP MERGES "+table))
	require.NoError(t, bf.RunOnce(ctx))
	require.NoError(t, bf.RunOnce(ctx))
	assert.EqualValues(t, 1, countMutations(name), "вторая мутация поверх незавершённой не ставится")
	require.NoError(t, conn.Exec(ctx, "SYSTEM START MERGES "+table))
	waitCHMutations(t, ctx, conn, db, name)

	assert.Equal(t, []string{"192.168.2.5", "192.168.2.6"}, resolver.askedIPs(),
		"не-IP и loopback до резолвера не доходят")

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
		"r6": "",
		"r7": "",
	}, got)

	assert.EqualValues(t, 1, countMutations(name), "одна мутация на таблицу")
	assert.Zero(t, countMutations("test_client_host_backfill_legacy"), "таблица без client_host пропущена")

	// Повторный цикл: у разрешимых IP пустых строк не осталось — мутаций нет.
	require.NoError(t, bf.RunOnce(ctx))
	assert.EqualValues(t, 1, countMutations(name), "повторный цикл не ставит мутаций")
}
