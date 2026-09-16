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

// §102: рейтинг «Запросы по методам» считается ТЕМИ ЖЕ выражениями
// уникальности, что и NodeKPI, и тем же WHERE, что список логов. Тест сторожит
// обе тождественности: разойдясь, они дали бы строку «Остальные» с плавающим
// знаком (клиент вычитает сумму методов из kpi.total).

func TestMethodStatsSQL_UsesSameUniqueExprsAsKPI(t *testing.T) {
	t.Parallel()

	for _, approx := range []bool{false, true} {
		wantTotal, wantDelivered := uniqueExprs(approx)
		sql := methodStatsSQL("db.t", "", approx, 9)

		assert.Contains(t, sql, wantTotal+" AS total",
			"total обязан считаться тем же выражением, что в NodeKPI (approx=%v)", approx)
		assert.Contains(t, sql, wantDelivered+" AS delivered",
			"delivered обязан считаться тем же выражением, что в NodeKPI (approx=%v)", approx)
	}
}

func TestMethodStatsSQL_GroupOrderLimit(t *testing.T) {
	t.Parallel()

	sql := methodStatsSQL("db.t", " WHERE node_id = ?", false, 21)

	assert.Contains(t, sql, "FROM db.t WHERE node_id = ?", "WHERE вклеивается как есть, без второго набора условий")
	assert.Contains(t, sql, "GROUP BY method")
	// Второй ключ сортировки — детерминизм: без него строки с равными
	// счётчиками переставлялись бы на каждом поллинге вкладки.
	assert.Contains(t, sql, "ORDER BY total DESC, method ASC")
	assert.Contains(t, sql, "LIMIT 21")

	// Пустое значение method — законная группа: без неё Σ по методам перестала
	// бы сходиться с NodeKPI.Total (в отличие от фасета DistinctMethods, где
	// `method != ''` стоит намеренно).
	assert.NotContains(t, sql, "method != ''")
}

func TestMethodStatsLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   int
		want int
	}{
		{name: "запрошенное в пределах капа проходит как есть", in: 20, want: 20},
		{name: "ноль — кап", in: 0, want: maxMethodStats},
		{name: "отрицательное — кап", in: -3, want: maxMethodStats},
		{name: "запредельное срезается до капа", in: maxMethodStats + 1, want: maxMethodStats},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, methodStatsLimit(tt.in))
		})
	}
}

// TestNodeMethodStats_RejectsUnsafeTable — имя таблицы приходит из конфигурации
// узла и вклеивается в SQL текстом (параметризовать имя нечем), поэтому
// проверка обязана стоять ДО обращения к соединению.
func TestNodeMethodStats_RejectsUnsafeTable(t *testing.T) {
	t.Parallel()

	r := NewLogReader(nil, logging.NewNoop())
	_, _, err := r.NodeMethodStats(context.Background(), port.LogQuery{Table: "db.t; DROP TABLE x"}, false, 8)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid table name")
}

// TestNodeMethodStats_SharesSearchCondsWithLogs — фильтры рейтинга это фильтры
// журнала (§79.4). Сверяется не текст запроса, а сами условия: они строятся
// общим searchConds, и тест фиксирует, что рейтинг не заводит своих.
func TestNodeMethodStats_SharesSearchCondsWithLogs(t *testing.T) {
	t.Parallel()

	r := NewLogReader(nil, logging.NewNoop())
	q := port.LogQuery{
		Table:      "db.t",
		Method:     "v1/GetParcelsInfo",
		ClientHost: "host.example",
		Status:     "err",
		SinceMs:    1_000,
		UntilMs:    2_000,
	}
	conds, args := r.searchConds(context.Background(), q)
	where := " WHERE " + strings.Join(conds, " AND ")
	sql := methodStatsSQL(q.Table, where, false, 9)

	assert.Contains(t, sql, "method = ?")
	assert.Contains(t, sql, "client_host = ?")
	// Порядок — тот, в котором searchConds добавляет условия (client_host
	// раньше method), и он обязан совпадать с порядком позиционных аргументов.
	assert.Equal(t, []any{"host.example", "v1/GetParcelsInfo"}, args[len(args)-2:])
}
