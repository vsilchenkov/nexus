package usecase

import (
	"context"
	"time"

	"golang.org/x/sync/errgroup"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// MetricsUsecase — дашборды метрик панели (§21).
//
// Два источника:
//   - Prometheus (prom, может быть nil): ГЛОБАЛЬНЫЕ/кросс-сервисные метрики —
//     KPI Overview (incoming/outgoing/errors 24ч), Kafka-мониторинг. При пустом
//     prometheus.url prom == nil → деградация (PrometheusAvailable=false).
//   - ClickHouse (nodeLogs): ТОЧНЫЕ per-node метрики — KPI/график страницы узла
//     (вкладки «Обзор»/«Метрики») И per-node throughput рабочего стола
//     (NodesOverview): один источник → цифры стола и узла совпадают, без
//     rate-экстраполяции Prometheus и без мерцания. Без CH NodesOverview
//     деградирует на Prometheus. См. §21.
type MetricsUsecase struct {
	prom     port.PromMetrics    // может быть nil
	nodeLogs port.NodeLogMetrics // ClickHouse-логи (per-node KPI/график)
	nodes    port.NodeRepo
	logger   logging.Logger
}

func NewMetricsUsecase(prom port.PromMetrics, nodeLogs port.NodeLogMetrics, nodes port.NodeRepo, logger logging.Logger) *MetricsUsecase {
	return &MetricsUsecase{prom: prom, nodeLogs: nodeLogs, nodes: nodes, logger: logger}
}

// OverviewKPI — 4 KPI головного экрана + флаг доступности Prometheus.
type OverviewKPI struct {
	Incoming24h         uint64
	Outgoing24h         uint64
	KafkaQueue          uint64
	Errors24h           uint64
	ErrorRate           float64 // errors / outgoing (0..1)
	PrometheusAvailable bool
}

// NodeThroughputRow — строка per-node throughput для таблицы/карточек Overview.
type NodeThroughputRow struct {
	Node   string
	In     uint64
	Out    uint64
	Errors uint64
	P95ms  float64   // §22: p95 латентности исходящих (Prometheus)
	Spark  []float64 // §22: спарклайн входящего трафика (range-запрос)
}

// NodesOverview — батч per-node throughput за окно.
type NodesOverview struct {
	Items               []NodeThroughputRow
	PrometheusAvailable bool
}

// NodeMetrics — KPI + временной ряд одного узла.
type NodeMetrics struct {
	KPI            port.NodeKPI
	Series         []port.SeriesPoint
	ChartAvailable bool // доступен ли источник (Prometheus сконфигурирован и ответил)
	RangeMs        int64
}

// f2u безопасно округляет неотрицательное float-значение Prometheus в uint64.
func f2u(v float64) uint64 {
	if v <= 0 {
		return 0
	}
	return uint64(v + 0.5)
}

// Overview — глобальные KPI за 24ч. Без Prometheus возвращает нули с
// PrometheusAvailable=false. Ошибки запроса логируются и тоже деградируют
// (не 500) — поллинг UI не должен спамить ошибками.
func (u *MetricsUsecase) Overview(ctx context.Context) OverviewKPI {
	if u.prom == nil {
		return OverviewKPI{}
	}
	totals, err := u.prom.GlobalTotals(ctx, 24*time.Hour)
	if err != nil {
		u.logger.Warn("prometheus global totals failed", u.logger.Err(err))
		return OverviewKPI{}
	}
	queue, err := u.prom.KafkaQueue(ctx)
	if err != nil {
		u.logger.Warn("prometheus kafka queue failed", u.logger.Err(err))
		return OverviewKPI{}
	}
	var rate float64
	if totals.Outgoing > 0 {
		rate = totals.Errors / totals.Outgoing
	}
	return OverviewKPI{
		Incoming24h:         f2u(totals.Incoming),
		Outgoing24h:         f2u(totals.Outgoing),
		KafkaQueue:          f2u(queue),
		Errors24h:           f2u(totals.Errors),
		ErrorRate:           rate,
		PrometheusAvailable: true,
	}
}

// nodesSparkBuckets — число точек спарклайна per-node на рабочем столе.
const nodesSparkBuckets = 12

// NodesOverview — per-node throughput за период (since, until]. Источник — те же
// ClickHouse-логи узла, что и вкладки «Обзор»/«Метрики» (§21): чтобы счётчики
// рабочего стола (вход/выход/ошибки/p95) и спарклайн ТОЧНО совпадали со страницей
// узла. Без ClickHouse (nodeLogs==nil) деградирует на Prometheus (старый путь).
// Пустой период нормализуется в последний час.
func (u *MetricsUsecase) NodesOverview(ctx context.Context, teamID string, since, until time.Time) NodesOverview {
	if until.IsZero() {
		until = time.Now()
	}
	if since.IsZero() || !since.Before(until) {
		since = until.Add(-time.Hour)
	}
	if u.nodeLogs != nil {
		return u.nodesOverviewCH(ctx, teamID, since, until)
	}
	return u.nodesOverviewProm(ctx, since, until)
}

// nodesOverviewCH — per-node throughput из CH-логов. Для каждого узла с таблицей
// конкурентно (cap'нутый errgroup) считаем KPI (вход=Total, выход=Delivered,
// ошибки, p95) и спарклайн (count по бакетам) — тем же NodeKPI/NodeChart, что и
// страница узла, поэтому цифры совпадают. Ошибка по одному узлу деградирует его до
// нулей, не валя весь список.
func (u *MetricsUsecase) nodesOverviewCH(ctx context.Context, teamID string, since, until time.Time) NodesOverview {
	nodes, err := u.nodes.List(ctx, port.ListNodesFilter{TeamID: teamID})
	if err != nil {
		u.logger.Warn("nodes overview: list nodes failed", u.logger.Err(err))
		return NodesOverview{Items: []NodeThroughputRow{}}
	}
	sinceMs, untilMs := since.UnixMilli(), until.UnixMilli()
	rows := make([]NodeThroughputRow, len(nodes))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(12)
	for i, n := range nodes {
		i, n := i, n
		rows[i] = NodeThroughputRow{Node: n.Path, Spark: []float64{}}
		if n.ClickHouseTable == "" {
			continue // нет логирования → нет per-node CH-метрик
		}
		g.Go(func() error {
			kpi, kerr := u.nodeLogs.NodeKPI(gctx, n.ClickHouseTable, n.ID, sinceMs, untilMs)
			if kerr != nil {
				u.logger.Warn("nodes overview: node kpi failed",
					u.logger.Str("node", n.Path), u.logger.Err(kerr))
				return nil
			}
			// Спарклайн — отдельный запрос; для узлов без трафика (Total=0) он всё
			// равно плоский, поэтому второй запрос делаем только при наличии трафика.
			spark := []float64{}
			if kpi.Total > 0 {
				if series, serr := u.nodeLogs.NodeChart(gctx, n.ClickHouseTable, n.ID, sinceMs, untilMs, nodesSparkBuckets); serr == nil {
					spark = make([]float64, len(series))
					for j, p := range series {
						spark[j] = float64(p.Count)
					}
				}
			}
			rows[i] = NodeThroughputRow{
				Node: n.Path, In: kpi.Total, Out: kpi.Delivered,
				Errors: kpi.Errors, P95ms: kpi.P95ms, Spark: spark,
			}
			return nil
		})
	}
	_ = g.Wait() // ошибки узлов уже залогированы и проглочены внутри
	return NodesOverview{Items: rows, PrometheusAvailable: true}
}

// nodesOverviewProm — fallback на Prometheus (когда ClickHouse не подключён).
func (u *MetricsUsecase) nodesOverviewProm(ctx context.Context, since, until time.Time) NodesOverview {
	if u.prom == nil {
		return NodesOverview{Items: []NodeThroughputRow{}}
	}
	m, err := u.prom.NodeThroughput(ctx, since, until)
	if err != nil {
		u.logger.Warn("prometheus node throughput failed", u.logger.Err(err))
		return NodesOverview{Items: []NodeThroughputRow{}}
	}
	// Спарклайн (12 точек) одним range-запросом на весь список. Ошибка
	// спарклайна не валит throughput — деградируем до пустых рядов.
	series, err := u.prom.NodeSeries(ctx, since, until, nodesSparkBuckets)
	if err != nil {
		u.logger.Warn("prometheus node series failed", u.logger.Err(err))
		series = map[string][]float64{}
	}
	items := make([]NodeThroughputRow, 0, len(m))
	for node, t := range m {
		spark := series[node]
		if spark == nil {
			spark = []float64{}
		}
		items = append(items, NodeThroughputRow{
			Node:   node,
			In:     f2u(t.In),
			Out:    f2u(t.Out),
			Errors: f2u(t.Errors),
			P95ms:  t.P95ms,
			Spark:  spark,
		})
	}
	return NodesOverview{Items: items, PrometheusAvailable: true}
}

// NodeMetrics — ТОЧНЫЕ KPI + ряд графика одного узла за окно из ClickHouse-логов
// узла (§21, вкладки «Обзор»/«Метрики»). Узел резолвится с проверкой team-scope.
// Источник — CH (не Prometheus): точные счётчики по уникальным запросам, без
// rate-экстраполяции и без мерцания. Если у узла нет ClickHouse-таблицы (нет
// логирования) или CH-запрос упал → нулевые значения с ChartAvailable=false
// (штатная деградация, не 500), чтобы поллинг UI не спамил ошибками.
func (u *MetricsUsecase) NodeMetrics(ctx context.Context, nodeID, teamID string, since, until time.Time, buckets int) (NodeMetrics, error) {
	n, err := u.nodes.Get(ctx, nodeID)
	if err != nil {
		return NodeMetrics{}, err
	}
	if teamID != "" && n.TeamID != teamID {
		return NodeMetrics{}, domain.ErrNodeNotFound
	}
	if until.IsZero() {
		until = time.Now()
	}
	if since.IsZero() || !since.Before(until) {
		since = until.Add(-time.Hour)
	}
	if buckets <= 0 {
		buckets = 48
	}
	res := NodeMetrics{RangeMs: until.Sub(since).Milliseconds(), Series: []port.SeriesPoint{}}

	// Нет CH-таблицы (логирование выключено) → метрики недоступны (как «не настроено»).
	if u.nodeLogs == nil || n.ClickHouseTable == "" {
		return res, nil
	}
	sinceMs, untilMs := since.UnixMilli(), until.UnixMilli()
	kpi, err := u.nodeLogs.NodeKPI(ctx, n.ClickHouseTable, n.ID, sinceMs, untilMs)
	if err != nil {
		u.logger.Warn("clickhouse node kpi failed", u.logger.Err(err))
		return res, nil
	}
	series, err := u.nodeLogs.NodeChart(ctx, n.ClickHouseTable, n.ID, sinceMs, untilMs, buckets)
	if err != nil {
		u.logger.Warn("clickhouse node chart failed", u.logger.Err(err))
		return res, nil
	}
	res.KPI = kpi
	res.Series = series
	res.ChartAvailable = true
	return res, nil
}
