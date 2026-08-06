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

// failedQ — запрос «Неудачных доставок» (§79.1). Зеркалит usecase.failedQuery:
// адаптер обязан получать именно Unresolved, а не Done="no".
func failedQ(table, nodeID string, sinceMs, untilMs int64) webport.LogQuery {
	return webport.LogQuery{
		Table:             table,
		NodeID:            nodeID,
		SinceMs:           sinceMs,
		UntilMs:           untilMs,
		Unresolved:        true,
		DateCreateAligned: true,
	}
}

// TestLogReader_UnresolvedFailed_E2E — §79.1 на реальном ClickHouse.
//
// Фикстура повторяет боевой слепок узла kz (2026-08-05): запись, спасённая
// авто-репроцессором, обязана исчезнуть из «Неудачных доставок» сама, без
// нажатия кнопки. На прежнем коде (предикат «есть строка done=0») этот тест
// красный: A попадает и в счётчик, и в набор ID, и в список.
func TestLogReader_UnresolvedFailed_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	conn, cfg, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	const table = "nexus_default.failed_unresolved"
	createNodeLogTable(t, ctx, conn, table)

	logger := logging.NewNoop()
	provider := clickhouse.StaticProvider(conn)
	writer := chlog.New(provider, cfg, logger)
	defer writer.Stop(ctx)

	const (
		nodeA = "11111111-1111-1111-1111-111111111111"
		idA   = "00000000-0000-0000-0000-0000000000a0" // упала, затем доставлена
		idB   = "00000000-0000-0000-0000-0000000000b0" // только упала
		idC   = "00000000-0000-0000-0000-0000000000c0" // доставлена сразу
		idD   = "00000000-0000-0000-0000-0000000000d0" // три неудачных прогона
	)

	now := time.Now().UTC().Truncate(time.Second)
	mk := func(id string, at time.Time, status int32, done bool) *domain.LogRecord {
		return &domain.LogRecord{
			ID: id, NodeID: nodeA, Type: domain.RootMethodRequestAsync, URL: "https://x",
			Method: "POST", Request: "{}", Response: "{}", Status: status,
			DateCreate: at, DateRequest: at, DateResponse: at, Duration: 1, Done: done,
			ChecksumRequest: strings.Repeat("a", 32), ChecksumResponse: strings.Repeat("b", 32),
			Host: "h", IP: "127.0.0.1", Attempts: 1, AttemptsDetails: "[]",
		}
	}
	// A: прогон упал, следующий прогон той же записи доставил её (репроцессор §36).
	writer.Write(ctx, table, mk(idA, now.Add(-10*time.Minute), 0, false))
	writer.Write(ctx, table, mk(idA, now.Add(-9*time.Minute), 200, true))
	// B: единственный прогон, неудачный.
	writer.Write(ctx, table, mk(idB, now.Add(-8*time.Minute), 0, false))
	// C: доставлена с первого раза.
	writer.Write(ctx, table, mk(idC, now.Add(-7*time.Minute), 200, true))
	// D: три неудачных прогона одной записи — это ОДНА неудачная доставка.
	writer.Write(ctx, table, mk(idD, now.Add(-6*time.Minute), 0, false))
	writer.Write(ctx, table, mk(idD, now.Add(-5*time.Minute), 500, false))
	writer.Write(ctx, table, mk(idD, now.Add(-4*time.Minute), 0, false))
	require.NoError(t, writer.Flush(ctx))

	require.Eventually(t, func() bool {
		var total uint64
		_ = conn.QueryRow(ctx, "SELECT count() FROM "+table).Scan(&total)
		return total == 7
	}, 20*time.Second, 200*time.Millisecond, "ожидаем 7 строк-прогонов")

	reader := webch.NewLogReader(provider, logger)
	reader.SetTableUsage(attrUsage{counts: map[string]int{table: 1}})
	q := failedQ(table, nodeA, 0, 0)

	t.Run("счётчик считает записи без успешного прогона", func(t *testing.T) {
		n, err := reader.CountFailed(ctx, q, false)
		require.NoError(t, err)
		require.EqualValues(t, 2, n,
			"неудачны только B и D; A спасена репроцессором, C доставлена сразу")
	})

	t.Run("счётчик тождествен Errors из NodeKPI", func(t *testing.T) {
		n, err := reader.CountFailed(ctx, q, false)
		require.NoError(t, err)
		kpi, err := reader.NodeKPI(ctx, webport.LogQuery{Table: table, NodeID: nodeA}, false)
		require.NoError(t, err)
		require.EqualValues(t, kpi.Errors, n,
			"KPI узла и KPI вкладки «Очередь» обязаны совпадать (бой: 1 против 13)")
		require.EqualValues(t, 4, kpi.Total, "записей четыре, прогонов семь")
	})

	t.Run("приблизительный режим даёт то же и не уходит в underflow", func(t *testing.T) {
		n, err := reader.CountFailed(ctx, q, true)
		require.NoError(t, err)
		require.EqualValues(t, 2, n)
	})

	t.Run("набор ID не содержит доставленных", func(t *testing.T) {
		ids, capped, err := reader.FailedIDs(ctx, q, 1000)
		require.NoError(t, err)
		require.False(t, capped)
		require.ElementsMatch(t, []string{idB, idD}, ids,
			"A обязана выпасть: её повтор уже уехал, и «Повторить все» шлёт дубль")
	})

	t.Run("список не показывает спасённую запись", func(t *testing.T) {
		lq := q
		lq.Limit = 50
		rows, err := reader.Search(ctx, lq)
		require.NoError(t, err)
		got := make([]string, 0, len(rows))
		for _, r := range rows {
			got = append(got, r.ID)
		}
		require.ElementsMatch(t, []string{idB, idD}, got)
	})

	t.Run("Count под тем же фильтром считает ту же единицу", func(t *testing.T) {
		n, err := reader.Count(ctx, q)
		require.NoError(t, err)
		require.EqualValues(t, 2, n, "«Показано N из M» не имеет права расходиться со списком")
	})

	t.Run("§81.5: отсев по маркеру причины убирает отменённые клиентом", func(t *testing.T) {
		// Ещё одна недоставленная запись, но с маркером «ушла вызывающая
		// сторона»: массовый повтор обязан её пропустить, а обычная выборка —
		// показывать (шина доставку не подтвердила, сигнал честный).
		const idE = "00000000-0000-0000-0000-0000000000e0"
		rec := mk(idE, now.Add(-3*time.Minute), 0, false)
		rec.Reason = domain.ReasonClientCanceled + ": waited 50000 ms, no response"
		writer.Write(ctx, table, rec)
		require.NoError(t, writer.Flush(ctx))
		require.Eventually(t, func() bool {
			var total uint64
			_ = conn.QueryRow(ctx, "SELECT count() FROM "+table).Scan(&total)
			return total == 8
		}, 20*time.Second, 200*time.Millisecond, "ожидаем 8 строк-прогонов")

		all, _, err := reader.FailedIDs(ctx, q, 1000)
		require.NoError(t, err)
		require.ElementsMatch(t, []string{idB, idD, idE}, all,
			"без отсева видны все недоставленные, включая отменённые клиентом")

		filtered := q
		filtered.ExcludeReasonPrefixes = []string{domain.ReasonClientCanceled}
		got, _, err := reader.FailedIDs(ctx, filtered, 1000)
		require.NoError(t, err)
		require.ElementsMatch(t, []string{idB, idD}, got,
			"массовый повтор не берёт запись, которую приёмник, вероятно, уже обработал")
	})
}

// TestMetricsReader_ChartByRecord_E2E — §79.5 на реальном ClickHouse: столбец
// считает ЗАПИСИ по интервалу прихода с ИТОГОВЫМ статусом.
//
// Фикстура — слепок аварии: запрос пришёл в первом часе и упал, повтор доставил
// его во втором. Ожидание пользователя (и контракт раздела): красный сегмент
// исчезает из столбца первого часа, а нового столбца во втором не появляется.
// На прежнем коде тест красный трижды: столбцы считали строки, ошибка оставалась
// в первом часе и дублировалась во втором.
func TestMetricsReader_ChartByRecord_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	conn, cfg, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	const table = "nexus_default.chart_by_record"
	createNodeLogTable(t, ctx, conn, table)

	logger := logging.NewNoop()
	provider := clickhouse.StaticProvider(conn)
	writer := chlog.New(provider, cfg, logger)
	defer writer.Stop(ctx)

	const (
		nodeA   = "11111111-1111-1111-1111-111111111111"
		idSaved = "00000000-0000-0000-0000-0000000000a3" // упал в 1-м часе, доставлен во 2-м
		idLost  = "00000000-0000-0000-0000-0000000000b3" // упал в 1-м часе и не доставлен
	)

	// Часовые бакеты: база выровнена по началу часа, чтобы прогоны заведомо легли
	// в соседние интервалы.
	base := time.Now().UTC().Truncate(time.Hour).Add(-3 * time.Hour)
	mk := func(id string, at time.Time, status int32, done bool) *domain.LogRecord {
		return &domain.LogRecord{
			ID: id, NodeID: nodeA, Type: domain.RootMethodRequestAsync, URL: "https://x",
			Method: "POST", Request: "{}", Response: "{}", Status: status,
			DateCreate: at, DateRequest: at, DateResponse: at, Duration: 1, Done: done,
			ChecksumRequest: strings.Repeat("a", 32), ChecksumResponse: strings.Repeat("b", 32),
			Host: "h", IP: "127.0.0.1", Attempts: 1, AttemptsDetails: "[]",
		}
	}
	writer.Write(ctx, table, mk(idSaved, base.Add(10*time.Minute), 0, false))
	writer.Write(ctx, table, mk(idSaved, base.Add(70*time.Minute), 200, true)) // следующий час
	writer.Write(ctx, table, mk(idLost, base.Add(20*time.Minute), 0, false))
	require.NoError(t, writer.Flush(ctx))

	require.Eventually(t, func() bool {
		var total uint64
		_ = conn.QueryRow(ctx, "SELECT count() FROM "+table).Scan(&total)
		return total == 3
	}, 20*time.Second, 200*time.Millisecond)

	reader := webch.NewLogReader(provider, logger)
	reader.SetTableUsage(attrUsage{counts: map[string]int{table: 1}})

	q := webport.LogQuery{
		Table:             table,
		NodeID:            nodeA,
		SinceMs:           base.Add(-time.Minute).UnixMilli(),
		UntilMs:           base.Add(3 * time.Hour).UnixMilli(),
		DateCreateAligned: true,
	}

	t.Run("по итогу записи: спасённая запись не красит свой столбец", func(t *testing.T) {
		series, err := reader.NodeChart(ctx, q, webport.ChartQuery{StepSec: 3600, ByRecord: true})
		require.NoError(t, err)

		var totalCount, totalErrors uint64
		nonEmpty := 0
		for _, p := range series {
			totalCount += p.Count
			totalErrors += p.Errors
			if p.Count > 0 {
				nonEmpty++
				require.EqualValues(t, 2, p.Count, "обе записи пришли в один час")
				require.EqualValues(t, 1, p.Errors, "красной осталась только недоставленная")
			}
		}
		require.Equal(t, 1, nonEmpty, "повтор не создаёт нового столбца во втором часе")
		require.EqualValues(t, 2, totalCount, "Σ столбцов == числу записей, а не прогонов")
		require.EqualValues(t, 1, totalErrors)
	})

	t.Run("Σ столбцов сходится с KPI", func(t *testing.T) {
		kpi, err := reader.NodeKPI(ctx, q, false)
		require.NoError(t, err)
		series, err := reader.NodeChart(ctx, q, webport.ChartQuery{StepSec: 3600, ByRecord: true})
		require.NoError(t, err)

		var sum, errs uint64
		for _, p := range series {
			sum += p.Count
			errs += p.Errors
		}
		require.EqualValues(t, kpi.Total, sum, "«Всего» над графиком и сумма столбцов — одна величина")
		require.EqualValues(t, kpi.Errors, errs)
	})

	t.Run("режим по прогонам виден как расхождение — потому и помечается", func(t *testing.T) {
		series, err := reader.NodeChart(ctx, q, webport.ChartQuery{StepSec: 3600, ByRecord: false})
		require.NoError(t, err)
		nonEmpty := 0
		for _, p := range series {
			if p.Count > 0 {
				nonEmpty++
			}
		}
		require.Equal(t, 2, nonEmpty,
			"в дешёвой форме повтор виден отдельным столбцом — ровно поэтому ответ помечается chart_unit=attempts")
	})

	t.Run("шаг задаёт ширину столбца", func(t *testing.T) {
		series, err := reader.NodeChart(ctx, q, webport.ChartQuery{StepSec: 86400, ByRecord: true})
		require.NoError(t, err)
		require.NotEmpty(t, series)
		if len(series) > 1 {
			require.EqualValues(t, 86_400_000, series[1].TsMs-series[0].TsMs, "столбцы по суткам")
		}
	})
}

// TestLogReader_DeleteFailedRows_E2E — §79.2: убираем строки done=0 указанных
// записей и НЕ трогаем ни их успешные прогоны, ни чужие записи.
func TestLogReader_DeleteFailedRows_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	conn, cfg, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	const table = "nexus_default.failed_rows_delete"
	createNodeLogTable(t, ctx, conn, table)

	logger := logging.NewNoop()
	provider := clickhouse.StaticProvider(conn)
	writer := chlog.New(provider, cfg, logger)
	defer writer.Stop(ctx)

	const (
		nodeA = "11111111-1111-1111-1111-111111111111"
		nodeB = "22222222-2222-2222-2222-222222222222"
		idA   = "00000000-0000-0000-0000-0000000000a1" // nodeA: упала и доставлена
		idB   = "00000000-0000-0000-0000-0000000000b1" // nodeA: только упала
		idX   = "00000000-0000-0000-0000-0000000000c1" // nodeB: только упала
	)

	now := time.Now().UTC().Truncate(time.Second)
	mk := func(id, nodeID string, status int32, done bool) *domain.LogRecord {
		return &domain.LogRecord{
			ID: id, NodeID: nodeID, Type: domain.RootMethodRequestAsync, URL: "https://x",
			Method: "POST", Request: "{}", Response: "{}", Status: status,
			DateCreate: now, DateRequest: now, DateResponse: now, Duration: 1, Done: done,
			ChecksumRequest: strings.Repeat("a", 32), ChecksumResponse: strings.Repeat("b", 32),
			Host: "h", IP: "127.0.0.1", Attempts: 1, AttemptsDetails: "[]",
		}
	}
	writer.Write(ctx, table, mk(idA, nodeA, 0, false))
	writer.Write(ctx, table, mk(idA, nodeA, 200, true))
	writer.Write(ctx, table, mk(idB, nodeA, 0, false))
	writer.Write(ctx, table, mk(idX, nodeB, 0, false))
	require.NoError(t, writer.Flush(ctx))

	require.Eventually(t, func() bool {
		var total uint64
		_ = conn.QueryRow(ctx, "SELECT count() FROM "+table).Scan(&total)
		return total == 4
	}, 20*time.Second, 200*time.Millisecond)

	reader := webch.NewLogReader(provider, logger)
	reader.SetTableUsage(attrUsage{counts: map[string]int{table: 2}})

	// Убираем обе записи nodeA — включая ту, у которой есть успешный прогон:
	// именно так поступает «Очистить все неудачные», если оператор передал их явно.
	deleted, err := reader.DeleteFailedRows(ctx, failedQ(table, nodeA, 0, 0), []string{idA, idB})
	require.NoError(t, err)
	require.EqualValues(t, 2, deleted, "возвращаются ЗАПИСИ, а не строки")

	require.Eventually(t, func() bool {
		var remain uint64
		_ = conn.QueryRow(ctx, "SELECT count() FROM "+table).Scan(&remain)
		return remain == 2
	}, 20*time.Second, 500*time.Millisecond, "уходят только строки done=0 записей nodeA")

	var okRows uint64
	require.NoError(t, conn.QueryRow(ctx,
		"SELECT count() FROM "+table+" WHERE ID = ? AND done = 1", idA).Scan(&okRows))
	require.EqualValues(t, 1, okRows, "успешный прогон записи A остаётся: история не теряется")

	var foreign uint64
	require.NoError(t, conn.QueryRow(ctx,
		"SELECT count() FROM "+table+" WHERE node_id = ?", nodeB).Scan(&foreign))
	require.EqualValues(t, 1, foreign, "записи соседнего узла в общей таблице не тронуты")
}

// TestLogReader_DeleteFailedRows_MirrorsReadFilter_E2E — §79.2: очистка убирает
// ровно то, что узел ВИДИТ. На таблице, помеченной внешней (§64), пустой node_id
// штатен для каждой строки и такие записи показываются в «Неудачных доставках»,
// значит и «Очистить» обязана их убрать — иначе кнопка молча не работает (этот
// разрыв поймал TestAsyncQueue_PurgeFailed_E2E).
//
// Данные постороннего писателя защищает гейт §70.4 (на чужой БД DML запрещён
// целиком) и точный список ID, а не сужение фильтра.
func TestLogReader_DeleteFailedRows_MirrorsReadFilter_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	conn, cfg, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	const table = "nexus_default.failed_rows_strict"
	createNodeLogTable(t, ctx, conn, table)

	logger := logging.NewNoop()
	provider := clickhouse.StaticProvider(conn)
	writer := chlog.New(provider, cfg, logger)
	defer writer.Stop(ctx)

	const (
		nodeA   = "11111111-1111-1111-1111-111111111111"
		idMine  = "00000000-0000-0000-0000-0000000000a2"
		idAlien = "00000000-0000-0000-0000-0000000000e2" // строка постороннего писателя
	)

	now := time.Now().UTC().Truncate(time.Second)
	mk := func(id, nodeID string) *domain.LogRecord {
		return &domain.LogRecord{
			ID: id, NodeID: nodeID, Type: domain.RootMethodRequestAsync, URL: "https://x",
			Method: "POST", Request: "{}", Response: "{}", Status: 0,
			DateCreate: now, DateRequest: now, DateResponse: now, Duration: 1, Done: false,
			ChecksumRequest: strings.Repeat("a", 32), ChecksumResponse: strings.Repeat("b", 32),
			Host: "h", IP: "127.0.0.1", Attempts: 1, AttemptsDetails: "[]",
		}
	}
	writer.Write(ctx, table, mk(idMine, nodeA))
	writer.Write(ctx, table, mk(idAlien, "")) // node_id пуст — пишет посторонний сервис
	require.NoError(t, writer.Flush(ctx))

	require.Eventually(t, func() bool {
		var total uint64
		_ = conn.QueryRow(ctx, "SELECT count() FROM "+table).Scan(&total)
		return total == 2
	}, 20*time.Second, 200*time.Millisecond)

	// Таблица личная и помечена внешней → чтение послабляет фильтр.
	reader := webch.NewLogReader(provider, logger)
	reader.SetTableUsage(attrUsage{
		counts:   map[string]int{table: 1},
		external: map[string]struct{}{table: {}},
	})
	q := failedQ(table, nodeA, 0, 0)

	seen, _, err := reader.FailedIDs(ctx, q, 1000)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{idMine, idAlien}, seen,
		"предпосылка §64: на внешней таблице узел ВИДИТ строки постороннего писателя")

	// Раз видим — значит и чистим: обе записи уходят из вида.
	deleted, err := reader.DeleteFailedRows(ctx, q, seen)
	require.NoError(t, err)
	require.EqualValues(t, 2, deleted)

	require.Eventually(t, func() bool {
		var remain uint64
		_ = conn.QueryRow(ctx, "SELECT count() FROM "+table).Scan(&remain)
		return remain == 0
	}, 20*time.Second, 500*time.Millisecond,
		"на внешней таблице «Очистить» обязана убирать и записи без node_id — иначе кнопка врёт")

	// Гейт §70.4 — вот что защищает данные постороннего писателя: на таблице
	// чужой ноды удаление не проходит вовсе.
	guarded := webch.NewLogReader(provider, logger)
	guarded.SetTableUsage(attrUsage{
		counts:   map[string]int{table: 1},
		external: map[string]struct{}{table: {}},
	})
	guarded.SetOwnership(foreignOwnership{})
	_, err = guarded.DeleteFailedRows(ctx, q, []string{idMine})
	require.Error(t, err, "§70.4: в чужой таблице удаление запрещено")
}
