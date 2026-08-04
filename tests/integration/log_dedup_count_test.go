//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/clickhouse"
	"nexus/internal/platform/logging"
	webch "nexus/internal/web/adapter/out/clickhouse"
	"nexus/internal/web/usecase/port"
)

// Параметры сида: 40 записей по 3 строки-попытки каждая = 120 строк.
// Именно это соотношение и дало боевое «Показано 17 из 40»: счётчик считал
// строки, список — записи.
const (
	dedupRecords = 40
	dedupTries   = 3
	dedupRows    = dedupRecords * dedupTries
)

// TestClickHouse_LogDedup_CountAndPage_E2E — §72.2/§77.2: список и счётчик
// логов обязаны считать ОДНУ единицу — запись (уникальный ID), а не строку
// ClickHouse.
//
// В таблице логов одна запись живёт несколькими строками: каждый повторный
// прогон доставки (redelivery Kafka, DLQ-репроцесс, replay) пишет свою строку с
// тем же ID (внутренние ретраи одного вызова, наоборот, схлопнуты в attempts/
// attempts_details одной строки). До этого теста ни один тест логов дублей ID
// не сеял — поэтому расхождение «Показано N из M» не ловил ни один гейт.
func TestClickHouse_LogDedup_CountAndPage_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	conn, _, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	const table = "nexus_default.test_dedup"
	createNodeLogTable(t, ctx, conn, table)
	insertRetriedLogs(t, ctx, conn, table)

	reader := webch.NewLogReader(clickhouse.StaticProvider(conn), logging.NewNoop())
	q := port.LogQuery{Table: table}

	t.Run("счётчик считает записи, а не строки-попытки", func(t *testing.T) {
		total, err := reader.Count(ctx, q)
		require.NoError(t, err)
		require.EqualValues(t, dedupRecords, total,
			"«из M» обязано совпадать с числом записей в списке; %d — это строки-попытки",
			dedupRows)
	})

	t.Run("страница отдаёт limit разных записей, каждую свежайшей попыткой", func(t *testing.T) {
		const limit = 10
		recs, err := reader.Search(ctx, port.LogQuery{Table: table, Limit: limit})
		require.NoError(t, err)
		require.Len(t, recs, limit)

		seen := map[string]bool{}
		for _, r := range recs {
			require.False(t, seen[r.ID], "страница вернула запись %s дважды", r.ID)
			seen[r.ID] = true
			require.EqualValues(t, dedupTries, r.Attempts,
				"на странице обязана быть свежайшая попытка записи %s — её и раскроет GetByID", r.ID)
		}
	})

	t.Run("скролл проходит все записи без ложного конца", func(t *testing.T) {
		const limit = 10
		seen := map[string]bool{}
		page := port.LogQuery{Table: table, Limit: limit}
		// Запас к dedupRecords/limit: страница может повторно отдать запись,
		// чья старая попытка лежит ниже курсора (её отсеивает дедуп фронта), —
		// это стоит лишних страниц, но не ложного конца истории.
		for i := range dedupRows/limit + 5 {
			recs, err := reader.Search(ctx, page)
			require.NoError(t, err)
			for _, r := range recs {
				seen[r.ID] = true
			}
			if len(recs) < limit {
				require.Len(t, seen, dedupRecords,
					"страница %d недобрала — но прочитаны не все записи (ложный конец истории)", i)
				return
			}
			oldest := recs[len(recs)-1]
			page.UntilMs = oldest.DateRequest.UnixMilli()
			page.BeforeID = oldest.ID
		}
		require.Len(t, seen, dedupRecords, "скролл обязан дойти до конца истории")
	})

	t.Run("счётчик неудачных считает записи", func(t *testing.T) {
		// Неудачная = запись, у которой в окне есть хотя бы одна попытка done=0:
		// ровно те ID, что отменяет и удаляет очистка «Неудачных доставок»
		// (FailedIDs/DeleteFailed). Сид: доставлены все попытки только у каждой
		// четвёртой записи.
		want := dedupRecords - dedupRecords/4
		n, err := reader.CountFailed(ctx, table, "", 0, 0)
		require.NoError(t, err)
		require.EqualValues(t, want, n,
			"KPI «неудачные доставки» обязан совпадать со списком неудачных и с числом отменяемых ID")

		ids, capped, err := reader.FailedIDs(ctx, table, "", 0, 0, 1000)
		require.NoError(t, err)
		require.False(t, capped)
		require.EqualValues(t, n, len(ids), "счётчик и список отменяемых ID обязаны сойтись")
	})

	t.Run("фильтр «в работе» не двоит записи", func(t *testing.T) {
		// Фильтры не меняют единицу счёта: и список, и счётчик — записи.
		fq := port.LogQuery{Table: table, Done: "no", Limit: 100}
		recs, err := reader.Search(ctx, fq)
		require.NoError(t, err)
		uniq := map[string]bool{}
		for _, r := range recs {
			uniq[r.ID] = true
		}
		require.Len(t, recs, len(uniq), "под фильтром страница не должна двоить записи")

		total, err := reader.Count(ctx, fq)
		require.NoError(t, err)
		require.EqualValues(t, len(recs), total, "«Показано N» и «из M» под фильтром обязаны сойтись")
	})
}

// insertRetriedLogs — сид таблицы логов с ПОВТОРНЫМИ строками на один ID
// (dedupTries строк на запись, разные date_request и номер попытки).
//
// Раскладка сделана так, чтобы покрыть все три жизненные ситуации:
//   - каждая 4-я запись доставлена всеми попытками (done=1) — не неудачная;
//   - следующая за ней доставлена только свежайшей попыткой (ретраи done=0);
//   - остальные не доставлены вовсе.
func insertRetriedLogs(t *testing.T, ctx context.Context, conn chdriver.Conn, table string) {
	t.Helper()
	// rec — индекс записи (0..dedupRecords-1), try — номер попытки в записи
	// (1..dedupTries); свежайшая попытка записи — последняя по date_request.
	require.NoError(t, conn.Exec(ctx, fmt.Sprintf(`
		INSERT INTO %s SELECT
			concat('r-', toString(intDiv(number, %[2]d))) AS id,
			'requestAsync', 'GET',
			'https://example.com/upstream?a=1', 'POST', 'a=1', '', '',
			if(done_flag, 200, 502), '',
			toDate(toDateTime('2026-02-01 00:00:00') + number*60),
			toDateTime('2026-02-01 00:00:00') + number*60,
			toDateTime('2026-02-01 00:00:00') + number*60,
			1, done_flag,
			repeat('a', 32), repeat('b', 32), '', '', '',
			toInt32(number %% %[2]d + 1), '[]', '', 0, 0
		FROM (
			SELECT
				number,
				intDiv(number, %[2]d) %% 4 = 0
					OR (intDiv(number, %[2]d) %% 4 = 1 AND number %% %[2]d = %[2]d - 1) AS done_flag
			FROM numbers(%[3]d)
		)`, table, dedupTries, dedupRows)))
	waitRows(t, ctx, conn, table, dedupRows)
}
