package usecase

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/clock"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// scopeMetricsRepo — репозиторий узлов, запоминающий фильтр и число вызовов.
// По одним лишь строкам ответа «скоуп применён» и «скоуп потерян» неразличимы.
type scopeMetricsRepo struct {
	port.NodeRepo
	list       []*domain.Node
	lastFilter port.ListNodesFilter
	calls      atomic.Int32
	mu         sync.Mutex
}

func (r *scopeMetricsRepo) List(_ context.Context, f port.ListNodesFilter) ([]*domain.Node, error) {
	r.mu.Lock()
	r.lastFilter = f
	r.mu.Unlock()
	r.calls.Add(1)
	return r.list, nil
}

func (r *scopeMetricsRepo) filter() port.ListNodesFilter {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastFilter
}

func metricsWindow() (time.Time, time.Time) {
	until := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	return until.Add(-time.Hour), until
}

// TestMetrics_NodesOverviewScoped_AllTeams (§86.4): сквозной скоуп задаётся
// членствами, команда сессии в фильтр не подмешивается.
func TestMetrics_NodesOverviewScoped_AllTeams(t *testing.T) {
	t.Parallel()
	repo := &scopeMetricsRepo{list: []*domain.Node{{ID: "n1", Path: "a/x"}}}
	uc := NewMetricsUsecase(&fakeProm{}, nil, repo, nil, nil, logging.NewNoop(),
		WithMetricsTeams(newScopeTeams()))
	since, until := metricsWindow()

	uc.NodesOverviewScoped(context.Background(),
		NodesScope{TeamID: "session-team", UserID: "u1"}, since, until)

	f := repo.filter()
	assert.ElementsMatch(t, []string{"team1", "team2"}, f.TeamIDs)
	assert.Empty(t, f.TeamID, "команда сессии не участвует в сквозном скоупе")
}

// TestMetrics_NodesOverviewScoped_NodeIDs (§86.4): порционный запрос сужает
// выборку, НЕ подменяя team-скоуп.
func TestMetrics_NodesOverviewScoped_NodeIDs(t *testing.T) {
	t.Parallel()
	repo := &scopeMetricsRepo{}
	uc := NewMetricsUsecase(&fakeProm{}, nil, repo, nil, nil, logging.NewNoop(),
		WithMetricsTeams(newScopeTeams()))
	since, until := metricsWindow()

	uc.NodesOverviewScoped(context.Background(),
		NodesScope{TeamID: "session-team", NodeIDs: []string{"n1", "n2"}}, since, until)

	f := repo.filter()
	assert.Equal(t, []string{"n1", "n2"}, f.IDs)
	// Сужение обязано ехать ВМЕСТЕ с командой: иначе набор id стал бы способом
	// прочитать узел чужой команды.
	assert.Equal(t, "session-team", f.TeamID)
}

// TestMetrics_NodesOverviewScoped_NoMemberships (§86.9): без членств расчёт не
// запускается вовсе.
func TestMetrics_NodesOverviewScoped_NoMemberships(t *testing.T) {
	t.Parallel()
	repo := &scopeMetricsRepo{}
	uc := NewMetricsUsecase(&fakeProm{}, nil, repo, nil, nil, logging.NewNoop(),
		WithMetricsTeams(&scopeTeams{}))
	since, until := metricsWindow()

	got := uc.NodesOverviewScoped(context.Background(), NodesScope{UserID: "u1"}, since, until)

	assert.Empty(t, got.Items)
	// Пустой TeamIDs в адаптере означает «фильтра нет» — то есть узлы ВСЕХ
	// команд инстанса. Запрос не должен уйти.
	assert.Zero(t, repo.calls.Load())
}

// TestMetrics_TotalsScoped_IgnoresNodeIDs (§86.4): агрегат шапки считается по
// всему скоупу — иначе его значение зависело бы от прокрутки таблицы.
func TestMetrics_TotalsScoped_IgnoresNodeIDs(t *testing.T) {
	t.Parallel()
	repo := &scopeMetricsRepo{}
	uc := NewMetricsUsecase(&fakeProm{}, nil, repo, nil, nil, logging.NewNoop(),
		WithMetricsTeams(newScopeTeams()))
	since, until := metricsWindow()

	uc.OverviewTotalsScoped(context.Background(),
		NodesScope{TeamID: "session-team", NodeIDs: []string{"n1"}}, since, until)

	assert.Empty(t, repo.filter().IDs, "сужение по узлам к агрегату не применяется")
}

// TestMetrics_TotalsScoped_Cache (§86.4): кеш снимает множитель автообновления,
// а по истечении TTL значение пересчитывается.
func TestMetrics_TotalsScoped_Cache(t *testing.T) {
	t.Parallel()
	repo := &scopeMetricsRepo{list: []*domain.Node{{ID: "n1", Path: "a/x"}}}
	fake := clock.NewFake(time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC))
	uc := NewMetricsUsecase(&fakeProm{}, nil, repo, nil, nil, logging.NewNoop(),
		WithMetricsTeams(newScopeTeams()),
		WithMetricsClock(fake),
		WithTotalsCacheTTL(15*time.Second))
	sc := NodesScope{UserID: "u1"}
	since, until := metricsWindow()

	uc.OverviewTotalsScoped(context.Background(), sc, since, until)
	first := repo.calls.Load()
	require.Positive(t, first)

	uc.OverviewTotalsScoped(context.Background(), sc, since, until)
	assert.Equal(t, first, repo.calls.Load(), "повтор в пределах TTL берётся из кеша")

	fake.Advance(20 * time.Second)
	uc.OverviewTotalsScoped(context.Background(), sc, since, until)
	assert.Greater(t, repo.calls.Load(), first, "после TTL агрегат пересчитывается")
}

// chNodes — узлы для CH-ветки: два с логированием и один без таблицы.
func chNodes() []*domain.Node {
	return []*domain.Node{
		{ID: "n1", Path: "a/x", ClickHouseTable: "nexus_a.logs"},
		{ID: "n2", Path: "b/y", ClickHouseTable: "nexus_b.logs"},
		{ID: "n3", Path: "c/z"}, // логирование выключено — CH-запросов не будет
	}
}

// TestMetrics_TotalsScoped_SkipsSparkline (§86.10): проход для шапки НЕ строит
// спарклайны.
//
// Тест на экономию, а не на форму ответа: спарклайн — второй CH-запрос на узел,
// и его результат на этом пути гарантированно уходит в мусор. Без проверки
// лишний проход вернулся бы незамеченным — сумма от него не меняется.
func TestMetrics_TotalsScoped_SkipsSparkline(t *testing.T) {
	t.Parallel()
	repo := &scopeMetricsRepo{list: chNodes()}
	logs := &fakeNodeLogs{kpi: port.NodeKPI{Total: 100, Delivered: 90}}
	uc := NewMetricsUsecase(&fakeProm{}, logs, repo, nil, nil, logging.NewNoop(),
		WithMetricsTeams(newScopeTeams()))
	since, until := metricsWindow()

	uc.OverviewTotalsScoped(context.Background(), NodesScope{UserID: "u1"}, since, until)

	logs.mu.Lock()
	kpiCalls, chartCalls := logs.kpiCalls, logs.chartCalls
	logs.mu.Unlock()
	assert.Equal(t, 2, kpiCalls, "KPI считается для каждого узла с таблицей")
	assert.Zero(t, chartCalls, "спарклайн на пути агрегата шапки не нужен")
}

// TestMetrics_NodesOverviewScoped_KeepsSparkline (§86.10): обычный обзор
// спарклайны строит по-прежнему — они рисуются в строках таблицы.
//
// Регресс-гейт на оптимизацию выше: если флаг «без графика» протечёт на общий
// путь, спарклайны молча исчезнут из таблицы рабочего стола.
func TestMetrics_NodesOverviewScoped_KeepsSparkline(t *testing.T) {
	t.Parallel()
	repo := &scopeMetricsRepo{list: chNodes()}
	logs := &fakeNodeLogs{kpi: port.NodeKPI{Total: 100, Delivered: 90}}
	uc := NewMetricsUsecase(&fakeProm{}, logs, repo, nil, nil, logging.NewNoop(),
		WithMetricsTeams(newScopeTeams()))
	since, until := metricsWindow()

	uc.NodesOverviewScoped(context.Background(), NodesScope{UserID: "u1"}, since, until)

	logs.mu.Lock()
	chartCalls := logs.chartCalls
	logs.mu.Unlock()
	assert.Equal(t, 2, chartCalls, "в таблице спарклайн нужен на каждом узле с трафиком")
}

// TestMetrics_TotalsScoped_RankSlice (§86.10): срез приходит на ВСЕ узлы скоупа.
//
// Узел без ClickHouse-таблицы обязан быть в срезе с нулями: иначе он выпал бы из
// карты рангов и уехал в конец списка «как неизвестный», хотя про него всё
// известно — у него просто нет логирования.
func TestMetrics_TotalsScoped_RankSlice(t *testing.T) {
	t.Parallel()
	repo := &scopeMetricsRepo{list: chNodes()}
	logs := &fakeNodeLogs{kpi: port.NodeKPI{Total: 65, Delivered: 54, Errors: 11}}
	uc := NewMetricsUsecase(&fakeProm{}, logs, repo, nil, nil, logging.NewNoop(),
		WithMetricsTeams(newScopeTeams()))
	since, until := metricsWindow()

	got := uc.OverviewTotalsScoped(context.Background(), NodesScope{UserID: "u1"}, since, until)

	ids := make([]string, 0, len(got.Nodes))
	byID := make(map[string]NodeRankRow, len(got.Nodes))
	for _, n := range got.Nodes {
		ids = append(ids, n.NodeID)
		byID[n.NodeID] = n
	}
	assert.ElementsMatch(t, []string{"n1", "n2", "n3"}, ids)
	assert.Equal(t, uint64(65), byID["n1"].In)
	assert.Equal(t, uint64(54), byID["n1"].Out)
	assert.Equal(t, uint64(11), byID["n1"].Errors)
	assert.Zero(t, byID["n3"].In, "узел без логирования — нули, но в срезе")
	// Сумма шапки и срез считаются одним проходом и обязаны сходиться.
	assert.Equal(t, uint64(130), got.Totals.Incoming)
}

// TestMetrics_TotalsScoped_CacheKeepsSlice (§86.10): из кеша приезжает не только
// сумма, но и срез.
//
// Иначе повторный запрос в пределах TTL отдал бы шапку с пустым nodes, и клиент
// на каждом втором автообновлении терял бы порядок и статусы.
func TestMetrics_TotalsScoped_CacheKeepsSlice(t *testing.T) {
	t.Parallel()
	repo := &scopeMetricsRepo{list: chNodes()}
	logs := &fakeNodeLogs{kpi: port.NodeKPI{Total: 10}}
	fake := clock.NewFake(time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC))
	uc := NewMetricsUsecase(&fakeProm{}, logs, repo, nil, nil, logging.NewNoop(),
		WithMetricsTeams(newScopeTeams()),
		WithMetricsClock(fake),
		WithTotalsCacheTTL(15*time.Second))
	sc := NodesScope{UserID: "u1"}
	since, until := metricsWindow()

	first := uc.OverviewTotalsScoped(context.Background(), sc, since, until)
	require.Len(t, first.Nodes, 3)

	second := uc.OverviewTotalsScoped(context.Background(), sc, since, until)
	assert.Equal(t, int32(1), repo.calls.Load(), "второй запрос взят из кеша")
	assert.Equal(t, first.Nodes, second.Nodes, "срез переживает попадание в кеш")
}

// TestMetrics_TotalsCacheKey_QuantizesWindow (§86.4): границы скользящего окна
// квантуются по TTL.
//
// Без квантования правая граница («сейчас») делала бы ключ уникальным на каждый
// запрос, и кеш не срабатывал бы НИКОГДА — при зелёном тесте на попадание в
// кеш с фиксированным окном.
func TestMetrics_TotalsCacheKey_QuantizesWindow(t *testing.T) {
	t.Parallel()
	ttl := 15 * time.Second
	base := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	ids := []string{"team1", "team2"}

	k1 := totalsCacheKey(ids, base.Add(-time.Hour), base, false, ttl)
	k2 := totalsCacheKey(ids, base.Add(-time.Hour).Add(3*time.Second), base.Add(3*time.Second), false, ttl)
	assert.Equal(t, k1, k2, "сдвиг внутри окна TTL не меняет ключ")

	k3 := totalsCacheKey(ids, base.Add(-time.Hour).Add(ttl), base.Add(ttl), false, ttl)
	assert.NotEqual(t, k1, k3, "переход в следующее окно TTL меняет ключ")

	assert.NotEqual(t, k1, totalsCacheKey(ids, base.Add(-time.Hour), base, true, ttl),
		"режим подсчёта уникальных входит в ключ")
	assert.NotEqual(t, k1, totalsCacheKey([]string{"team1"}, base.Add(-time.Hour), base, false, ttl),
		"набор команд входит в ключ")
}

// TestMetrics_TotalsScoped_SingleFlight (§86.4): одновременные промахи
// схлопываются в один проход.
func TestMetrics_TotalsScoped_SingleFlight(t *testing.T) {
	t.Parallel()
	repo := &scopeMetricsRepo{list: []*domain.Node{{ID: "n1", Path: "a/x"}}}
	uc := NewMetricsUsecase(&fakeProm{}, nil, repo, nil, nil, logging.NewNoop(),
		WithMetricsTeams(newScopeTeams()),
		WithTotalsCacheTTL(time.Minute))
	sc := NodesScope{UserID: "u1"}
	since, until := metricsWindow()

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			uc.OverviewTotalsScoped(context.Background(), sc, since, until)
		})
	}
	wg.Wait()

	// Восемь одновременных зрителей рабочего стола не должны давать восемь
	// одинаковых проходов по ClickHouse.
	assert.LessOrEqual(t, repo.calls.Load(), int32(2))
}
