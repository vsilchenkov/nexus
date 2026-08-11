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
