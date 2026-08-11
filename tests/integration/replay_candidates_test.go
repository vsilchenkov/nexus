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
	"nexus/internal/web/usecase/port"
)

// replayRow — короткая запись прогона доставки для тестов §85.
type replayRow struct {
	id   string
	at   time.Time
	done bool
	body string
}

// TestClickHouse_ReplayCandidates_MultiRunRecordOnce — ГЛАВНЫЙ тест §85.5.1.
//
// Запись с двумя прогонами (t1 и t3) при обходе страницами по одной записи
// обязана быть отдана РОВНО ОДИН РАЗ. `LIMIT 1 BY ID` схлопывает прогоны только
// внутри страницы, поэтому без пробы min(date_request) запись приходит дважды —
// и уходит получателю дублем (ровно то, что §79 стоило пятнадцати дублей в 1С).
func TestClickHouse_ReplayCandidates_MultiRunRecordOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	conn, cfg, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	const table = "nexus_default.test_replay_candidates"
	createNodeLogTable(t, ctx, conn, table)

	logger := logging.NewNoop()
	provider := clickhouse.StaticProvider(conn)
	writer := chlog.New(provider, cfg, logger)
	defer writer.Stop(ctx)

	base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	const nodeID = "11111111-1111-1111-1111-111111111111"

	// A: один прогон. B: ДВА прогона (неудача, затем успешный повтор §36).
	// C: один прогон. Порядок по времени: A(t0) B(t1) C(t2) B(t3).
	rows := []replayRow{
		{id: "aaaaaaaa-0000-0000-0000-000000000001", at: base, done: true, body: `{"n":1}`},
		{id: "bbbbbbbb-0000-0000-0000-000000000002", at: base.Add(1 * time.Second), done: false, body: `{"n":2}`},
		{id: "cccccccc-0000-0000-0000-000000000003", at: base.Add(2 * time.Second), done: true, body: `{"n":3}`},
		{id: "bbbbbbbb-0000-0000-0000-000000000002", at: base.Add(3 * time.Second), done: true, body: `{"n":2}`},
	}
	for _, r := range rows {
		writer.Write(ctx, table, &domain.LogRecord{
			ID:               r.id,
			Type:             domain.RootMethodRequestAsync,
			HTTPMethod:       "POST",
			URL:              "https://example.com/u",
			Request:          r.body,
			RequestSize:      int64(len(r.body)),
			Status:           200,
			DateCreate:       r.at,
			DateRequest:      r.at,
			DateResponse:     r.at,
			Done:             r.done,
			ChecksumRequest:  strings.Repeat("a", 32),
			ChecksumResponse: strings.Repeat("b", 32),
			Host:             "h",
			IP:               "127.0.0.1",
			NodeID:           nodeID,
			Attempts:         1,
			AttemptsDetails:  "[]",
		})
	}
	require.NoError(t, writer.Flush(ctx))
	waitRows(t, ctx, conn, table, uint64(len(rows)))

	reader := webch.NewLogReader(provider, logger)
	q := port.LogQuery{
		Table:   table,
		NodeID:  nodeID,
		SinceMs: base.Add(-time.Second).UnixMilli(),
		UntilMs: base.Add(time.Minute).UnixMilli(),
	}

	// Страница в ОДНУ запись — так курсор обязательно пройдёт через оба прогона B.
	seen := map[string]int{}
	cursor := port.ReplayCursor{}
	for page := 0; page < len(rows)+3; page++ {
		items, next, err := reader.ReplayCandidates(ctx, q, cursor, 1)
		require.NoError(t, err)
		for _, c := range items {
			seen[c.ID]++
		}
		if next.IsZero() {
			break
		}
		require.NotEqual(t, cursor, next, "курсор обязан двигаться, иначе обход зациклится")
		cursor = next
	}

	require.Len(t, seen, 3, "каждая ЗАПИСЬ окна отдана, и только один раз")
	for id, n := range seen {
		require.Equal(t, 1, n, "запись %s отдана %d раз — повтор ушёл бы дублем", id, n)
	}
}

// TestClickHouse_ReplayCandidates_OrderAndBodyFacts — хронологический порядок и
// признаки тела, на которых стоит классификация §85.3.
func TestClickHouse_ReplayCandidates_OrderAndBodyFacts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	conn, cfg, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	const table = "nexus_default.test_replay_facts"
	createNodeLogTable(t, ctx, conn, table)

	logger := logging.NewNoop()
	provider := clickhouse.StaticProvider(conn)
	writer := chlog.New(provider, cfg, logger)
	defer writer.Stop(ctx)

	base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	const nodeID = "22222222-2222-2222-2222-222222222222"

	truncated := strings.Repeat("x", 20) + domain.LogBodyTruncationMarker
	multipart := "multipart/form-data\n- part 1: name=\"f\"; filename=\"a.pdf\"; type=application/pdf; size=10\n[1 part, body 10 bytes]"

	specs := []struct {
		id      string
		at      time.Time
		typ     domain.RootMethod
		body    string
		full    int64
		params  string
		wantWhy domain.ReplaySkipReason
	}{
		{"11111111-0000-0000-0000-000000000001", base, domain.RootMethodRequestAsync, `{"ok":1}`, 8, "", domain.ReplaySkipNone},
		{"22222222-0000-0000-0000-000000000002", base.Add(time.Second), domain.RootMethodRequest, `{"ok":1}`, 8, "", domain.ReplaySkipNotAsync},
		{"33333333-0000-0000-0000-000000000003", base.Add(2 * time.Second), domain.RootMethodRequestAsync, truncated, 9999, "", domain.ReplaySkipTruncated},
		{"44444444-0000-0000-0000-000000000004", base.Add(3 * time.Second), domain.RootMethodRequestAsync, multipart, 4096, "", domain.ReplaySkipMultipart},
		{"55555555-0000-0000-0000-000000000005", base.Add(4 * time.Second), domain.RootMethodRequestAsync, "", 0, "", domain.ReplaySkipBodyMissing},
		{"66666666-0000-0000-0000-000000000006", base.Add(5 * time.Second), domain.RootMethodRequestAsync, `{"ok":1}`, 8, domain.ReplayOfParam + "=old-id", domain.ReplaySkipReplayCopy},
	}
	for _, s := range specs {
		writer.Write(ctx, table, &domain.LogRecord{
			ID:               s.id,
			Type:             s.typ,
			HTTPMethod:       "POST",
			URL:              "https://example.com/u",
			Parameters:       s.params,
			Request:          s.body,
			RequestSize:      s.full,
			Status:           200,
			DateCreate:       s.at,
			DateRequest:      s.at,
			DateResponse:     s.at,
			Done:             true,
			ChecksumRequest:  strings.Repeat("a", 32),
			ChecksumResponse: strings.Repeat("b", 32),
			Host:             "h",
			IP:               "127.0.0.1",
			NodeID:           nodeID,
			Attempts:         1,
			AttemptsDetails:  "[]",
		})
	}
	require.NoError(t, writer.Flush(ctx))
	waitRows(t, ctx, conn, table, uint64(len(specs)))

	reader := webch.NewLogReader(provider, logger)
	items, _, err := reader.ReplayCandidates(ctx, port.LogQuery{
		Table:   table,
		NodeID:  nodeID,
		SinceMs: base.Add(-time.Second).UnixMilli(),
		UntilMs: base.Add(time.Minute).UnixMilli(),
	}, port.ReplayCursor{}, 100)
	require.NoError(t, err)
	require.Len(t, items, len(specs))

	for i, s := range specs {
		got := items[i]
		require.Equal(t, s.id, got.ID, "порядок обязан быть хронологическим (позиция %d)", i)
		require.Equal(t, s.wantWhy,
			domain.ClassifyReplayCandidate(got, domain.ReplayEffectiveMethod("POST", got.HTTPMethod), true),
			"запись %s классифицирована неверно", s.id)
	}
}
