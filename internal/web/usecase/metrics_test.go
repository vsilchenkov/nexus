package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// --- fakes -----------------------------------------------------------------

type fakeProm struct {
	totals       port.GlobalTotals
	totalsErr    error
	queue        float64
	queueErr     error
	throughput   map[string]port.NodeThroughput
	thrErr       error
	nodeErrs     map[string]float64
	nodeErrErr   error
	lastErrs     map[string]float64
	lastErrsErr  error
	series       map[string][]float64
	seriesErr    error
	nodeKPI      port.NodeKPI
	nodeKPIErr   error
	nodeChart    []port.SeriesPoint
	nodeChartErr error
	kafkaOv      port.KafkaSummary
	kafkaOvErr   error
	kafkaSeries  map[string][]port.KafkaPoint
	kafkaSerErr  error
}

func (f *fakeProm) GlobalTotals(_ context.Context, _ time.Duration) (port.GlobalTotals, error) {
	return f.totals, f.totalsErr
}
func (f *fakeProm) KafkaQueue(_ context.Context) (float64, error) { return f.queue, f.queueErr }
func (f *fakeProm) NodeThroughput(_ context.Context, _, _ time.Time) (map[string]port.NodeThroughput, error) {
	return f.throughput, f.thrErr
}
func (f *fakeProm) NodeErrors(_ context.Context, _ time.Duration) (map[string]float64, error) {
	return f.nodeErrs, f.nodeErrErr
}
func (f *fakeProm) NodeLastErrors(_ context.Context, _ time.Time) (map[string]float64, error) {
	return f.lastErrs, f.lastErrsErr
}
func (f *fakeProm) NodeSeries(_ context.Context, _, _ time.Time, _ int) (map[string][]float64, error) {
	return f.series, f.seriesErr
}
func (f *fakeProm) NodeKPI(_ context.Context, _ string, _, _ time.Time) (port.NodeKPI, error) {
	return f.nodeKPI, f.nodeKPIErr
}
func (f *fakeProm) NodeChart(_ context.Context, _ string, _, _ time.Time, _ int) ([]port.SeriesPoint, error) {
	return f.nodeChart, f.nodeChartErr
}
func (f *fakeProm) KafkaOverview(_ context.Context, _, _ time.Time) (port.KafkaSummary, error) {
	return f.kafkaOv, f.kafkaOvErr
}
func (f *fakeProm) KafkaTimeseries(_ context.Context, _, _ time.Time, _ time.Duration, _ []string) (map[string][]port.KafkaPoint, error) {
	return f.kafkaSeries, f.kafkaSerErr
}

// fakeNodeLogs — ClickHouse-источник per-node метрик (port.NodeLogMetrics).
type fakeNodeLogs struct {
	kpi      port.NodeKPI
	kpiErr   error
	chart    []port.SeriesPoint
	chartErr error
}

func (f *fakeNodeLogs) NodeKPI(_ context.Context, _, _ string, _, _ int64) (port.NodeKPI, error) {
	return f.kpi, f.kpiErr
}
func (f *fakeNodeLogs) NodeChart(_ context.Context, _, _ string, _, _ int64, _ int) ([]port.SeriesPoint, error) {
	return f.chart, f.chartErr
}

// fakeNodeRepo встраивает port.NodeRepo (nil): usecase зовёт Get и List.
type fakeNodeRepo struct {
	port.NodeRepo
	node *domain.Node
	list []*domain.Node
	err  error
}

func (f *fakeNodeRepo) Get(_ context.Context, _ string) (*domain.Node, error) {
	return f.node, f.err
}
func (f *fakeNodeRepo) List(_ context.Context, _ port.ListNodesFilter) ([]*domain.Node, error) {
	return f.list, f.err
}

// --- Overview --------------------------------------------------------------

// §43.A: Overview отдаёт только очередь Kafka + флаг доступности Prometheus.
// Трафик (входящие/исходящие/ошибки) переехал в NodesOverview.Totals.
func TestMetricsUsecase_Overview(t *testing.T) {
	t.Parallel()
	log := logging.NewNoop()

	t.Run("no prometheus → degraded", func(t *testing.T) {
		t.Parallel()
		uc := NewMetricsUsecase(nil, nil, &fakeNodeRepo{}, log)
		got := uc.Overview(context.Background())
		require.False(t, got.PrometheusAvailable)
		require.Zero(t, got.KafkaQueue)
	})

	t.Run("happy path → kafka queue + available", func(t *testing.T) {
		t.Parallel()
		prom := &fakeProm{queue: 312}
		uc := NewMetricsUsecase(prom, nil, &fakeNodeRepo{}, log)
		got := uc.Overview(context.Background())
		require.True(t, got.PrometheusAvailable)
		require.EqualValues(t, 312, got.KafkaQueue)
		// Трафик в шапке больше не считается здесь (§43.A).
		require.Zero(t, got.Incoming24h)
		require.Zero(t, got.Outgoing24h)
		require.Zero(t, got.Errors24h)
	})

	t.Run("kafka queue error degrades, not panics", func(t *testing.T) {
		t.Parallel()
		prom := &fakeProm{queueErr: errors.New("boom")}
		uc := NewMetricsUsecase(prom, nil, &fakeNodeRepo{}, log)
		got := uc.Overview(context.Background())
		require.False(t, got.PrometheusAvailable)
	})
}

// --- NodesOverview ---------------------------------------------------------

func TestMetricsUsecase_NodesOverview(t *testing.T) {
	t.Parallel()
	log := logging.NewNoop()

	t.Run("no prometheus → empty degraded", func(t *testing.T) {
		t.Parallel()
		uc := NewMetricsUsecase(nil, nil, &fakeNodeRepo{}, log)
		got := uc.NodesOverview(context.Background(), "", time.Now().Add(-time.Hour), time.Now())
		require.False(t, got.PrometheusAvailable)
		require.Empty(t, got.Items)
	})

	t.Run("maps rows by node", func(t *testing.T) {
		t.Parallel()
		prom := &fakeProm{
			throughput: map[string]port.NodeThroughput{
				"webhook/send": {In: 4201, Out: 4198, Errors: 2},
			},
			// §41: последний вызов узла — ошибка → LastError=true.
			lastErrs: map[string]float64{"webhook/send": 1},
		}
		uc := NewMetricsUsecase(prom, nil, &fakeNodeRepo{}, log)
		got := uc.NodesOverview(context.Background(), "", time.Now().Add(-time.Hour), time.Now())
		require.True(t, got.PrometheusAvailable)
		require.Len(t, got.Items, 1)
		require.Equal(t, "webhook/send", got.Items[0].Node)
		require.EqualValues(t, 4201, got.Items[0].In)
		require.EqualValues(t, 4198, got.Items[0].Out)
		require.EqualValues(t, 2, got.Items[0].Errors)
		require.True(t, got.Items[0].LastError, "§41: последний вызов — ошибка → Down")
		// §43.A: шапка = сумма строк (Prometheus-ветка).
		require.EqualValues(t, 4201, got.Totals.Incoming)
		require.EqualValues(t, 4198, got.Totals.Outgoing)
		require.EqualValues(t, 2, got.Totals.Errors)
		require.InDelta(t, 2.0/4201.0, got.Totals.ErrorRate, 1e-9)
	})

	t.Run("prom error degrades", func(t *testing.T) {
		t.Parallel()
		prom := &fakeProm{thrErr: errors.New("boom")}
		uc := NewMetricsUsecase(prom, nil, &fakeNodeRepo{}, log)
		got := uc.NodesOverview(context.Background(), "", time.Now().Add(-time.Hour), time.Now())
		require.False(t, got.PrometheusAvailable)
		require.Empty(t, got.Items)
	})

	t.Run("clickhouse source matches node detail (per-node KPI)", func(t *testing.T) {
		t.Parallel()
		// nodeLogs != nil → берём из CH (как страница узла), не из Prometheus.
		repo := &fakeNodeRepo{list: []*domain.Node{
			{Path: "a/x", ClickHouseTable: "db.a"},
			{Path: "b/y", ClickHouseTable: ""}, // нет логирования → нули
		}}
		logs := &fakeNodeLogs{
			kpi:   port.NodeKPI{Total: 50, Delivered: 47, Errors: 3, P95ms: 12},
			chart: []port.SeriesPoint{{Count: 5}, {Count: 7}},
		}
		uc := NewMetricsUsecase(nil, logs, repo, log) // prom=nil — путь именно CH
		got := uc.NodesOverview(context.Background(), "default", time.Now().Add(-time.Hour), time.Now())
		require.True(t, got.PrometheusAvailable)
		require.Len(t, got.Items, 2)
		byNode := map[string]NodeThroughputRow{}
		for _, it := range got.Items {
			byNode[it.Node] = it
		}
		require.EqualValues(t, 50, byNode["a/x"].In)
		require.EqualValues(t, 47, byNode["a/x"].Out)
		require.EqualValues(t, 3, byNode["a/x"].Errors)
		require.EqualValues(t, 12, byNode["a/x"].P95ms)
		require.Equal(t, []float64{5, 7}, byNode["a/x"].Spark)
		require.Zero(t, byNode["b/y"].In, "узел без CH-таблицы → нули")
		// §43.A: шапка = сумма строк (CH-ветка) = только узел a/x (b/y нулевой).
		require.EqualValues(t, 50, got.Totals.Incoming)
		require.EqualValues(t, 47, got.Totals.Outgoing)
		require.EqualValues(t, 3, got.Totals.Errors)
		require.InDelta(t, 3.0/50.0, got.Totals.ErrorRate, 1e-9)
	})

	t.Run("totals: пустой список → нулевой агрегат без паники", func(t *testing.T) {
		t.Parallel()
		uc := NewMetricsUsecase(nil, &fakeNodeLogs{}, &fakeNodeRepo{list: nil}, log)
		got := uc.NodesOverview(context.Background(), "default", time.Now().Add(-time.Hour), time.Now())
		require.Zero(t, got.Totals.Incoming)
		require.Zero(t, got.Totals.ErrorRate)
	})
}

// --- NodeMetrics -----------------------------------------------------------

func TestMetricsUsecase_NodeMetrics(t *testing.T) {
	t.Parallel()
	log := logging.NewNoop()

	t.Run("node not found propagates", func(t *testing.T) {
		t.Parallel()
		repo := &fakeNodeRepo{err: domain.ErrNodeNotFound}
		uc := NewMetricsUsecase(nil, nil, repo, log)
		_, err := uc.NodeMetrics(context.Background(), "x", "default", time.Now().Add(-time.Hour), time.Now(), 10)
		require.ErrorIs(t, err, domain.ErrNodeNotFound)
	})

	t.Run("team mismatch → not found", func(t *testing.T) {
		t.Parallel()
		repo := &fakeNodeRepo{node: &domain.Node{ID: "n1", Path: "p", TeamID: "other"}}
		uc := NewMetricsUsecase(nil, nil, repo, log)
		_, err := uc.NodeMetrics(context.Background(), "n1", "default", time.Now().Add(-time.Hour), time.Now(), 10)
		require.ErrorIs(t, err, domain.ErrNodeNotFound)
	})

	t.Run("no clickhouse table → chart unavailable", func(t *testing.T) {
		t.Parallel()
		// Узел без ClickHouseTable (логирование выключено) → метрики недоступны.
		repo := &fakeNodeRepo{node: &domain.Node{ID: "n1", Path: "p", TeamID: "default"}}
		uc := NewMetricsUsecase(nil, &fakeNodeLogs{}, repo, log)
		got, err := uc.NodeMetrics(context.Background(), "n1", "default", time.Now().Add(-time.Hour), time.Now(), 10)
		require.NoError(t, err)
		require.False(t, got.ChartAvailable)
		require.Zero(t, got.KPI.Total)
	})

	t.Run("clickhouse error → degrade, not 500", func(t *testing.T) {
		t.Parallel()
		repo := &fakeNodeRepo{node: &domain.Node{ID: "n1", Path: "p", TeamID: "default", ClickHouseTable: "db.t"}}
		logs := &fakeNodeLogs{kpiErr: errors.New("boom")}
		uc := NewMetricsUsecase(nil, logs, repo, log)
		got, err := uc.NodeMetrics(context.Background(), "n1", "default", time.Now().Add(-time.Hour), time.Now(), 10)
		require.NoError(t, err)
		require.False(t, got.ChartAvailable)
	})

	t.Run("happy path returns kpi+series from clickhouse", func(t *testing.T) {
		t.Parallel()
		repo := &fakeNodeRepo{node: &domain.Node{ID: "n1", Path: "webhook/send", TeamID: "default", ClickHouseTable: "db.webhook_send"}}
		logs := &fakeNodeLogs{
			kpi:   port.NodeKPI{Total: 100, Delivered: 98, Errors: 2, P95ms: 87, P99ms: 142},
			chart: []port.SeriesPoint{{TsMs: 1, Count: 10}, {TsMs: 2, Count: 20}},
		}
		uc := NewMetricsUsecase(nil, logs, repo, log)
		got, err := uc.NodeMetrics(context.Background(), "n1", "default", time.Now().Add(-time.Hour), time.Now(), 10)
		require.NoError(t, err)
		require.True(t, got.ChartAvailable)
		require.EqualValues(t, 100, got.KPI.Total)
		require.EqualValues(t, 142, got.KPI.P99ms)
		require.Len(t, got.Series, 2)
		require.EqualValues(t, time.Hour.Milliseconds(), got.RangeMs)
	})

	t.Run("empty teamID skips scope check", func(t *testing.T) {
		t.Parallel()
		repo := &fakeNodeRepo{node: &domain.Node{ID: "n1", Path: "p", TeamID: "whatever"}}
		uc := NewMetricsUsecase(nil, nil, repo, log)
		_, err := uc.NodeMetrics(context.Background(), "n1", "", time.Now().Add(-time.Hour), time.Now(), 10)
		require.NoError(t, err)
	})
}
