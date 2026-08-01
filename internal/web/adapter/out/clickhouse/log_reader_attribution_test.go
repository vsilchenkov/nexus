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
	counts    map[string]int
	externals map[string]struct{}
	err       error
	extErr    error
	calls     atomic.Int32
	extCalls  atomic.Int32
	delay     time.Duration
	release   chan struct{}
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

func (u *countingUsage) ExternalCHTables(ctx context.Context) (map[string]struct{}, error) {
	u.extCalls.Add(1)
	if u.delay > 0 {
		time.Sleep(u.delay)
	}
	if u.extErr != nil {
		return nil, u.extErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return u.externals, nil
}

// ownershipStub — Ownership (§70.4). Для nodeFilter значим только OwnsTable;
// остальные методы существуют, чтобы стаб удовлетворял интерфейсу.
type ownershipStub struct {
	owns bool
	err  error
}

func (ownershipStub) Claim(context.Context, string) error               { return nil }
func (ownershipStub) AssertOwnsDatabase(context.Context, string) error  { return nil }
func (ownershipStub) AssertOwnsTable(context.Context, string) error     { return nil }
func (o ownershipStub) OwnsTable(context.Context, string) (bool, error) { return o.owns, o.err }

func newReaderWithUsage(u *countingUsage) *LogReaderCH {
	return newReader(u, nil)
}

// newReader — читатель с подключёнными (или нет) фактами о таблицах и гейтом
// владения. Гейт нужен кейсам §70.4/§64: без него ownsTable отвечает true и
// ветка «чужая таблица» не проверяется вовсе — именно поэтому регресс §70,
// спрятавший логи внешней таблицы, не был пойман unit-тестами.
func newReader(u *countingUsage, o Ownership) *LogReaderCH {
	r := NewLogReader(nil, logging.NewNoop())
	if u != nil {
		r.SetTableUsage(u)
	}
	if o != nil {
		r.SetOwnership(o)
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
	usage := &countingUsage{
		counts: map[string]int{
			"nexus_default.shared": 3,
			"nexus_default.solo":   1,
			"gate_logs.ext_solo":   1,
			"gate_logs.ext_shared": 2,
			"other_logs.foreign":   1,
		},
		externals: map[string]struct{}{
			"gate_logs.ext_solo":   {},
			"gate_logs.ext_shared": {},
		},
	}
	foreign := ownershipStub{owns: false}
	own := ownershipStub{owns: true}

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

		// §64: внешняя таблица чужая по определению (маркера владения у неё нет и
		// быть не может), а все её записи приходит с пустым node_id — строгий
		// фильтр спрятал бы единственное содержимое таблицы. Боевой инцидент:
		// 10 222 523 записи в gate_logs.PDT_PDTExchange не показывались вовсе.
		{name: "§64: внешняя чужая таблица — послабление", reader: newReader(usage, foreign), table: "gate_logs.ext_solo", nodeID: "n1", wantCond: lenient},
		// §70.4 остаётся в силе там, ради чего вводился.
		{name: "§70.4: чужая НЕ внешняя — строго", reader: newReader(usage, foreign), table: "other_logs.foreign", nodeID: "n1", wantCond: strict},
		// Общая побеждает внешность: на такой таблице логи узла есть и без
		// послабления, а чужие строки видеть нельзя.
		{name: "общая внешняя — строго", reader: newReader(usage, foreign), table: "gate_logs.ext_shared", nodeID: "n1", wantCond: strict},
		{name: "своя таблица с флагом внешней — послабление", reader: newReader(usage, own), table: "gate_logs.ext_solo", nodeID: "n1", wantCond: lenient},
		// Фактов из PostgreSQL нет → правило деградирует в поведение до §61/§70,
		// иначе сбой базы прячет логи узла.
		{name: "снимка нет + чужая таблица — послабление", reader: newReader(nil, foreign), table: "other_logs.foreign", nodeID: "n1", wantCond: lenient},
		// Отказ гейта трактуется как «чужая» (fail-closed) и на не внешней
		// таблице обязан оставаться строгим.
		{name: "ошибка OwnsTable на не внешней — строго", reader: newReader(usage, ownershipStub{err: errors.New("ch down")}), table: "other_logs.foreign", nodeID: "n1", wantCond: strict},
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
	assert.Equal(t, int32(1), usage.extCalls.Load(), "второй факт снимка тоже берётся один раз")
}

// TestNodeFilter_ExternalLookupFailureDoesNotStorm: снимок фактов собирается
// двумя запросами, и падение ВТОРОГО обязано вести себя как падение первого —
// снимок не применяется целиком (полуснимок прятал бы логи), повторы отсекаются
// паузой.
func TestNodeFilter_ExternalLookupFailureDoesNotStorm(t *testing.T) {
	t.Parallel()
	usage := &countingUsage{
		counts: map[string]int{"nexus_default.shared": 2},
		extErr: errors.New("pg is down"),
	}
	r := newReaderWithUsage(usage)

	for range 50 {
		cond, _ := r.nodeFilter(context.Background(), "nexus_default.shared", "n1")
		require.Equal(t, "(node_id = ? OR node_id = '')", cond,
			"счётчики загрузились, но признак внешности — нет: снимок неполон, правило остаётся мягким")
	}

	assert.Equal(t, int32(1), usage.calls.Load(), "после неудачи следующая попытка — не раньше паузы")
	assert.Equal(t, int32(1), usage.extCalls.Load())
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
	assert.Equal(t, "node_id = ?", cond, "снимок загружен несмотря на отменённый ctx вызывающего")
	assert.Equal(t, int32(1), usage.calls.Load())
	assert.Equal(t, int32(1), usage.extCalls.Load(), "второй запрос снимка тоже переживает отмену")
}
