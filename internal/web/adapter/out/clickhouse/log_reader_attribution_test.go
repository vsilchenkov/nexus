package clickhouse

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/logging"
)

// countingUsage — port.NodeTableUsage со счётчиком обращений: тесты проверяют
// не только результат, но и ЦЕНУ (сколько раз сходили в PostgreSQL).
type countingUsage struct {
	counts  map[string]int
	err     error
	calls   atomic.Int32
	delay   time.Duration
	release chan struct{}
}

func (u *countingUsage) CountByCHTable(context.Context, string, string) (int, error) {
	return 0, nil
}

func (u *countingUsage) CountsByCHTable(ctx context.Context) (map[string]int, error) {
	u.calls.Add(1)
	if u.release != nil {
		select {
		case <-u.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if u.delay > 0 {
		time.Sleep(u.delay)
	}
	if u.err != nil {
		return nil, u.err
	}
	return u.counts, nil
}

func newReaderWithUsage(u *countingUsage) *LogReaderCH {
	r := NewLogReader(nil, logging.NewNoop())
	if u != nil {
		r.SetTableUsage(u)
	}
	return r
}

// TestNodeFilter_Attribution: на ЛИЧНОЙ таблице записи без node_id
// засчитываются узлу (legacy-совместимость), на ОБЩЕЙ — нет (иначе узел видит
// чужие строки, а после переноса — из другой команды).
func TestNodeFilter_Attribution(t *testing.T) {
	t.Parallel()
	const (
		lenient = "(node_id = ? OR node_id = '')"
		strict  = "node_id = ?"
	)
	usage := &countingUsage{counts: map[string]int{
		"nexus_default.shared": 3,
		"nexus_default.solo":   1,
	}}

	tests := []struct {
		name     string
		reader   *LogReaderCH
		table    string
		nodeID   string
		wantCond string
	}{
		{name: "личная таблица — послабление", reader: newReaderWithUsage(usage), table: "nexus_default.solo", nodeID: "n1", wantCond: lenient},
		{name: "общая таблица — строго свой node_id", reader: newReaderWithUsage(usage), table: "nexus_default.shared", nodeID: "n1", wantCond: strict},
		{name: "таблица не известна — послабление", reader: newReaderWithUsage(usage), table: "nexus_default.unknown", nodeID: "n1", wantCond: lenient},
		{name: "порт не подключён — послабление", reader: newReaderWithUsage(nil), table: "nexus_default.shared", nodeID: "n1", wantCond: lenient},
		{name: "без узла — фильтра нет", reader: newReaderWithUsage(usage), table: "nexus_default.shared", nodeID: "", wantCond: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cond, args := tc.reader.nodeFilter(context.Background(), tc.table, tc.nodeID)
			assert.Equal(t, tc.wantCond, cond)
			if tc.wantCond == "" {
				assert.Empty(t, args)
			} else {
				assert.Equal(t, []any{tc.nodeID}, args)
			}
		})
	}
}

// TestNodeFilter_CacheHitCost: обычная работа не должна добавлять нагрузку —
// дашборд считает KPI по всем узлам разом, и все эти вызовы обязаны обойтись
// ОДНИМ запросом к PostgreSQL.
func TestNodeFilter_CacheHitCost(t *testing.T) {
	t.Parallel()
	usage := &countingUsage{counts: map[string]int{"nexus_default.shared": 2}}
	r := newReaderWithUsage(usage)

	var wg sync.WaitGroup
	for i := range 200 {
		wg.Go(func() {
			table := "nexus_default.shared"
			if i%2 == 0 {
				table = "nexus_default.solo"
			}
			r.nodeFilter(context.Background(), table, "n1")
		})
	}
	wg.Wait()

	assert.Equal(t, int32(1), usage.calls.Load(), "200 запросов логов → один запрос к PostgreSQL")
}

// TestNodeFilter_FailureDoesNotStorm: при лежащем PostgreSQL сбой не должен
// усиливаться трафиком UI — повторные попытки отсекаются паузой, а правило
// остаётся прежним (мягким), чтобы не спрятать логи.
func TestNodeFilter_FailureDoesNotStorm(t *testing.T) {
	t.Parallel()
	usage := &countingUsage{err: errors.New("pg is down")}
	r := newReaderWithUsage(usage)

	for range 50 {
		cond, _ := r.nodeFilter(context.Background(), "nexus_default.shared", "n1")
		require.Equal(t, "(node_id = ? OR node_id = '')", cond, "сбой не должен прятать записи узла")
	}

	assert.Equal(t, int32(1), usage.calls.Load(), "после неудачи следующая попытка — не раньше паузы")
}

// TestNodeFilter_SingleFlight: пока идёт обновление карты, остальные запросы
// не выстраиваются в очередь на мьютексе и не дублируют поход в PostgreSQL —
// они работают по прежней карте.
func TestNodeFilter_SingleFlight(t *testing.T) {
	t.Parallel()
	usage := &countingUsage{
		counts:  map[string]int{"nexus_default.shared": 2},
		release: make(chan struct{}),
	}
	r := newReaderWithUsage(usage)

	started := make(chan struct{})
	go func() {
		close(started)
		r.nodeFilter(context.Background(), "nexus_default.shared", "n1") // зависнет на release
	}()
	<-started
	require.Eventually(t, func() bool { return usage.calls.Load() == 1 }, time.Second, 5*time.Millisecond)

	// Пока первый висит в запросе, второй отвечает сразу и не идёт в PostgreSQL.
	done := make(chan string, 1)
	go func() {
		cond, _ := r.nodeFilter(context.Background(), "nexus_default.shared", "n2")
		done <- cond
	}()
	select {
	case cond := <-done:
		assert.Equal(t, "(node_id = ? OR node_id = '')", cond, "пока карты нет — прежнее правило")
	case <-time.After(time.Second):
		t.Fatal("вызов заблокировался на время запроса к PostgreSQL")
	}
	assert.Equal(t, int32(1), usage.calls.Load(), "второй вызов не дублирует запрос")

	close(usage.release)
}

// TestNodeFilter_SurvivesCallerCancel: пользователь ушёл со страницы (ctx
// отменён) — карта всё равно обновляется, иначе она не обновится никогда при
// коротких запросах UI.
func TestNodeFilter_SurvivesCallerCancel(t *testing.T) {
	t.Parallel()
	usage := &countingUsage{counts: map[string]int{"nexus_default.shared": 2}}
	r := newReaderWithUsage(usage)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cond, _ := r.nodeFilter(ctx, "nexus_default.shared", "n1")
	assert.Equal(t, "node_id = ?", cond, "карта загружена несмотря на отменённый ctx вызывающего")
	assert.Equal(t, int32(1), usage.calls.Load())
}
