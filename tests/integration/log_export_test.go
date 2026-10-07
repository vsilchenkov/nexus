//go:build integration

package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	chgo "github.com/ClickHouse/clickhouse-go/v2"
	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/clickhouse"
	"nexus/internal/platform/logging"
	webch "nexus/internal/web/adapter/out/clickhouse"
	"nexus/internal/web/usecase/port"
)

// exportRun — один прогон доставки для тестов выгрузки §115.
type exportRun struct {
	id     string
	at     time.Time
	done   bool
	status int32
	req    string
}

const exportNodeID = "33333333-3333-3333-3333-333333333333"

// insertExportRuns пишет прогоны одним батчем; date_create — UTC-день
// date_request (инвариант write-path Nexus, §72.4).
func insertExportRuns(t *testing.T, ctx context.Context, conn chdriver.Conn, table string, runs []exportRun) {
	t.Helper()
	b, err := conn.PrepareBatch(ctx, "INSERT INTO "+table)
	require.NoError(t, err)
	for _, r := range runs {
		done := uint8(0)
		if r.done {
			done = 1
		}
		require.NoError(t, b.Append(
			r.id, "request", "POST", "https://example.com/u", "", "", r.req, "",
			r.status, "", r.at.UTC().Truncate(24*time.Hour), r.at, r.at,
			int32(1), done, strings.Repeat("a", 32), strings.Repeat("b", 32),
			"h", "127.0.0.1", "", int32(1), "[]", exportNodeID, int64(len(r.req)), int64(0),
		))
	}
	require.NoError(t, b.Send())
	waitRows(t, ctx, conn, table, uint64(len(runs)))
}

// exportKeyIDs — идентификаторы ключей выгрузки.
func exportKeyIDs(keys []port.LogExportKey) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k.ID)
	}
	return out
}

// TestLogReader_Export_MatchesSearch — ГЛАВНЫЙ тест §115.7: выгрузка отдаёт те
// же записи, в том же порядке и тем же прогоном, что и список журнала под тем же
// фильтром. Запись с несколькими прогонами под фильтром «Ошибки» выгружается
// своим новейшим ОШИБОЧНЫМ прогоном, а не новейшим вообще.
func TestLogReader_Export_MatchesSearch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	conn, _, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	const table = "nexus_default.test_export_match"
	createNodeLogTable(t, ctx, conn, table)

	base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	at := func(s int) time.Time { return base.Add(time.Duration(s) * time.Second) }
	runs := []exportRun{
		{id: "a", at: at(0), done: false, status: 500, req: "aaaaaaaaaa"},
		{id: "b", at: at(1), done: true, status: 200, req: "bb"},
		{id: "c", at: at(2), done: false, status: 502},
		{id: "a", at: at(3), done: true, status: 200, req: "aaaaaaaaaa"},
		{id: "c", at: at(4), done: false, status: 503},
		{id: "d", at: at(5), done: true, status: 200, req: "привет мир"},
	}
	insertExportRuns(t, ctx, conn, table, runs)

	reader := webch.NewLogReader(clickhouse.StaticProvider(conn), logging.NewNoop())
	q := port.LogQuery{
		Table: table, NodeID: exportNodeID, DateCreateAligned: true,
		SinceMs: at(-1).UnixMilli(), UntilMs: at(60).UnixMilli(),
	}

	for _, tc := range []struct {
		status string
		wantA  time.Time // какой прогон записи «a» обязан попасть в выгрузку
	}{
		{status: "", wantA: at(3)},
		{status: "ok", wantA: at(3)},
		{status: "err", wantA: at(0)},
	} {
		t.Run("status="+tc.status, func(t *testing.T) {
			qq := q
			qq.Status = tc.status
			listQ := qq
			listQ.Limit = 500
			list, err := reader.Search(ctx, listQ)
			require.NoError(t, err)

			keys, err := reader.ExportKeys(ctx, qq, 100)
			require.NoError(t, err)
			require.Equal(t, searchIDs(t, ctx, reader, listQ), exportKeyIDs(keys), "порядок и состав — как у списка")

			rows, err := reader.ExportRows(ctx, qq, keys, 4)
			require.NoError(t, err)
			require.Len(t, rows, len(list))
			for i, row := range rows {
				require.Equal(t, list[i].ID, row.Rec.ID)
				require.True(t, list[i].DateRequest.Equal(row.Rec.DateRequest), "тот же прогон, что в списке: %s", row.Rec.ID)
				require.Equal(t, list[i].Status, row.Rec.Status)
				require.Equal(t, keys[i].TsMs, row.Rec.DateRequest.UnixMilli(), "ключ несёт время выгружаемого прогона")
				if row.Rec.ID == "a" {
					require.True(t, tc.wantA.Equal(row.Rec.DateRequest), "запись a: прогон %v, ждали %v", row.Rec.DateRequest, tc.wantA)
				}
				if row.Rec.ID == "d" {
					require.Equal(t, "прив", row.Rec.Request, "тело урезано по символам, а не по байтам")
					require.EqualValues(t, utf8.RuneCountInString("привет мир"), row.ReqRunes)
				}
			}

			short, err := reader.ExportKeys(ctx, qq, 2)
			require.NoError(t, err)
			require.Equal(t, exportKeyIDs(keys)[:min(2, len(keys))], exportKeyIDs(short), "лимит берёт начало того же порядка")
		})
	}

	t.Run("без тел", func(t *testing.T) {
		keys, err := reader.ExportKeys(ctx, q, 100)
		require.NoError(t, err)
		rows, err := reader.ExportRows(ctx, q, keys, 0)
		require.NoError(t, err)
		require.Len(t, rows, 4)
		for _, row := range rows {
			require.Empty(t, row.Rec.Request)
			require.Zero(t, row.ReqRunes)
			require.NotZero(t, row.Rec.Status, "метаданные читаются и без тел")
		}
	})

	t.Run("пропавшая запись не ломает батч", func(t *testing.T) {
		keys, err := reader.ExportKeys(ctx, q, 100)
		require.NoError(t, err)
		gone := append([]port.LogExportKey{{ID: "vanished", TsMs: at(2).UnixMilli(), DateCreate: keys[0].DateCreate}}, keys...)
		rows, err := reader.ExportRows(ctx, q, gone, 0)
		require.NoError(t, err)
		require.Len(t, rows, len(keys), "запись без строки пропускается, остальные — в порядке ключей")
	})

	t.Run("оценка", func(t *testing.T) {
		st, err := reader.ExportStats(ctx, q, 5)
		require.NoError(t, err)
		require.EqualValues(t, 4, st.Records)
		require.EqualValues(t, 6, st.Rows)
		// least(size, 5): a 5+5, b 2, c 0+0, d 5.
		require.EqualValues(t, 17, st.ReqBytes)
		require.Zero(t, st.RespBytes)

		errQ := q
		errQ.Status = "err"
		st, err = reader.ExportStats(ctx, errQ, 5)
		require.NoError(t, err)
		require.EqualValues(t, 2, st.Records, "a(t0) и c")
		require.EqualValues(t, 3, st.Rows)
	})
}

// TestLogReader_Export_KeysDedupAcrossRounds — запись с тысячами прогонов
// занимает целые раунды чтения ключей (top-N идёт по строкам), и добор
// keyset-курсором обязан дойти до остальных записей, не потеряв ни одной и не
// повторив «горячую».
func TestLogReader_Export_KeysDedupAcrossRounds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	conn, _, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	const table = "nexus_default.test_export_rounds"
	createNodeLogTable(t, ctx, conn, table)

	base := time.Now().UTC().Truncate(time.Second).Add(-3 * time.Hour)
	runs := make([]exportRun, 0, 5)
	for i := range 5 {
		runs = append(runs, exportRun{id: fmt.Sprintf("r%d", i), at: base.Add(time.Duration(i) * time.Second), done: true, status: 200})
	}
	insertExportRuns(t, ctx, conn, table, runs)
	// 3 000 прогонов одной записи — все новее остальных: первые раунды состоят
	// из одних дублей.
	const hotRuns = 3000
	hotFrom := base.Add(time.Minute).Format(time.DateTime)
	require.NoError(t, conn.Exec(ctx, fmt.Sprintf(`
		INSERT INTO %s SELECT 'hot', 'request', 'POST', 'u', '', '', '', '',
			200, '', toDate(toDateTime('%s') + number), toDateTime('%s') + number, toDateTime('%s') + number,
			1, 1, repeat('a', 32), repeat('b', 32), '', '', '', 1, '[]', '%s', 0, 0
		FROM numbers(%d)`, table, hotFrom, hotFrom, hotFrom, exportNodeID, hotRuns)))
	waitRows(t, ctx, conn, table, uint64(len(runs)+hotRuns))

	reader := webch.NewLogReader(clickhouse.StaticProvider(conn), logging.NewNoop())
	q := port.LogQuery{Table: table, NodeID: exportNodeID, DateCreateAligned: true}

	want := []string{"hot", "r4", "r3", "r2", "r1", "r0"}
	keys, err := reader.ExportKeys(ctx, q, len(want))
	require.NoError(t, err)
	require.Equal(t, want, exportKeyIDs(keys))
	require.Equal(t, base.Add(time.Minute+(hotRuns-1)*time.Second).UnixMilli(), keys[0].TsMs, "у горячей записи — новейший прогон")

	all, err := reader.ExportKeys(ctx, q, 100)
	require.NoError(t, err)
	require.Equal(t, want, exportKeyIDs(all), "меньше лимита — значит, записей больше нет")
}

// TestLogReader_Export_RowsPrunedOnExternalTable — батч строк читает узкое
// окно, а не таблицу (§115.7). Внешняя таблица §64 — худший случай: окна по
// date_create из периода нет (DateCreateAligned=false), а без условия по
// первому столбцу ключа ClickHouse не отсекает гранулы и по date_request
// (первая версия адаптера читала 200 000 строк из 200 000). Сужает батч
// date_create, взятый из самих ключей, — поэтому запись с посторонним
// date_create (её пишет чужой сервис) всё равно находится.
func TestLogReader_Export_RowsPrunedOnExternalTable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	conn, _, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	const (
		table   = "nexus_default.test_export_external"
		total   = 200_000
		stepSec = 1 // плотно, как на живой таблице: гранула лежит внутри дня
		baseTS  = "2026-05-01 00:00:00"
		alienID = "alien-1"
	)
	createNodeLogTable(t, ctx, conn, table)
	// node_id пуст, как во внешней таблице: её строки пишет не Sender.
	require.NoError(t, conn.Exec(ctx, fmt.Sprintf(`
		INSERT INTO %s SELECT
			concat('p-', toString(number)), 'request', 'POST', 'u', 'POST', '', repeat('x', 100), '',
			200, '', toDate(toDateTime('%s') + number*%d), toDateTime('%s') + number*%d, toDateTime('%s') + number*%d,
			1, 1, repeat('a', 32), repeat('b', 32), '', '', '', 1, '[]', '', 100, 0
		FROM numbers(%d)`, table, baseTS, stepSec, baseTS, stepSec, baseTS, stepSec, total)))
	base := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	alienAt := base.Add(24*time.Hour + 12*time.Hour + 15*time.Second).Format(time.DateTime)
	require.NoError(t, conn.Exec(ctx, fmt.Sprintf(`
		INSERT INTO %s SELECT '%s', 'request', 'POST', 'u', 'POST', '', 'alien', '',
			200, '', toDate('2026-01-05'), toDateTime('%s'), toDateTime('%s'),
			1, 1, repeat('a', 32), repeat('b', 32), '', '', '', 1, '[]', '', 5, 0`,
		table, alienID, alienAt, alienAt)))
	waitRows(t, ctx, conn, table, total+1)

	// Фактов о таблице нет (порт не подключён) — читатель считает её внешней и
	// пускает строки с пустым node_id (§64).
	reader := webch.NewLogReader(clickhouse.StaticProvider(conn), logging.NewNoop())
	q := port.LogQuery{
		Table: table, NodeID: exportNodeID,
		SinceMs: base.Add(24 * time.Hour).UnixMilli(),
		UntilMs: base.Add(48 * time.Hour).UnixMilli(),
	}
	keys, err := reader.ExportKeys(ctx, q, 50)
	require.NoError(t, err)
	require.Len(t, keys, 50)

	alienQ := q
	alienQ.SinceMs = base.Add(36 * time.Hour).UnixMilli()
	alienQ.UntilMs = base.Add(36*time.Hour + 15*time.Second).UnixMilli()
	alienKeys, err := reader.ExportKeys(ctx, alienQ, 10)
	require.NoError(t, err)
	require.Contains(t, exportKeyIDs(alienKeys), alienID, "запись с посторонним date_create видна, как в списке")
	alienRows, err := reader.ExportRows(ctx, alienQ, alienKeys, 100)
	require.NoError(t, err)
	require.Len(t, alienRows, len(alienKeys))

	dropQueryConditionCache(t, ctx, conn)
	const comment = "nexus_export_rows_external"
	qctx := chgo.Context(ctx, chgo.WithSettings(map[string]any{"log_comment": comment}))
	rows, err := reader.ExportRows(qctx, q, keys, 1000)
	require.NoError(t, err)
	require.Len(t, rows, 50)

	require.NoError(t, conn.Exec(ctx, "SYSTEM FLUSH LOGS"))
	var readRows uint64
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT read_rows FROM system.query_log
		WHERE type = 'QueryFinish' AND log_comment = ?
		ORDER BY event_time_microseconds DESC LIMIT 1`, comment).Scan(&readRows))
	t.Logf("export rows batch: read_rows=%d of %d", readRows, total)
	// Окно ключей — последние 50 секунд дня: это одна-две гранулы (8 192 строки),
	// а не сутки (86 400) и не таблица (200 000).
	require.Less(t, readRows*10, uint64(total),
		"батч обязан читать гранулы окна ключей, а не день или всю внешнюю таблицу")
}
