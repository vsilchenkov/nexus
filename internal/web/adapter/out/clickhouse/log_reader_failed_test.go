package clickhouse

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/web/usecase/port"
)

// §79.1: единица «неудачной доставки» — ЗАПИСЬ без единого прогона done=1.
// Здесь проверяется то, что можно проверить без ClickHouse: разность множеств,
// защита счётчика от underflow и СТРОГИЙ node-фильтр в разрушающей операции.
// Поведение самих запросов — в tests/integration (Phase 79.3).

func TestDiffIDs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		candidates []string
		delivered  []string
		want       []string
	}{
		{name: "нет доставленных — набор не меняется", candidates: []string{"a", "b"}, want: []string{"a", "b"}},
		{name: "все доставлены — пусто", candidates: []string{"a", "b"}, delivered: []string{"b", "a"}, want: []string{}},
		{name: "частично", candidates: []string{"a", "b", "c"}, delivered: []string{"b"}, want: []string{"a", "c"}},
		{name: "порядок кандидатов сохраняется", candidates: []string{"c", "a", "b"}, delivered: []string{"a"}, want: []string{"c", "b"}},
		{name: "пустые кандидаты", candidates: nil, delivered: []string{"a"}, want: []string{}},
		{name: "доставленный не из набора игнорируется", candidates: []string{"a"}, delivered: []string{"z"}, want: []string{"a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, diffIDs(tt.candidates, tt.delivered))
		})
	}
}

// TestSubUnsigned — в приблизительном режиме (HLL) delivered считается
// независимо от total и может оказаться больше него. Голое total-delivered на
// uint64 показало бы пользователю 1.8e19 «неудачных доставок».
func TestSubUnsigned(t *testing.T) {
	t.Parallel()
	assert.Equal(t, uint64(3), subUnsigned(10, 7))
	assert.Equal(t, uint64(0), subUnsigned(10, 10))
	assert.Equal(t, uint64(0), subUnsigned(10, 11), "underflow обязан схлопнуться в 0")
}

// TestUniqueExprs — счётчики неудачных и NodeKPI обязаны считать одним и тем же
// выражением: разъехавшись, они дают разные ответы на один вопрос на одном
// экране (боевое расхождение 05.08: KPI 1 против 13 на вкладке «Очередь»).
func TestUniqueExprs(t *testing.T) {
	t.Parallel()
	total, delivered := uniqueExprs(false)
	assert.Equal(t, "countDistinct(ID)", total)
	assert.Equal(t, "uniqExactIf(ID, done = 1)", delivered)

	total, delivered = uniqueExprs(true)
	assert.Equal(t, "uniq(ID)", total)
	assert.Equal(t, "uniqIf(ID, done = 1)", delivered)
}

// TestDeleteFailedConds_StrictNodeFilter — в DML фильтр по узлу СТРОГИЙ даже
// там, где чтение послабляет его до `OR node_id = ”` (одиночная внешняя
// таблица §64). Послабление в DELETE означало бы удаление строк постороннего
// писателя.
func TestDeleteFailedConds_StrictNodeFilter(t *testing.T) {
	t.Parallel()
	usage := &countingUsage{counts: map[string]int{"db.t": 1}, externals: map[string]struct{}{"db.t": {}}}
	r := newReader(usage, nil)

	// Предпосылка кейса: на этой таблице ЧТЕНИЕ действительно послабляет фильтр.
	readCond, _ := r.nodeFilter(t.Context(), "db.t", "node-a")
	require.Equal(t, "(node_id = ? OR node_id = '')", readCond)

	conds, args := r.deleteFailedConds(port.LogQuery{Table: "db.t", NodeID: "node-a"}, []string{"id1"})
	where := strings.Join(conds, " AND ")
	assert.Contains(t, where, "node_id = ?")
	assert.NotContains(t, where, "node_id = ''", "разрушающая операция не имеет права трогать чужие строки")
	assert.Contains(t, where, "done = 0", "строки done=1 остаются: история успешной доставки не теряется")
	assert.Contains(t, where, "ID IN ?")
	assert.Equal(t, []any{"node-a", []string{"id1"}}, args)
}

// TestDeleteFailedConds_WindowPrunesPartitions — окно остаётся в условиях, хотя
// ID уже перечислены: без date_create-условий §72.4 DELETE сканирует таблицу
// целиком вместо одной партиции.
func TestDeleteFailedConds_WindowPrunesPartitions(t *testing.T) {
	t.Parallel()
	r := newReader(nil, nil)
	q := port.LogQuery{
		Table:             "db.t",
		NodeID:            "node-a",
		SinceMs:           1_700_000_000_000,
		UntilMs:           1_700_086_400_000,
		DateCreateAligned: true,
	}
	where := strings.Join(mustConds(r.deleteFailedConds(q, []string{"id1"})), " AND ")
	assert.Contains(t, where, "date_create >= toDate(?)")
	assert.Contains(t, where, "date_create <= toDate(?)")
	assert.Contains(t, where, "toUnixTimestamp64Milli(toDateTime64(date_request, 3)) > ?")
}

func mustConds(conds []string, _ []any) []string { return conds }
