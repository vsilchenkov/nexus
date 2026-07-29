//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	chgo "github.com/ClickHouse/clickhouse-go/v2"
	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/clickhouse"
	"nexus/internal/platform/logging"
	webch "nexus/internal/web/adapter/out/clickhouse"
	"nexus/internal/web/usecase/port"
)

// TestClickHouse_DateCreatePruning_E2E — §72.4: временное окно дублируется по
// date_create (колонке PARTITION BY), и это:
//   - не меняет результат на таблицах, которыми управляет Nexus;
//   - кратно сокращает чтение — партиции вне окна не читаются;
//   - ОТКЛЮЧЕНО для внешних таблиц §64 (последний подтест показывает, почему
//     гейт обязателен: там сужение теряет запись).
//
// Объём тут не для красоты. На маленькой таблице (сотни строк) выигрыш не
// виден — при первой попытке замер на 900 строках показал «одна партиция
// читается и так», из-за чего оптимизацию едва не выбросили как бесполезную.
// На 200 000 строк видно настоящее поведение: условие по date_request партиции
// НЕ отсекает, читается вся таблица.
func TestClickHouse_DateCreatePruning_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	conn, _, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	const table = "nexus_default.test_pruning"
	createNodeLogTable(t, ctx, conn, table)

	// Данные вставляются одним INSERT ... SELECT FROM numbers: батч-writer на
	// таком объёме упирается во flush и тест становится медленным и шатким.
	// date_create == UTC-день date_request — инвариант write-path Nexus
	// (send.go, dlq_reprocess.go), на который и опирается сужение.
	const (
		total      = 200_000
		stepSec    = 30 // ~70 суток истории
		baseTS     = "2026-05-01 00:00:00"
		wantParts  = 3  // май, июнь, июль
		errEveryNt = 25 // каждая 25-я запись — недоставленная
	)
	require.NoError(t, conn.Exec(ctx, fmt.Sprintf(`
		INSERT INTO %s SELECT
			concat('p-', toString(number)), 'request', 'POST',
			'https://example.com/upstream', 'POST', '', '', '',
			if(number %% %d = 0, 0, 200), '',
			toDate(toDateTime('%s') + number*%d),
			toDateTime('%s') + number*%d,
			toDateTime('%s') + number*%d,
			1, if(number %% %d = 0, 0, 1),
			repeat('a', 32), repeat('b', 32), '', '', '', 1, '[]', '', 0, 0
		FROM numbers(%d)`,
		table, errEveryNt, baseTS, stepSec, baseTS, stepSec, baseTS, stepSec, errEveryNt, total)))
	waitRows(t, ctx, conn, table, total)

	var parts uint64
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT count() FROM system.parts
		 WHERE database = 'nexus_default' AND table = 'test_pruning' AND active`).Scan(&parts))
	require.EqualValues(t, wantParts, parts, "ожидалась одна часть на месячную партицию")

	reader := webch.NewLogReader(clickhouse.StaticProvider(conn), logging.NewNoop())
	base := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	// Окно — сутки в середине истории (типичный «покажи за вчера»).
	day := port.LogQuery{
		Table:   table,
		SinceMs: base.Add(40 * 24 * time.Hour).UnixMilli(),
		UntilMs: base.Add(41 * 24 * time.Hour).UnixMilli(),
		Limit:   500,
	}
	aligned := day
	aligned.DateCreateAligned = true

	t.Run("результат не зависит от сужения", func(t *testing.T) {
		plainIDs := searchIDs(t, ctx, reader, day)
		require.NotEmpty(t, plainIDs)
		require.ElementsMatch(t, plainIDs, searchIDs(t, ctx, reader, aligned),
			"сужение по date_create обязано быть прозрачным для результата")

		plainTotal, err := reader.Count(ctx, day)
		require.NoError(t, err)
		alignedTotal, err := reader.Count(ctx, aligned)
		require.NoError(t, err)
		require.Equal(t, plainTotal, alignedTotal, "счётчик «из M» тоже обязан совпадать")

		// То же под фильтром «Ошибки» — основной сценарий §72.
		errQ, errAligned := day, aligned
		errQ.Status, errAligned.Status = "err", "err"
		require.ElementsMatch(t, searchIDs(t, ctx, reader, errQ), searchIDs(t, ctx, reader, errAligned))
	})

	t.Run("чтение сокращается кратно", func(t *testing.T) {
		plainRows, plainParts := measureRead(t, ctx, conn, reader, day, "nexus_pruning_plain")
		alignedRows, alignedParts := measureRead(t, ctx, conn, reader, aligned, "nexus_pruning_aligned")
		t.Logf("read_rows: plain=%d aligned=%d; parts: plain=%d aligned=%d (в таблице %d)",
			plainRows, alignedRows, plainParts, alignedParts, total)

		require.EqualValues(t, total, plainRows,
			"без сужения ClickHouse читает всю таблицу — условие по date_request партиции не отсекает")
		require.Less(t, alignedParts, plainParts, "партиции вне окна не должны читаться")
		require.Less(t, alignedRows*4, plainRows,
			"сужение обязано сокращать чтение кратно, иначе оно не окупает риска")
	})

	// Внешняя таблица §64: посторонний писатель может поставить date_create, не
	// связанный с date_request. Сужение такую запись потеряет — поэтому usecase
	// не выдаёт разрешение при external_table.
	t.Run("гейт внешних таблиц спасает от потери записей", func(t *testing.T) {
		const alienID = "alien-1"
		// Ставим запись в самый конец окна: список сортируется по date_request
		// DESC, и она попадает на первую страницу — иначе её отсутствие в
		// выборке ничего не доказывало бы.
		req := base.Add(41*24*time.Hour - time.Second).Format("2006-01-02 15:04:05")
		require.NoError(t, conn.Exec(ctx, fmt.Sprintf(`
			INSERT INTO %s SELECT '%s', 'request', 'POST', 'u', 'POST', '', '', '',
				200, '', toDate('2026-01-05'), toDateTime('%s'), toDateTime('%s'),
				1, 1, repeat('a', 32), repeat('b', 32), '', '', '', 1, '[]', '', 0, 0`,
			table, alienID, req, req)))
		waitRows(t, ctx, conn, table, total+1)

		require.Contains(t, searchIDs(t, ctx, reader, day), alienID,
			"без сужения запись видна — так её и читает внешняя таблица §64")
		require.NotContains(t, searchIDs(t, ctx, reader, aligned), alienID,
			"с сужением запись теряется — ровно поэтому гейт по external_table обязателен")
	})
}

// searchIDs — идентификаторы записей под фильтром.
func searchIDs(t *testing.T, ctx context.Context, r *webch.LogReaderCH, q port.LogQuery) []string {
	t.Helper()
	recs, err := r.Search(ctx, q)
	require.NoError(t, err)
	out := make([]string, 0, len(recs))
	for _, rec := range recs {
		out = append(out, rec.ID)
	}
	return out
}

// waitRows — дождаться, пока вставка просочится в таблицу.
func waitRows(t *testing.T, ctx context.Context, conn chdriver.Conn, table string, want uint64) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var n uint64
	for time.Now().Before(deadline) {
		require.NoError(t, conn.QueryRow(ctx, fmt.Sprintf("SELECT count() FROM %s", table)).Scan(&n))
		if n >= want {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.EqualValues(t, want, n, "rows in %s", table)
}

// measureRead — сколько строк и кусков данных ClickHouse прочитал ради запроса.
// Метка log_comment проставляется настройкой запроса: адаптер передаёт ctx в
// conn.Query как есть, поэтому она доезжает до сервера и позволяет найти нужную
// строку в system.query_log.
func measureRead(
	t *testing.T, ctx context.Context, conn chdriver.Conn,
	r *webch.LogReaderCH, q port.LogQuery, comment string,
) (readRows, selectedParts uint64) {
	t.Helper()
	qctx := chgo.Context(ctx, chgo.WithSettings(map[string]any{"log_comment": comment}))
	_, err := r.Search(qctx, q)
	require.NoError(t, err)

	require.NoError(t, conn.Exec(ctx, "SYSTEM FLUSH LOGS"))
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT read_rows, ProfileEvents['SelectedParts']
		FROM system.query_log
		WHERE type = 'QueryFinish' AND log_comment = ?
		ORDER BY event_time_microseconds DESC LIMIT 1`, comment).Scan(&readRows, &selectedParts))
	return readRows, selectedParts
}
