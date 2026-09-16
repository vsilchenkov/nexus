//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/clickhouse"
	"nexus/internal/platform/logging"
	"nexus/internal/sender/adapter/out/chlog"
	webch "nexus/internal/web/adapter/out/clickhouse"
	webport "nexus/internal/web/usecase/port"
)

// TestLogReader_NodeMethodStats_E2E (§102) — рейтинг «Запросы по методам»
// настоящим ClickHouse-SQL.
//
// Главное, ради чего тест живёт: ИНВАРИАНТ Σ(methods.total) == NodeKPI.Total на
// данных с повторами. Карточка считает строку «Остальные» как
// `kpi.total − Σ(показанных)`, поэтому расхождение разбивки и KPI хотя бы на
// единицу рисует пользователю отрицательный или завышенный остаток. Юнит-тест
// SQL-строки этого не ловит: он сравнивает выражения, а не то, что ClickHouse по
// ним посчитает на записи, спасённой повтором (2 строки, 1 ID).
func TestLogReader_NodeMethodStats_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	conn, cfg, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	const table = "nexus_default.method_stats"
	createNodeLogTable(t, ctx, conn, table)

	logger := logging.NewNoop()
	provider := clickhouse.StaticProvider(conn)
	writer := chlog.New(provider, cfg, logger)
	defer writer.Stop(ctx)

	now := time.Now().UTC().Truncate(time.Second)
	// §39: Method — это подпуть (колонка `method`), HTTPMethod — глагол.
	mk := func(id, method string, status int32, done bool) *domain.LogRecord {
		return &domain.LogRecord{
			ID: id, Type: domain.RootMethodRequest, URL: "https://x",
			HTTPMethod: "POST", Method: method,
			Request: "{}", Response: "{}", Status: status, DateCreate: now,
			DateRequest: now, DateResponse: now, Duration: 10, Done: done,
			ChecksumRequest: strings.Repeat("a", 32), ChecksumResponse: strings.Repeat("b", 32),
			Host: "h", IP: "127.0.0.1", Attempts: 1, AttemptsDetails: "[]",
		}
	}

	// Длинное имя — ровно тот случай, ради которого в UI появилось сокращение.
	const (
		mLong  = "suggestions/api/4_1/rs/suggest/address"
		mGeo   = "api/4_1/rs/geolocate/address"
		mFirst = "aaa/first"
		mLast  = "zzz/last"
	)
	// mLong: 3 записи (4 строки) — a1 упала и спасена повтором, a3 не доставлена.
	writer.Write(ctx, table, mk("00000000-0000-0000-0000-0000000000a1", mLong, 500, false))
	writer.Write(ctx, table, mk("00000000-0000-0000-0000-0000000000a1", mLong, 200, true))
	writer.Write(ctx, table, mk("00000000-0000-0000-0000-0000000000a2", mLong, 200, true))
	writer.Write(ctx, table, mk("00000000-0000-0000-0000-0000000000a3", mLong, 0, false))
	// mGeo: 2 записи, обе доставлены.
	writer.Write(ctx, table, mk("00000000-0000-0000-0000-0000000000b1", mGeo, 200, true))
	writer.Write(ctx, table, mk("00000000-0000-0000-0000-0000000000b2", mGeo, 200, true))
	// Пустой method (обычный узел, не passthrough) — своя группа, а не «прочее».
	writer.Write(ctx, table, mk("00000000-0000-0000-0000-0000000000c1", "", 500, false))
	// Двое по одной записи — тай-брейкер `method ASC` при равном total.
	writer.Write(ctx, table, mk("00000000-0000-0000-0000-0000000000d1", mLast, 200, true))
	writer.Write(ctx, table, mk("00000000-0000-0000-0000-0000000000d2", mFirst, 200, true))
	require.NoError(t, writer.Flush(ctx))

	require.Eventually(t, func() bool {
		var rows uint64
		_ = conn.QueryRow(ctx, "SELECT count() FROM "+table).Scan(&rows)
		return rows == 9
	}, 20*time.Second, 200*time.Millisecond, "9 строк записаны (8 записей, одна с повтором)")

	reader := webch.NewLogReader(provider, logger)
	q := webport.LogQuery{
		Table:             table,
		SinceMs:           now.Add(-time.Hour).UnixMilli(),
		UntilMs:           now.Add(time.Hour).UnixMilli(),
		DateCreateAligned: true,
	}

	t.Run("инвариант Σ == KPI и порядок строк", func(t *testing.T) {
		kpi, err := reader.NodeKPI(ctx, q, false)
		require.NoError(t, err)
		require.EqualValues(t, 8, kpi.Total, "8 записей (не 9 строк: повтор a1 — та же запись)")
		require.EqualValues(t, 6, kpi.Delivered)

		stats, truncated, err := reader.NodeMethodStats(ctx, q, false, 20)
		require.NoError(t, err)
		require.False(t, truncated, "5 методов при лимите 20 — усечения нет")

		// Порядок: total DESC, затем method ASC. Пустой method сортируется
		// первым среди равных — карточка рисует его отдельной строкой.
		require.Equal(t, []webport.MethodStat{
			{Method: mLong, Total: 3, Delivered: 2},
			{Method: mGeo, Total: 2, Delivered: 2},
			{Method: "", Total: 1, Delivered: 0},
			{Method: mFirst, Total: 1, Delivered: 1},
			{Method: mLast, Total: 1, Delivered: 1},
		}, stats)

		var sumTotal, sumDelivered uint64
		for _, s := range stats {
			sumTotal += s.Total
			sumDelivered += s.Delivered
			assert.LessOrEqual(t, s.Delivered, s.Total, "доставлено не больше всего: %q", s.Method)
		}
		require.EqualValues(t, kpi.Total, sumTotal,
			"§102.3: Σ(по методам) == «Всего», иначе «Остальные» уходит в минус")
		require.EqualValues(t, kpi.Delivered, sumDelivered, "Σ доставленных тоже сходится с KPI")
	})

	t.Run("truncated при лимите меньше числа методов", func(t *testing.T) {
		stats, truncated, err := reader.NodeMethodStats(ctx, q, false, 2)
		require.NoError(t, err)
		require.True(t, truncated, "методов 5, лимит 2 — карточка обязана показать «+»")
		require.Len(t, stats, 2, "лишняя строка из LIMIT limit+1 наружу не отдаётся")
		require.Equal(t, mLong, stats[0].Method)

		// Граница: лимит ровно по числу групп — усечения нет.
		stats, truncated, err = reader.NodeMethodStats(ctx, q, false, 5)
		require.NoError(t, err)
		require.False(t, truncated)
		require.Len(t, stats, 5)
	})

	t.Run("approx считает то же самое и сходится со своим KPI", func(t *testing.T) {
		// На такой кардинальности HLL точен, поэтому сравниваем как есть:
		// тест стережёт не погрешность, а согласованность обеих веток
		// uniqueExprs между KPI и разбивкой.
		kpi, err := reader.NodeKPI(ctx, q, true)
		require.NoError(t, err)

		stats, _, err := reader.NodeMethodStats(ctx, q, true, 20)
		require.NoError(t, err)
		var sumTotal, sumDelivered uint64
		for _, s := range stats {
			sumTotal += s.Total
			sumDelivered += s.Delivered
		}
		require.EqualValues(t, kpi.Total, sumTotal)
		require.EqualValues(t, kpi.Delivered, sumDelivered)
	})

	t.Run("фильтры вкладки применяются к разбивке", func(t *testing.T) {
		// Тот же searchConds, что у KPI: иначе тултип показал бы долю от
		// «Всего», посчитанного под другим набором условий.
		byMethod := q
		byMethod.Method = mLong
		stats, _, err := reader.NodeMethodStats(ctx, byMethod, false, 20)
		require.NoError(t, err)
		require.Equal(t, []webport.MethodStat{{Method: mLong, Total: 3, Delivered: 2}}, stats)

		past := q
		past.SinceMs = now.Add(-2 * time.Hour).UnixMilli()
		past.UntilMs = now.Add(-time.Hour).UnixMilli()
		stats, truncated, err := reader.NodeMethodStats(ctx, past, false, 20)
		require.NoError(t, err)
		require.Empty(t, stats, "окно в прошлом — пустой рейтинг, а не ошибка")
		require.False(t, truncated)
	})
}

// TestLogReader_NodeMethodStats_ExternalTable_E2E (§64 + §102) — на внешней
// одиночной таблице разбивка обязана видеть записи постороннего писателя
// (node_id пуст), как их видят KPI и журнал. Иначе у узла, заведённого ровно
// ради чужой таблицы, карточка «Запросы по методам» вечно пуста при непустом
// «Всего» — и строка «Остальные» проглотила бы 100 %.
func TestLogReader_NodeMethodStats_ExternalTable_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	conn, cfg, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	const table = "nexus_default.method_stats_external"
	createNodeLogTable(t, ctx, conn, table)

	logger := logging.NewNoop()
	provider := clickhouse.StaticProvider(conn)
	writer := chlog.New(provider, cfg, logger)
	defer writer.Stop(ctx)

	now := time.Now().UTC().Truncate(time.Second)
	mk := func(id, method, nodeID string) *domain.LogRecord {
		return &domain.LogRecord{
			ID: id, Type: domain.RootMethodRequest, URL: "https://x",
			HTTPMethod: "POST", Method: method,
			Request: "{}", Response: "{}", Status: 200, DateCreate: now,
			DateRequest: now, DateResponse: now, Duration: 10, Done: true,
			ChecksumRequest: strings.Repeat("a", 32), ChecksumResponse: strings.Repeat("b", 32),
			Host: "h", IP: "127.0.0.1", Attempts: 1, AttemptsDetails: "[]", NodeID: nodeID,
		}
	}
	const nodeA = "44444444-4444-4444-4444-444444444444"
	writer.Write(ctx, table, mk("00000000-0000-0000-0000-0000000000f1", "foreign/one", ""))
	writer.Write(ctx, table, mk("00000000-0000-0000-0000-0000000000f2", "foreign/one", ""))
	writer.Write(ctx, table, mk("00000000-0000-0000-0000-0000000000f3", "own/two", nodeA))
	require.NoError(t, writer.Flush(ctx))

	require.Eventually(t, func() bool {
		var rows uint64
		_ = conn.QueryRow(ctx, "SELECT count() FROM "+table).Scan(&rows)
		return rows == 3
	}, 20*time.Second, 200*time.Millisecond)

	// Читатель на каждый случай новый: снимок фактов о таблицах кешируется.
	newReader := func(external map[string]struct{}) *webch.LogReaderCH {
		r := webch.NewLogReader(provider, logger)
		r.SetTableUsage(attrUsage{counts: map[string]int{table: 1}, external: external})
		r.SetOwnership(foreignOwnership{})
		return r
	}
	q := webport.LogQuery{Table: table, NodeID: nodeA}

	stats, _, err := newReader(map[string]struct{}{table: {}}).NodeMethodStats(ctx, q, false, 20)
	require.NoError(t, err)
	require.Equal(t, []webport.MethodStat{
		{Method: "foreign/one", Total: 2, Delivered: 2},
		{Method: "own/two", Total: 1, Delivered: 1},
	}, stats, "§64: на помеченной внешней таблице видны и записи с пустым node_id")

	stats, _, err = newReader(nil).NodeMethodStats(ctx, q, false, 20)
	require.NoError(t, err)
	require.Equal(t, []webport.MethodStat{{Method: "own/two", Total: 1, Delivered: 1}}, stats,
		"без пометки external_table разбивка строга — послабление не расползается за пределы §64")
}
