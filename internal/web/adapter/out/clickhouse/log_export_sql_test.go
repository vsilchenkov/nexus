package clickhouse

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// TestExportRowsSQL — запрос батча строк выгрузки (§115.7): окно журнала
// сужено до ключей батча, голый предикат по date_request, тот же фильтр, что у
// списка, и порядок аргументов (проекция → WHERE).
func TestExportRowsSQL(t *testing.T) {
	t.Parallel()
	r := NewLogReader(nil, logging.NewNoop())
	q := port.LogQuery{
		Table: "db.logs", SinceMs: 1_000, UntilMs: 9_000_000_000_000,
		DateCreateAligned: true, Status: "err",
		// Курсор и Unresolved пришли бы с вкладки — в батче их быть не должно.
		BeforeID: "cursor", Unresolved: true, Limit: 100,
	}
	keys := []port.LogExportKey{
		{ID: "b", TsMs: 1_767_225_600_500, DateCreate: "2026-01-01"}, // 00:00:00.500 UTC
		{ID: "a", TsMs: 1_767_225_000_000, DateCreate: "2025-12-31"}, // 23:50:00 UTC
		{ID: "c", TsMs: 1_767_225_300_000, DateCreate: "2025-12-31"},
	}

	tests := []struct {
		name      string
		bodyRunes int
		wantCols  string
		wantHead  []any
	}{
		{"with bodies", 32767, "substringUTF8(request, 1, ?)", []any{32767, 32767}},
		{"without bodies", 0, "'' AS list_req", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sql, args := r.exportRowsSQL(context.Background(), q, keys, tt.bodyRunes)

			assert.Contains(t, sql, tt.wantCols)
			assert.Contains(t, sql, "FROM db.logs WHERE ")
			assert.True(t, strings.HasSuffix(sql, "ORDER BY date_request DESC, ID DESC LIMIT 1 BY ID"), sql)
			assert.Contains(t, sql,
				"ID IN ? AND date_create IN ? AND date_request BETWEEN toDateTime(?) AND toDateTime(?)")
			assert.Contains(t, sql, condLogErr)
			assert.NotContains(t, sql, "ID < ?", "keyset cursor must be dropped")
			assert.NotContains(t, sql, "done = 0", "unresolved must be dropped")

			require.GreaterOrEqual(t, len(args), len(tt.wantHead)+4)
			assert.Equal(t, tt.wantHead, nilIfEmpty(args[:len(tt.wantHead)]))
			tail := args[len(args)-4:]
			assert.Equal(t, []string{"b", "a", "c"}, tail[0])
			assert.Equal(t, []string{"2026-01-01", "2025-12-31"}, tail[1], "distinct days of the keys")
			assert.Equal(t, int64(1_767_225_000), tail[2], "min ts floored to seconds")
			assert.Equal(t, int64(1_767_225_601), tail[3], "max ts ceiled to seconds")
			// Окно журнала сужено до ключей батча: since = min−1, until = max.
			assert.Contains(t, args, int64(1_767_224_999_999))
			assert.Contains(t, args, int64(1_767_225_600_500))
			// …и окно по date_create (§72.4) — от него же (± день запаса), а не
			// от исходного q (тот дал бы 1969-12-31).
			assert.Contains(t, args, "2025-12-30")
			assert.Contains(t, args, "2026-01-02")
		})
	}
}

func nilIfEmpty(a []any) []any {
	if len(a) == 0 {
		return nil
	}
	return a
}

func TestFloorCeilDiv(t *testing.T) {
	t.Parallel()
	tests := []struct {
		a, wantFloor, wantCeil int64
	}{
		{0, 0, 0},
		{1000, 1, 1},
		{1500, 1, 2},
		{999, 0, 1},
		{-1, -1, 0},
		{-1500, -2, -1},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.wantFloor, floorDiv(tt.a, 1000), tt.a)
		assert.Equal(t, tt.wantCeil, ceilDiv(tt.a, 1000), tt.a)
	}
}
