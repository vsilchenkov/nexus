package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/domain/logsearch"
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
	topicSizes   map[string]int64
	topicSizeErr error
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
func (f *fakeProm) KafkaTopicSizes(_ context.Context) (map[string]int64, error) {
	return f.topicSizes, f.topicSizeErr
}

// fakeNodeLogs — ClickHouse-источник per-node метрик (port.NodeLogMetrics).
// Поля got* фиксируют, ЧТО именно доехало до адаптера: §79.4 (фильтры и сужение
// по партициям) и §79.5 (шаг столбца и форма счёта) проверяются именно так.
type fakeNodeLogs struct {
	kpi      port.NodeKPI
	kpiErr   error
	chart    []port.SeriesPoint
	chartErr error

	gotKPIQuery   port.LogQuery
	gotChartQuery port.LogQuery
	gotChart      port.ChartQuery
	chartCalls    int
}

func (f *fakeNodeLogs) NodeKPI(_ context.Context, q port.LogQuery, _ bool) (port.NodeKPI, error) {
	f.gotKPIQuery = q
	return f.kpi, f.kpiErr
}
func (f *fakeNodeLogs) NodeChart(_ context.Context, q port.LogQuery, c port.ChartQuery) ([]port.SeriesPoint, error) {
	f.gotChartQuery, f.gotChart = q, c
	f.chartCalls++
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

// fakeNodeStatus — Redis-источник исхода последнего вызова (§46/§52,
// port.NodeStatusReader).
type fakeNodeStatus struct {
	m   map[string]domain.NodeOutcome
	err error
}

func (f *fakeNodeStatus) GetLastOutcomes(_ context.Context, _ []string) (map[string]domain.NodeOutcome, error) {
	return f.m, f.err
}

// --- Overview --------------------------------------------------------------

// §44.A: Overview отдаёт только очередь Kafka + флаг доступности Prometheus.
// Трафик (входящие/исходящие/ошибки) переехал в NodesOverview.Totals.
func TestMetricsUsecase_Overview(t *testing.T) {
	t.Parallel()
	log := logging.NewNoop()

	t.Run("no prometheus → degraded", func(t *testing.T) {
		t.Parallel()
		uc := NewMetricsUsecase(nil, nil, &fakeNodeRepo{}, nil, nil, log)
		got := uc.Overview(context.Background())
		require.False(t, got.PrometheusAvailable)
		require.Zero(t, got.KafkaQueue)
	})

	t.Run("happy path → kafka queue + available", func(t *testing.T) {
		t.Parallel()
		prom := &fakeProm{queue: 312}
		uc := NewMetricsUsecase(prom, nil, &fakeNodeRepo{}, nil, nil, log)
		got := uc.Overview(context.Background())
		require.True(t, got.PrometheusAvailable)
		require.EqualValues(t, 312, got.KafkaQueue)
		// Трафик в шапке больше не считается здесь (§44.A).
		require.Zero(t, got.Incoming24h)
		require.Zero(t, got.Outgoing24h)
		require.Zero(t, got.Errors24h)
	})

	t.Run("kafka queue error degrades, not panics", func(t *testing.T) {
		t.Parallel()
		prom := &fakeProm{queueErr: errors.New("boom")}
		uc := NewMetricsUsecase(prom, nil, &fakeNodeRepo{}, nil, nil, log)
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
		uc := NewMetricsUsecase(nil, nil, &fakeNodeRepo{}, nil, nil, log)
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
			// §41/§52: gauge=2 → последний вызов — down.
			lastErrs: map[string]float64{"webhook/send": 2},
		}
		uc := NewMetricsUsecase(prom, nil, &fakeNodeRepo{}, nil, nil, log)
		got := uc.NodesOverview(context.Background(), "", time.Now().Add(-time.Hour), time.Now())
		require.True(t, got.PrometheusAvailable)
		require.Len(t, got.Items, 1)
		require.Equal(t, "webhook/send", got.Items[0].Node)
		require.EqualValues(t, 4201, got.Items[0].In)
		require.EqualValues(t, 4198, got.Items[0].Out)
		require.EqualValues(t, 2, got.Items[0].Errors)
		require.Equal(t, domain.NodeOutcomeDown, got.Items[0].LastOutcome,
			"§41/§52: gauge=2 → Down")
		// §44.A: шапка = сумма строк (Prometheus-ветка).
		require.EqualValues(t, 4201, got.Totals.Incoming)
		require.EqualValues(t, 4198, got.Totals.Outgoing)
		require.EqualValues(t, 2, got.Totals.Errors)
		require.InDelta(t, 2.0/4201.0, got.Totals.ErrorRate, 1e-9)
	})

	t.Run("prom error degrades", func(t *testing.T) {
		t.Parallel()
		prom := &fakeProm{thrErr: errors.New("boom")}
		uc := NewMetricsUsecase(prom, nil, &fakeNodeRepo{}, nil, nil, log)
		got := uc.NodesOverview(context.Background(), "", time.Now().Add(-time.Hour), time.Now())
		require.False(t, got.PrometheusAvailable)
		require.Empty(t, got.Items)
	})

	// §46/§52: исход из двух источников — Redis приоритетнее Prometheus, узлы
	// без записи в Redis добираются из Prometheus (0→ok, 1→degraded, 2→down).
	t.Run("last outcome: redis priority + prometheus fallback", func(t *testing.T) {
		t.Parallel()
		prom := &fakeProm{
			throughput: map[string]port.NodeThroughput{
				"a/x": {In: 1}, "b/y": {In: 1}, "c/z": {In: 1}, "d/w": {In: 1},
			},
			// Prometheus: a/x — down, c/z — degraded, d/w — ok (нет значения).
			lastErrs: map[string]float64{"a/x": 2, "b/y": 2, "c/z": 1},
		}
		// Redis: a/x → degraded (перебивает Prometheus down), b/y → down;
		// c/z и d/w отсутствуют → fallback на Prometheus.
		rs := &fakeNodeStatus{m: map[string]domain.NodeOutcome{
			"a/x": domain.NodeOutcomeDegraded,
			"b/y": domain.NodeOutcomeDown,
		}}
		uc := NewMetricsUsecase(prom, nil, &fakeNodeRepo{}, nil, rs, log)
		got := uc.NodesOverview(context.Background(), "", time.Now().Add(-time.Hour), time.Now())
		by := map[string]NodeThroughputRow{}
		for _, it := range got.Items {
			by[it.Node] = it
		}
		require.Equal(t, domain.NodeOutcomeDegraded, by["a/x"].LastOutcome,
			"Redis degraded перебивает Prometheus down")
		require.Equal(t, domain.NodeOutcomeDown, by["b/y"].LastOutcome, "Redis down")
		require.Equal(t, domain.NodeOutcomeDegraded, by["c/z"].LastOutcome,
			"нет в Redis → fallback на Prometheus gauge=1 → degraded")
		require.Equal(t, domain.NodeOutcomeOK, by["d/w"].LastOutcome,
			"нет ни в Redis, ни в Prometheus → ok")
	})

	// §46: ошибка Redis не валит — деградация на Prometheus.
	t.Run("redis error degrades to prometheus", func(t *testing.T) {
		t.Parallel()
		prom := &fakeProm{
			throughput: map[string]port.NodeThroughput{"a/x": {In: 1}},
			lastErrs:   map[string]float64{"a/x": 2},
		}
		rs := &fakeNodeStatus{err: errors.New("redis down")}
		uc := NewMetricsUsecase(prom, nil, &fakeNodeRepo{}, nil, rs, log)
		got := uc.NodesOverview(context.Background(), "", time.Now().Add(-time.Hour), time.Now())
		require.Len(t, got.Items, 1)
		require.Equal(t, domain.NodeOutcomeDown, got.Items[0].LastOutcome,
			"ошибка Redis → fallback на Prometheus")
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
		uc := NewMetricsUsecase(nil, logs, repo, nil, nil, log) // prom=nil — путь именно CH
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
		// §44.A: шапка = сумма строк (CH-ветка) = только узел a/x (b/y нулевой).
		require.EqualValues(t, 50, got.Totals.Incoming)
		require.EqualValues(t, 47, got.Totals.Outgoing)
		require.EqualValues(t, 3, got.Totals.Errors)
		require.InDelta(t, 3.0/50.0, got.Totals.ErrorRate, 1e-9)
	})

	// §43.1: узел с выключенными логами хранит черновичное имя таблицы
	// («nexus_default.») — CH-запрос по нему не делается, строка нулевая.
	// На старом коде NodeKPI вызывался и строка получила бы Total фейка.
	t.Run("draft table name → нули без CH-запроса", func(t *testing.T) {
		t.Parallel()
		repo := &fakeNodeRepo{list: []*domain.Node{
			{Path: "ok/x", ClickHouseTable: "db.a"},
			{Path: "nolog/draft", ClickHouseTable: "nexus_default."}, // черновик
		}}
		logs := &fakeNodeLogs{kpi: port.NodeKPI{Total: 50, Delivered: 47, Errors: 3}}
		uc := NewMetricsUsecase(nil, logs, repo, nil, nil, log)
		got := uc.NodesOverview(context.Background(), "default", time.Now().Add(-time.Hour), time.Now())
		byNode := map[string]NodeThroughputRow{}
		for _, it := range got.Items {
			byNode[it.Node] = it
		}
		require.EqualValues(t, 50, byNode["ok/x"].In, "валидная таблица считается")
		require.Zero(t, byNode["nolog/draft"].In, "черновичное имя таблицы → нули, без CH-запроса")
		require.Zero(t, byNode["nolog/draft"].Errors)
	})

	t.Run("totals: пустой список → нулевой агрегат без паники", func(t *testing.T) {
		t.Parallel()
		uc := NewMetricsUsecase(nil, &fakeNodeLogs{}, &fakeNodeRepo{list: nil}, nil, nil, log)
		got := uc.NodesOverview(context.Background(), "default", time.Now().Add(-time.Hour), time.Now())
		require.Zero(t, got.Totals.Incoming)
		require.Zero(t, got.Totals.ErrorRate)
	})
}

// --- Diagnostics (§44.E) ---------------------------------------------------

// TestMetricsUsecase_Diagnostics кодирует гипотезы расхождения счётчиков:
// Prometheus считает ПОПЫТКИ (ретраи → outgoing>incoming), ClickHouse —
// УНИКАЛЬНЫЕ запросы (out≤in); CH-ошибки ≥ Prometheus (3xx/висящие).
func TestMetricsUsecase_Diagnostics(t *testing.T) {
	t.Parallel()
	log := logging.NewNoop()

	// CH-сторона: узел a/x с уникальными 809/739/70 (out≤in).
	repo := &fakeNodeRepo{list: []*domain.Node{{Path: "a/x", ClickHouseTable: "db.a"}}}
	logs := &fakeNodeLogs{kpi: port.NodeKPI{Total: 809, Delivered: 739, Errors: 70}}
	// Prometheus-сторона: попытки 794/807/68 (outgoing>incoming из-за ретраев).
	prom := &fakeProm{
		totals:     port.GlobalTotals{Incoming: 794, Outgoing: 807, Errors: 68},
		throughput: map[string]port.NodeThroughput{"a/x": {In: 794, Out: 807, Errors: 68}},
	}
	uc := NewMetricsUsecase(prom, logs, repo, nil, nil, log)
	d := uc.Diagnostics(context.Background(), "default", time.Now().Add(-24*time.Hour), time.Now())

	require.True(t, d.ClickHouseAvailable)
	require.True(t, d.PrometheusAvailable)
	// ClickHouse (уникальные): out ≤ in.
	require.EqualValues(t, 809, d.ClickHouse.Incoming)
	require.EqualValues(t, 739, d.ClickHouse.Outgoing)
	require.EqualValues(t, 70, d.ClickHouse.Errors)
	require.LessOrEqual(t, d.ClickHouse.Outgoing, d.ClickHouse.Incoming, "CH: уникальные доставленные ≤ уникальные")
	// Prometheus (попытки): outgoing > incoming (ретраи).
	require.EqualValues(t, 794, d.Prometheus.Incoming)
	require.EqualValues(t, 807, d.Prometheus.Outgoing)
	require.Greater(t, d.Prometheus.Outgoing, d.Prometheus.Incoming, "Prometheus: ретраи раздувают исходящие")
	// CH-ошибки ≥ Prometheus-ошибки (3xx/висящие у CH).
	require.GreaterOrEqual(t, d.ClickHouse.Errors, d.Prometheus.Errors)
	// Per-node сверка присутствует.
	require.Len(t, d.Nodes, 1)
	require.Equal(t, "a/x", d.Nodes[0].Node)
	require.EqualValues(t, 809, d.Nodes[0].CHIn)
	require.EqualValues(t, 794, d.Nodes[0].PromIn)
}

func TestMetricsUsecase_Diagnostics_NoPrometheus(t *testing.T) {
	t.Parallel()
	log := logging.NewNoop()
	repo := &fakeNodeRepo{list: []*domain.Node{{Path: "a/x", ClickHouseTable: "db.a"}}}
	logs := &fakeNodeLogs{kpi: port.NodeKPI{Total: 10, Delivered: 9, Errors: 1}}
	uc := NewMetricsUsecase(nil, logs, repo, nil, nil, log) // prom=nil
	d := uc.Diagnostics(context.Background(), "default", time.Now().Add(-time.Hour), time.Now())
	require.True(t, d.ClickHouseAvailable)
	require.False(t, d.PrometheusAvailable)
	require.False(t, d.Prometheus.Available)
	require.EqualValues(t, 10, d.ClickHouse.Incoming)
}

// --- NodeMetrics -----------------------------------------------------------

func TestMetricsUsecase_NodeMetrics(t *testing.T) {
	t.Parallel()
	log := logging.NewNoop()

	t.Run("node not found propagates", func(t *testing.T) {
		t.Parallel()
		repo := &fakeNodeRepo{err: domain.ErrNodeNotFound}
		uc := NewMetricsUsecase(nil, nil, repo, nil, nil, log)
		_, err := uc.NodeMetrics(context.Background(), NodeMetricsQuery{NodeID: "x", TeamID: "default", Since: time.Now().Add(-time.Hour), Until: time.Now()})
		require.ErrorIs(t, err, domain.ErrNodeNotFound)
	})

	t.Run("team mismatch → not found", func(t *testing.T) {
		t.Parallel()
		repo := &fakeNodeRepo{node: &domain.Node{ID: "n1", Path: "p", TeamID: "other"}}
		uc := NewMetricsUsecase(nil, nil, repo, nil, nil, log)
		_, err := uc.NodeMetrics(context.Background(), NodeMetricsQuery{NodeID: "n1", TeamID: "default", Since: time.Now().Add(-time.Hour), Until: time.Now()})
		require.ErrorIs(t, err, domain.ErrNodeNotFound)
	})

	t.Run("no clickhouse table → chart unavailable", func(t *testing.T) {
		t.Parallel()
		// Узел без ClickHouseTable (логирование выключено) → метрики недоступны.
		repo := &fakeNodeRepo{node: &domain.Node{ID: "n1", Path: "p", TeamID: "default"}}
		uc := NewMetricsUsecase(nil, &fakeNodeLogs{}, repo, nil, nil, log)
		got, err := uc.NodeMetrics(context.Background(), NodeMetricsQuery{NodeID: "n1", TeamID: "default", Since: time.Now().Add(-time.Hour), Until: time.Now()})
		require.NoError(t, err)
		require.False(t, got.ChartAvailable)
		require.Zero(t, got.KPI.Total)
	})

	// §43.1: черновичное имя таблицы → та же деградация, что и без таблицы.
	// На старом коде NodeKPI вызывался и вернул бы Total фейка.
	t.Run("draft table name → chart unavailable", func(t *testing.T) {
		t.Parallel()
		repo := &fakeNodeRepo{node: &domain.Node{ID: "n1", Path: "nolog/draft", TeamID: "default", ClickHouseTable: "nexus_default."}}
		logs := &fakeNodeLogs{kpi: port.NodeKPI{Total: 100}}
		uc := NewMetricsUsecase(nil, logs, repo, nil, nil, log)
		got, err := uc.NodeMetrics(context.Background(), NodeMetricsQuery{NodeID: "n1", TeamID: "default", Since: time.Now().Add(-time.Hour), Until: time.Now()})
		require.NoError(t, err)
		require.False(t, got.ChartAvailable, "черновичное имя таблицы → метрики недоступны")
		require.Zero(t, got.KPI.Total)
	})

	t.Run("clickhouse error → degrade, not 500", func(t *testing.T) {
		t.Parallel()
		repo := &fakeNodeRepo{node: &domain.Node{ID: "n1", Path: "p", TeamID: "default", ClickHouseTable: "db.t"}}
		logs := &fakeNodeLogs{kpiErr: errors.New("boom")}
		uc := NewMetricsUsecase(nil, logs, repo, nil, nil, log)
		got, err := uc.NodeMetrics(context.Background(), NodeMetricsQuery{NodeID: "n1", TeamID: "default", Since: time.Now().Add(-time.Hour), Until: time.Now()})
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
		uc := NewMetricsUsecase(nil, logs, repo, nil, nil, log)
		got, err := uc.NodeMetrics(context.Background(), NodeMetricsQuery{NodeID: "n1", TeamID: "default", Since: time.Now().Add(-time.Hour), Until: time.Now()})
		require.NoError(t, err)
		require.True(t, got.ChartAvailable)
		require.EqualValues(t, 100, got.KPI.Total)
		require.EqualValues(t, 142, got.KPI.P99ms)
		require.Len(t, got.Series, 2)
		require.EqualValues(t, time.Hour.Milliseconds(), got.RangeMs)
	})

	// §79.4: фильтры журнала обязаны доехать до ClickHouse — иначе метрики и
	// список логов под одинаковыми фильтрами показывают разное.
	t.Run("§79.4 фильтры и сужение по партициям доезжают до адаптера", func(t *testing.T) {
		t.Parallel()
		repo := &fakeNodeRepo{node: &domain.Node{ID: "n1", Path: "p", TeamID: "default", ClickHouseTable: "db.t"}}
		logs := &fakeNodeLogs{kpi: port.NodeKPI{Total: 10}}
		uc := NewMetricsUsecase(nil, logs, repo, nil, nil, log)

		_, err := uc.NodeMetrics(context.Background(), NodeMetricsQuery{
			NodeID: "n1", TeamID: "default",
			Since: time.Now().Add(-time.Hour), Until: time.Now(),
			Filter: port.LogQuery{Method: "POST", ClientHost: "h1", Status: "err", Q: "order"},
		})
		require.NoError(t, err)
		require.Equal(t, "POST", logs.gotKPIQuery.Method)
		require.Equal(t, "h1", logs.gotKPIQuery.ClientHost)
		require.Equal(t, "err", logs.gotKPIQuery.Status)
		require.NotNil(t, logs.gotKPIQuery.QExpr, "мини-язык §48 обязан быть разобран usecase'ом")
		require.True(t, logs.gotKPIQuery.DateCreateAligned, "§72.4: сужение по партициям")
		require.Equal(t, logs.gotKPIQuery.Method, logs.gotChartQuery.Method,
			"график считается под теми же фильтрами, что KPI")
	})

	t.Run("§79.4 битый поисковый запрос → ошибка (400 у handler'а)", func(t *testing.T) {
		t.Parallel()
		repo := &fakeNodeRepo{node: &domain.Node{ID: "n1", Path: "p", TeamID: "default", ClickHouseTable: "db.t"}}
		uc := NewMetricsUsecase(nil, &fakeNodeLogs{}, repo, nil, nil, log)

		_, err := uc.NodeMetrics(context.Background(), NodeMetricsQuery{
			NodeID: "n1", TeamID: "default",
			Filter: port.LogQuery{Q: "(", QRegex: true},
		})
		require.ErrorIs(t, err, logsearch.ErrBadQuery)
	})

	t.Run("§72.4 внешняя таблица выключает сужение по партициям", func(t *testing.T) {
		t.Parallel()
		repo := &fakeNodeRepo{node: &domain.Node{
			ID: "n1", Path: "p", TeamID: "default", ClickHouseTable: "db.t", ExternalTable: true,
		}}
		logs := &fakeNodeLogs{kpi: port.NodeKPI{Total: 1}}
		uc := NewMetricsUsecase(nil, logs, repo, nil, nil, log)

		_, err := uc.NodeMetrics(context.Background(), NodeMetricsQuery{NodeID: "n1", TeamID: "default"})
		require.NoError(t, err)
		require.False(t, logs.gotKPIQuery.DateCreateAligned)
	})

	// §79.5: 14 дней с шагом 24 ч = столбцы по суткам; фактический шаг уезжает
	// наружу, потому что клиенту его больше неоткуда взять.
	t.Run("§79.5 шаг графика доезжает до адаптера и возвращается наружу", func(t *testing.T) {
		t.Parallel()
		repo := &fakeNodeRepo{node: &domain.Node{ID: "n1", Path: "p", TeamID: "default", ClickHouseTable: "db.t"}}
		logs := &fakeNodeLogs{kpi: port.NodeKPI{Total: 5}}
		uc := NewMetricsUsecase(nil, logs, repo, nil, nil, log)

		until := time.Now()
		got, err := uc.NodeMetrics(context.Background(), NodeMetricsQuery{
			NodeID: "n1", TeamID: "default",
			Since: until.Add(-14 * 24 * time.Hour), Until: until, Step: "24h",
		})
		require.NoError(t, err)
		require.EqualValues(t, 86400, logs.gotChart.StepSec)
		require.EqualValues(t, 86400, got.StepSec)
	})

	// §79.5: столбец считается «по итогу записи», поэтому после успешного
	// повтора красный сегмент уходит сам. На больших окнах форма деградирует —
	// и об этом обязано быть сказано наружу, а не молча.
	t.Run("§79.5 форма столбца: по итогу записи, с деградацией по порогу", func(t *testing.T) {
		t.Parallel()
		node := &domain.Node{ID: "n1", Path: "p", TeamID: "default", ClickHouseTable: "db.t"}

		small := &fakeNodeLogs{kpi: port.NodeKPI{Total: 100}}
		uc := NewMetricsUsecase(nil, small, &fakeNodeRepo{node: node}, nil, nil, log,
			WithExactChartMaxRecords(1000))
		got, err := uc.NodeMetrics(context.Background(), NodeMetricsQuery{NodeID: "n1", TeamID: "default"})
		require.NoError(t, err)
		require.True(t, small.gotChart.ByRecord)
		require.Equal(t, ChartUnitRecords, got.ChartUnit)

		big := &fakeNodeLogs{kpi: port.NodeKPI{Total: 5000}}
		uc = NewMetricsUsecase(nil, big, &fakeNodeRepo{node: node}, nil, nil, log,
			WithExactChartMaxRecords(1000))
		got, err = uc.NodeMetrics(context.Background(), NodeMetricsQuery{NodeID: "n1", TeamID: "default"})
		require.NoError(t, err)
		require.False(t, big.gotChart.ByRecord, "окно больше порога → дешёвая форма")
		require.Equal(t, ChartUnitAttempts, got.ChartUnit, "подмена семантики обязана быть видна клиенту")
	})

	t.Run("empty teamID skips scope check", func(t *testing.T) {
		t.Parallel()
		repo := &fakeNodeRepo{node: &domain.Node{ID: "n1", Path: "p", TeamID: "whatever"}}
		uc := NewMetricsUsecase(nil, nil, repo, nil, nil, log)
		_, err := uc.NodeMetrics(context.Background(), NodeMetricsQuery{NodeID: "n1", TeamID: "", Since: time.Now().Add(-time.Hour), Until: time.Now()})
		require.NoError(t, err)
	})
}
