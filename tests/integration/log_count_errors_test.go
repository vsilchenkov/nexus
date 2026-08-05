//go:build integration

package integration

import (
	"context"
	"errors"
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

	// §35/§79.1: CountFailed считает ЗАПИСИ без единого прогона done=1. Здесь у
	// всех трёх неудачных записей (id 2,3,4) успешного прогона нет вовсе, так
	// что результат тот же, что и у прежнего счёта по строкам.
	nf, err := reader.CountFailed(ctx, failedQ(table, "", sinceMs, untilMs), false)
	require.NoError(t, err)
	require.EqualValues(t, 3, nf, "3 undelivered records expected")

	nf, err = reader.CountFailed(ctx, failedQ(table, "", now.Add(-2*time.Hour).UnixMilli(), now.Add(-time.Hour).UnixMilli()), false)
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

	q := webport.LogQuery{Table: table, SinceMs: sinceMs, UntilMs: untilMs, DateCreateAligned: true}

	// KPI — по уникальным запросам (ID): Total=3 (A,B,C), Delivered=2 (A,C), Errors=1 (B).
	kpi, err := reader.NodeKPI(ctx, q, false)
	require.NoError(t, err)
	require.EqualValues(t, 3, kpi.Total, "3 уникальных запроса (A,B,C)")
	require.EqualValues(t, 2, kpi.Delivered, "A и C хотя бы раз доставлены")
	require.EqualValues(t, 1, kpi.Errors, "B так и не доставлен")
	require.Greater(t, kpi.P95ms, 0.0, "p95 длительности > 0")

	// §79.5: ряд считает ЗАПИСИ, а не строки-прогоны, и сходится с KPI. До §79.5
	// здесь было 4 и 2 (сырые строки): A, спасённая повтором, красила свой столбец
	// и завышала «Всего» относительно числа над графиком.
	series, err := reader.NodeChart(ctx, q, webport.ChartQuery{StepSec: 150, ByRecord: true})
	require.NoError(t, err)
	require.NotEmpty(t, series)
	var sumCnt, sumErr uint64
	for _, p := range series {
		sumCnt += p.Count
		sumErr += p.Errors
	}
	require.EqualValues(t, kpi.Total, sumCnt, "Σ столбцов == «Всего» (3 записи, а не 4 строки)")
	require.EqualValues(t, kpi.Errors, sumErr, "красным помечена только B")

	// Окно в прошлом — пусто.
	past, err := reader.NodeKPI(ctx, webport.LogQuery{
		Table:             table,
		SinceMs:           now.Add(-2 * time.Hour).UnixMilli(),
		UntilMs:           now.Add(-time.Hour).UnixMilli(),
		DateCreateAligned: true,
	}, false)
	require.NoError(t, err)
	require.Zero(t, past.Total)
}

// TestLogReader_NodeIDFilter_E2E (§37/§61): несколько узлов в ОДНОЙ таблице
// различаются по node_id. Все per-node чтения/удаления фильтруют СТРОГО
// (node_id = ?): записи с пустым node_id узлу не принадлежат — на обычной
// таблице послабление убрано (осталось только для внешних таблиц §64).
//
// Критично: очистка одного узла не трогает ни записи другого, ни записи
// без идентификатора — раньше очистка узла A сносила и их.
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

	// Обычная (не внешняя) таблица: факты подключены, поэтому действует строгое
	// правило. Без подключённого порта читатель ушёл бы в деградацию — она
	// проверяется отдельным блоком в конце.
	reader := webch.NewLogReader(provider, logger)
	reader.SetTableUsage(attrUsage{counts: map[string]int{table: 2}})

	// CountFailed: каждый узел видит ТОЛЬКО свои записи, запись без node_id — ничью.
	na, err := reader.CountFailed(ctx, failedQ(table, nodeA, 0, 0), false)
	require.NoError(t, err)
	require.EqualValues(t, 2, na, "nodeA: только свои 2, запись без node_id ему не принадлежит")
	nb, err := reader.CountFailed(ctx, failedQ(table, nodeB, 0, 0), false)
	require.NoError(t, err)
	require.EqualValues(t, 3, nb, "nodeB: только свои 3")

	// FailedIDs nodeA — только a1,a2 (ни b*, ни записи без node_id).
	ids, _, err := reader.FailedIDs(ctx, failedQ(table, nodeA, 0, 0), 1000)
	require.NoError(t, err)
	require.Len(t, ids, 2)
	require.NotContains(t, ids, "00000000-0000-0000-0000-0000000000b1")
	require.NotContains(t, ids, "00000000-0000-0000-0000-00000000000c")

	// NodeKPI nodeA — 2 уникальных, все ошибки.
	kpi, err := reader.NodeKPI(ctx, webport.LogQuery{Table: table, NodeID: nodeA}, false)
	require.NoError(t, err)
	require.EqualValues(t, 2, kpi.Total)
	require.EqualValues(t, 2, kpi.Errors)

	// §79.2: удаляем строки СВОИХ неудачных записей; записи nodeB и запись без
	// node_id целы. Набор ID тот же, что отдал FailedIDs, — это и есть контракт
	// «отменяем и удаляем одно множество».
	deleted, err := reader.DeleteFailedRows(ctx, failedQ(table, nodeA, 0, 0), ids)
	require.NoError(t, err)
	require.EqualValues(t, 2, deleted)
	require.Eventually(t, func() bool {
		var remain uint64
		_ = conn.QueryRow(ctx, "SELECT count() FROM "+table).Scan(&remain)
		return remain == 4 // 3 записи nodeB + 1 без node_id
	}, 20*time.Second, 500*time.Millisecond, "выживают записи nodeB и запись без node_id")

	var orphan uint64
	require.NoError(t, conn.QueryRow(ctx, "SELECT count() FROM "+table+" WHERE node_id = ''").Scan(&orphan))
	require.EqualValues(t, 1, orphan, "очистка узла A не должна сносить записи без идентификатора")

	nb2, err := reader.CountFailed(ctx, failedQ(table, nodeB, 0, 0), false)
	require.NoError(t, err)
	require.EqualValues(t, 3, nb2, "nodeB не задет очисткой nodeA")

	// Деградация (§61.1): фактов о таблице нет — правило мягкое, иначе сбой
	// PostgreSQL спрятал бы содержимое внешних таблиц.
	degraded := webch.NewLogReader(provider, logger)
	nd, err := degraded.CountFailed(ctx, failedQ(table, nodeB, 0, 0), false)
	require.NoError(t, err)
	require.EqualValues(t, 4, nd, "без фактов о таблице действует послабление: 3 свои + 1 без node_id")
}

// attrUsage — port.NodeTableUsage с фиксированным снимком фактов о таблицах.
type attrUsage struct {
	counts   map[string]int
	external map[string]struct{}
}

func (u attrUsage) CountByCHTable(context.Context, string, string) (int, error) { return 0, nil }

func (u attrUsage) CountsByCHTable(context.Context) (map[string]int, error) { return u.counts, nil }

func (u attrUsage) ExternalCHTables(context.Context) (map[string]struct{}, error) {
	return u.external, nil
}

// foreignOwnership — гейт владения, отвечающий «таблица не наша»: так выглядит
// ЛЮБАЯ внешняя таблица §64 (маркера `__nexus_owner` у чужой БД нет) и таблица
// соседней ноды §70.
type foreignOwnership struct{}

func (foreignOwnership) Claim(context.Context, string) error              { return nil }
func (foreignOwnership) AssertOwnsDatabase(context.Context, string) error { return nil }
func (foreignOwnership) AssertOwnsTable(context.Context, string) error {
	return errNotOwned
}

var errNotOwned = errors.New("table is owned by another node")

// TestLogReader_ExternalTableAttribution_E2E (§64) — внешняя таблица осталась
// ЕДИНСТВЕННЫМ местом, где записи с пустым node_id засчитываются узлу.
//
// Боевой инцидент: писатель посторонний, node_id проставляет только Sender,
// поэтому строгий фильтр прятал единственное содержимое таблицы (на бою —
// 10 222 523 записи).
//
// Проверяется настоящим ClickHouse-SQL, а не только сборкой условия: узел на
// одиночной внешней таблице записи видит, а два ограничения остаются в силе —
// таблица без пометки external_table строга, общая внешняя строга тоже
// (§61 сильнее §64, иначе видны записи соседа). Владение таблицей на выбор
// фильтра больше не влияет — гейт §70.4 остался только на удалении.
func TestLogReader_ExternalTableAttribution_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	conn, cfg, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	const table = "nexus_default.external_attr"
	createNodeLogTable(t, ctx, conn, table)

	logger := logging.NewNoop()
	provider := clickhouse.StaticProvider(conn)
	writer := chlog.New(provider, cfg, logger)
	defer writer.Stop(ctx)

	now := time.Now().UTC().Truncate(time.Second)
	mk := func(id, nodeID string) *domain.LogRecord {
		return &domain.LogRecord{
			ID: id, Type: domain.RootMethodRequest, URL: "https://x", Method: "POST",
			Request: "{}", Response: "{}", Status: 0, DateCreate: now,
			DateRequest: now, DateResponse: now, Duration: 1, Done: false,
			ChecksumRequest: strings.Repeat("a", 32), ChecksumResponse: strings.Repeat("b", 32),
			Host: "h", IP: "127.0.0.1", Attempts: 1, AttemptsDetails: "[]", NodeID: nodeID,
		}
	}
	const nodeA = "33333333-3333-3333-3333-333333333333"
	// Слепок боя: три записи постороннего писателя (node_id = '') и одна своя.
	writer.Write(ctx, table, mk("00000000-0000-0000-0000-0000000000e1", ""))
	writer.Write(ctx, table, mk("00000000-0000-0000-0000-0000000000e2", ""))
	writer.Write(ctx, table, mk("00000000-0000-0000-0000-0000000000e3", ""))
	writer.Write(ctx, table, mk("00000000-0000-0000-0000-0000000000e4", nodeA))
	require.NoError(t, writer.Flush(ctx))

	require.Eventually(t, func() bool {
		var total uint64
		_ = conn.QueryRow(ctx, "SELECT count() FROM "+table).Scan(&total)
		return total == 4
	}, 20*time.Second, 200*time.Millisecond)

	// Читатель на каждый случай новый: снимок фактов кешируется на минуту.
	newReader := func(counts map[string]int, external map[string]struct{}) *webch.LogReaderCH {
		r := webch.NewLogReader(provider, logger)
		r.SetTableUsage(attrUsage{counts: counts, external: external})
		r.SetOwnership(foreignOwnership{})
		return r
	}
	self := map[string]int{table: 1}
	shared := map[string]int{table: 2}
	marked := map[string]struct{}{table: {}}

	got, err := newReader(self, marked).CountFailed(ctx, failedQ(table, nodeA, 0, 0), false)
	require.NoError(t, err)
	require.EqualValues(t, 4, got,
		"§64: на внешней одиночной таблице узел видит и записи постороннего писателя — ради этого узел и заведён")

	got, err = newReader(self, nil).CountFailed(ctx, failedQ(table, nodeA, 0, 0), false)
	require.NoError(t, err)
	require.EqualValues(t, 1, got,
		"таблица без пометки external_table строга даже будучи личной: послабление не должно расползаться за пределы §64")

	got, err = newReader(shared, marked).CountFailed(ctx, failedQ(table, nodeA, 0, 0), false)
	require.NoError(t, err)
	require.EqualValues(t, 1, got,
		"§61 сильнее §64: общую таблицу узел читает строго, даже если она помечена внешней")

	// Гейт разрушающих операций фиксом не затронут: читать чужую таблицу можно,
	// удалять из неё — нет.
	_, err = newReader(self, marked).DeleteFailedRows(ctx, failedQ(table, nodeA, 0, 0),
		[]string{"00000000-0000-0000-0000-0000000000e4"})
	require.Error(t, err, "§70.4: очистка в чужой таблице по-прежнему запрещена")
}
