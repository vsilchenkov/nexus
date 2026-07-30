//go:build integration

package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/clickhouse"
	"nexus/internal/platform/logging"
	"nexus/internal/sender/adapter/out/chlog"
	webch "nexus/internal/web/adapter/out/clickhouse"
	"nexus/internal/web/usecase/port"
)

// TestClickHouse_StatusFilterComplement_E2E — §72.1: быстрые фильтры «ОК» и
// «Ошибки» вкладки логов разбивают набор записей НАЦЕЛО, без дыр и пересечений.
//
// До §72.1 SQL считал «ok» = 2xx, «err» = (>=400 OR =0). На реальных данных это
// давало две потери: запись с 3xx не попадала НИ В ОДИН фильтр (исчезала из UI
// при любом выборе, кроме «Все»), а незавершённая запись с кодом 2xx числилась
// успехом. Прежние E2E-фикстуры этого не ловили — в них не было ни 3xx, ни
// «2xx + done=0».
func TestClickHouse_StatusFilterComplement_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	conn, cfg, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	const table = "nexus_default.test_status_filter"
	createNodeLogTable(t, ctx, conn, table)

	logger := logging.NewNoop()
	provider := clickhouse.StaticProvider(conn)
	writer := chlog.New(provider, cfg, logger)
	defer writer.Stop(ctx)

	now := time.Now().UTC().Truncate(time.Second)
	rec := func(id string, status int32, done bool) *domain.LogRecord {
		return &domain.LogRecord{
			ID:               id,
			Type:             domain.RootMethodRequest,
			URL:              "https://example.com/upstream",
			Method:           "POST",
			Status:           status,
			DateCreate:       now,
			DateRequest:      now,
			DateResponse:     now,
			Done:             done,
			ChecksumRequest:  strings.Repeat("a", 32),
			ChecksumResponse: strings.Repeat("b", 32),
			AttemptsDetails:  "[]",
		}
	}

	// По одной записи на каждый класс исхода.
	const (
		idOK       = "00000000-0000-0000-0000-0000000000a1" // доставлено, 2xx
		idRedirect = "00000000-0000-0000-0000-0000000000a2" // доставлено, 3xx (§50)
		idPending  = "00000000-0000-0000-0000-0000000000a3" // 2xx, но не завершено
		idServer   = "00000000-0000-0000-0000-0000000000a4" // 5xx
		idTimeout  = "00000000-0000-0000-0000-0000000000a5" // сетевой сбой
	)
	writer.Write(ctx, table, rec(idOK, 200, true))
	writer.Write(ctx, table, rec(idRedirect, 302, true))
	writer.Write(ctx, table, rec(idPending, 200, false))
	writer.Write(ctx, table, rec(idServer, 500, true))
	writer.Write(ctx, table, rec(idTimeout, 0, false))
	require.NoError(t, writer.Flush(ctx))

	deadline := time.Now().Add(20 * time.Second)
	var n uint64
	for time.Now().Before(deadline) {
		require.NoError(t, conn.QueryRow(ctx, fmt.Sprintf("SELECT count() FROM %s", table)).Scan(&n))
		if n >= 5 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.EqualValues(t, 5, n, "expected 5 rows in %s", table)

	reader := webch.NewLogReader(provider, logger)
	ids := func(status string) []string {
		t.Helper()
		recs, err := reader.Search(ctx, port.LogQuery{Table: table, Status: status, Limit: 50})
		require.NoError(t, err)
		out := make([]string, 0, len(recs))
		for _, r := range recs {
			out = append(out, r.ID)
		}
		return out
	}

	okIDs, errIDs, allIDs := ids("ok"), ids("err"), ids("")

	require.ElementsMatch(t, []string{idOK, idRedirect}, okIDs,
		"успех = завершено и 2xx/3xx; редирект обязан попасть сюда, а не пропасть")
	require.ElementsMatch(t, []string{idPending, idServer, idTimeout}, errIDs,
		"ошибка = полное дополнение успеха, включая незавершённые с кодом 2xx")

	// Инвариант, ради которого всё затевалось: ok ∪ err = все, ok ∩ err = ∅.
	require.Len(t, allIDs, len(okIDs)+len(errIDs), "фильтры не покрывают набор нацело")
	require.ElementsMatch(t, allIDs, append(append([]string{}, okIDs...), errIDs...))

	// Счётчик «из M» (§67) обязан считать ровно то, что показывает список —
	// у Search и Count общий searchConds, и это тоже проверяем на реальном CH.
	for _, tc := range []struct {
		status string
		want   int
	}{{"ok", len(okIDs)}, {"err", len(errIDs)}, {"", len(allIDs)}} {
		total, err := reader.Count(ctx, port.LogQuery{Table: table, Status: tc.status})
		require.NoError(t, err)
		require.EqualValues(t, tc.want, total, "count(status=%q) расходится со списком", tc.status)
	}

	// Семантика done не менялась (§35 KPI неудачных доставок держится на ней).
	pending, err := reader.Count(ctx, port.LogQuery{Table: table, Done: "no"})
	require.NoError(t, err)
	require.EqualValues(t, 2, pending, "done=no — строго незавершённые, независимо от кода")
}
