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
	totals     port.GlobalTotals
	totalsErr  error
	queue      float64
	queueErr   error
	throughput map[string]port.NodeThroughput
	thrErr     error
	nodeErrs   map[string]float64
	nodeErrErr error
}

func (f *fakeProm) GlobalTotals(_ context.Context, _ time.Duration) (port.GlobalTotals, error) {
	return f.totals, f.totalsErr
}
func (f *fakeProm) KafkaQueue(_ context.Context) (float64, error) { return f.queue, f.queueErr }
func (f *fakeProm) NodeThroughput(_ context.Context, _ time.Duration) (map[string]port.NodeThroughput, error) {
	return f.throughput, f.thrErr
}
func (f *fakeProm) NodeErrors(_ context.Context, _ time.Duration) (map[string]float64, error) {
	return f.nodeErrs, f.nodeErrErr
}

type fakeCH struct {
	kpi       port.NodeKPI
	kpiErr    error
	series    []port.SeriesPoint
	seriesErr error
}

func (f *fakeCH) NodeKPI(_ context.Context, _ string, _, _ int64) (port.NodeKPI, error) {
	return f.kpi, f.kpiErr
}
func (f *fakeCH) NodeSeries(_ context.Context, _ string, _, _ int64, _ int) ([]port.SeriesPoint, error) {
	return f.series, f.seriesErr
}

// fakeNodeRepo встраивает port.NodeRepo (nil): usecase зовёт только Get.
type fakeNodeRepo struct {
	port.NodeRepo
	node *domain.Node
	err  error
}

func (f *fakeNodeRepo) Get(_ context.Context, _ string) (*domain.Node, error) {
	return f.node, f.err
}

// --- Overview --------------------------------------------------------------

func TestMetricsUsecase_Overview(t *testing.T) {
	t.Parallel()
	log := logging.NewNoop()

	t.Run("no prometheus → degraded", func(t *testing.T) {
		t.Parallel()
		uc := NewMetricsUsecase(nil, nil, &fakeNodeRepo{}, log)
		got := uc.Overview(context.Background())
		require.False(t, got.PrometheusAvailable)
		require.Zero(t, got.Incoming24h)
	})

	t.Run("happy path computes rate", func(t *testing.T) {
		t.Parallel()
		prom := &fakeProm{
			totals: port.GlobalTotals{Incoming: 1_240_000, Outgoing: 1_230_000, Errors: 1_845.6},
			queue:  312,
		}
		uc := NewMetricsUsecase(prom, nil, &fakeNodeRepo{}, log)
		got := uc.Overview(context.Background())
		require.True(t, got.PrometheusAvailable)
		require.EqualValues(t, 1_240_000, got.Incoming24h)
		require.EqualValues(t, 1_230_000, got.Outgoing24h)
		require.EqualValues(t, 312, got.KafkaQueue)
		require.EqualValues(t, 1846, got.Errors24h) // округление
		require.InDelta(t, 1845.6/1_230_000, got.ErrorRate, 1e-9)
	})

	t.Run("prom error degrades, not panics", func(t *testing.T) {
		t.Parallel()
		prom := &fakeProm{totalsErr: errors.New("boom")}
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
		got := uc.NodesOverview(context.Background(), time.Hour)
		require.False(t, got.PrometheusAvailable)
		require.Empty(t, got.Items)
	})

	t.Run("maps rows by node", func(t *testing.T) {
		t.Parallel()
		prom := &fakeProm{throughput: map[string]port.NodeThroughput{
			"webhook/send": {In: 4201, Out: 4198, Errors: 2},
		}}
		uc := NewMetricsUsecase(prom, nil, &fakeNodeRepo{}, log)
		got := uc.NodesOverview(context.Background(), time.Hour)
		require.True(t, got.PrometheusAvailable)
		require.Len(t, got.Items, 1)
		require.Equal(t, "webhook/send", got.Items[0].Node)
		require.EqualValues(t, 4201, got.Items[0].In)
		require.EqualValues(t, 4198, got.Items[0].Out)
		require.EqualValues(t, 2, got.Items[0].Errors)
	})

	t.Run("prom error degrades", func(t *testing.T) {
		t.Parallel()
		prom := &fakeProm{thrErr: errors.New("boom")}
		uc := NewMetricsUsecase(prom, nil, &fakeNodeRepo{}, log)
		got := uc.NodesOverview(context.Background(), time.Hour)
		require.False(t, got.PrometheusAvailable)
		require.Empty(t, got.Items)
	})
}

// --- NodeMetrics -----------------------------------------------------------

func TestMetricsUsecase_NodeMetrics(t *testing.T) {
	t.Parallel()
	log := logging.NewNoop()

	t.Run("node not found propagates", func(t *testing.T) {
		t.Parallel()
		repo := &fakeNodeRepo{err: domain.ErrNodeNotFound}
		uc := NewMetricsUsecase(nil, &fakeCH{}, repo, log)
		_, err := uc.NodeMetrics(context.Background(), "x", "default", time.Hour, 10)
		require.ErrorIs(t, err, domain.ErrNodeNotFound)
	})

	t.Run("team mismatch → not found", func(t *testing.T) {
		t.Parallel()
		repo := &fakeNodeRepo{node: &domain.Node{ID: "n1", TeamID: "other", ClickHouseTable: "db.t"}}
		uc := NewMetricsUsecase(nil, &fakeCH{}, repo, log)
		_, err := uc.NodeMetrics(context.Background(), "n1", "default", time.Hour, 10)
		require.ErrorIs(t, err, domain.ErrNodeNotFound)
	})

	t.Run("node without table → chart unavailable", func(t *testing.T) {
		t.Parallel()
		repo := &fakeNodeRepo{node: &domain.Node{ID: "n1", TeamID: "default", ClickHouseTable: ""}}
		uc := NewMetricsUsecase(nil, &fakeCH{}, repo, log)
		got, err := uc.NodeMetrics(context.Background(), "n1", "default", time.Hour, 10)
		require.NoError(t, err)
		require.False(t, got.ChartAvailable)
		require.Zero(t, got.KPI.Total)
	})

	t.Run("no CH provider → chart unavailable", func(t *testing.T) {
		t.Parallel()
		repo := &fakeNodeRepo{node: &domain.Node{ID: "n1", TeamID: "default", ClickHouseTable: "db.t"}}
		uc := NewMetricsUsecase(nil, nil, repo, log)
		got, err := uc.NodeMetrics(context.Background(), "n1", "default", time.Hour, 10)
		require.NoError(t, err)
		require.False(t, got.ChartAvailable)
	})

	t.Run("happy path returns kpi+series", func(t *testing.T) {
		t.Parallel()
		repo := &fakeNodeRepo{node: &domain.Node{ID: "n1", TeamID: "default", ClickHouseTable: "db.t"}}
		ch := &fakeCH{
			kpi:    port.NodeKPI{Total: 100, Delivered: 98, Errors: 2, P95ms: 87, P99ms: 142},
			series: []port.SeriesPoint{{TsMs: 1, Count: 10}, {TsMs: 2, Count: 20}},
		}
		uc := NewMetricsUsecase(nil, ch, repo, log)
		got, err := uc.NodeMetrics(context.Background(), "n1", "default", time.Hour, 10)
		require.NoError(t, err)
		require.True(t, got.ChartAvailable)
		require.EqualValues(t, 100, got.KPI.Total)
		require.EqualValues(t, 142, got.KPI.P99ms)
		require.Len(t, got.Series, 2)
		require.EqualValues(t, time.Hour.Milliseconds(), got.RangeMs)
	})

	t.Run("empty teamID skips scope check", func(t *testing.T) {
		t.Parallel()
		repo := &fakeNodeRepo{node: &domain.Node{ID: "n1", TeamID: "whatever", ClickHouseTable: ""}}
		uc := NewMetricsUsecase(nil, nil, repo, log)
		_, err := uc.NodeMetrics(context.Background(), "n1", "", time.Hour, 10)
		require.NoError(t, err)
	})
}
