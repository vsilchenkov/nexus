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
	// §41 («Down»): true, если последний исходящий вызов узла завершился
	// ошибкой (status 0/4xx/5xx). Overview красит узел в «Down» по этому флагу.
	LastError bool
}

// OverviewTotals — агрегат счётчиков шапки = СУММА строк таблицы узлов за тот же
// период и из того же источника (§43.A). Раньше шапка считалась отдельно из
// Prometheus (попытки, фикс. 24ч) и не сходилась с таблицей (CH, уникальные
// запросы). Теперь «итог в шапке = сумме видимых строк» по построению.
type OverviewTotals struct {
	Incoming  uint64
	Outgoing  uint64
	Errors    uint64
	ErrorRate float64 // errors / incoming (0..1)
}

// NodesOverview — батч per-node throughput за окно + агрегат для шапки.
type NodesOverview struct {
	Items               []NodeThroughputRow
	Totals              OverviewTotals
	PrometheusAvailable bool
}

// sumTotals — агрегат строк для шапки (§43.A). ErrorRate = доля недоставленных
// от входящих (Errors/Incoming), а не от исходящих — интуитивнее «X% входящих
// не доставлены».
func sumTotals(items []NodeThroughputRow) OverviewTotals {
	var t OverviewTotals
	for _, it := range items {
		t.Incoming += it.In
		t.Outgoing += it.Out
		t.Errors += it.Errors
	}
	if t.Incoming > 0 {
		t.ErrorRate = float64(t.Errors) / float64(t.Incoming)
	}
	return t
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

// Overview — KPI шапки, которые НЕ агрегируются из таблицы узлов: очередь Kafka
// (мгновенный lag, только в Prometheus) + флаг доступности Prometheus. Трафик
// (входящие/исходящие/ошибки) переехал в /api/metrics/nodes → totals (§43.A):
// там он = сумме строк таблицы за выбранный период (CH, уникальные запросы),
// поэтому шапка и таблица сходятся. Без Prometheus — нули с
// PrometheusAvailable=false; ошибка запроса деградирует (не 500).
func (u *MetricsUsecase) Overview(ctx context.Context) OverviewKPI {
	if u.prom == nil {
		return OverviewKPI{}
	}
	queue, err := u.prom.KafkaQueue(ctx)
	if err != nil {
		u.logger.Warn("prometheus kafka queue failed", u.logger.Err(err))
		return OverviewKPI{}
	}
	return OverviewKPI{
		KafkaQueue:          f2u(queue),
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
	var res NodesOverview
	if u.nodeLogs != nil {
		res = u.nodesOverviewCH(ctx, teamID, since, until)
	} else {
		res = u.nodesOverviewProm(ctx, since, until)
	}
	// §41 («Down»): оверлей исхода последнего вызова поверх любой ветки
	// (CH-источник его не считает, gauge живёт только в Prometheus).
	u.applyLastErrors(ctx, until, &res)
	// §43.A: агрегат для шапки = сумма строк (в любом источнике, за тот же
	// период) → «итог в шапке = сумме видимых строк таблицы».
	res.Totals = sumTotals(res.Items)
	return res
}

// applyLastErrors проставляет NodeThroughputRow.LastError из instant-gauge
// nexus_node_last_request_error (§41). Деградирует мягко: нет Prometheus или
// ошибка запроса → флаги остаются false (узлы не красятся в «Down» по нему).
func (u *MetricsUsecase) applyLastErrors(ctx context.Context, at time.Time, res *NodesOverview) {
	if u.prom == nil || len(res.Items) == 0 {
		return
	}
	le, err := u.prom.NodeLastErrors(ctx, at)
	if err != nil {
		u.logger.Warn("prometheus node last errors failed", u.logger.Err(err))
		return
	}
	for i := range res.Items {
		if le[res.Items[i].Node] >= 1 {
			res.Items[i].LastError = true
		}
	}
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

// DiagSource — агрегат одного источника для reconciliation (§43.E).
type DiagSource struct {
	Incoming  uint64
	Outgoing  uint64
	Errors    uint64
	Available bool
}

// DiagNode — сверка одного узла: ClickHouse (уникальные запросы) vs Prometheus
// (попытки). Ключ — path узла.
type DiagNode struct {
	Node                        string
	CHIn, CHOut, CHErrors       uint64
	PromIn, PromOut, PromErrors uint64
}

// Diagnostics — сверка счётчиков между источниками (§43.E): Prometheus (попытки,
// increase) против ClickHouse (уникальные запросы). Помогает объяснить
// расхождения шапки/таблицы: ретраи раздувают исходящие Prometheus
// (outgoing>incoming), увеличение CH-ошибок против Prometheus (3xx/висящие),
// занижение increase. Используется скилом анализа боевого Nexus.
type Diagnostics struct {
	SinceMs             int64
	UntilMs             int64
	Prometheus          DiagSource // глобальные попытки (GlobalTotals)
	ClickHouse          DiagSource // Σ уникальных (NodesOverview.Totals)
	Nodes               []DiagNode
	PrometheusAvailable bool
	ClickHouseAvailable bool
}

// Diagnostics собирает обе стороны за окно и per-node-сверку. CH-сторона
// переиспользует NodesOverview (тот же расчёт, что и таблица/шапка). Деградирует
// мягко: недоступный источник → нули + Available=false.
func (u *MetricsUsecase) Diagnostics(ctx context.Context, teamID string, since, until time.Time) Diagnostics {
	if until.IsZero() {
		until = time.Now()
	}
	if since.IsZero() || !since.Before(until) {
		since = until.Add(-time.Hour)
	}
	d := Diagnostics{SinceMs: since.UnixMilli(), UntilMs: until.UnixMilli()}

	// ClickHouse-сторона = NodesOverview (per-node + Totals). При nil CH —
	// Prometheus-fallback, тогда обе стороны совпадут (это нормально).
	ch := u.NodesOverview(ctx, teamID, since, until)
	d.ClickHouseAvailable = u.nodeLogs != nil
	d.ClickHouse = DiagSource{
		Incoming: ch.Totals.Incoming, Outgoing: ch.Totals.Outgoing,
		Errors: ch.Totals.Errors, Available: d.ClickHouseAvailable,
	}

	// Prometheus-сторона: глобальные попытки + per-node.
	promByNode := map[string]port.NodeThroughput{}
	if u.prom != nil {
		if gt, err := u.prom.GlobalTotals(ctx, until.Sub(since)); err == nil {
			d.Prometheus = DiagSource{
				Incoming: f2u(gt.Incoming), Outgoing: f2u(gt.Outgoing),
				Errors: f2u(gt.Errors), Available: true,
			}
			d.PrometheusAvailable = true
		} else {
			u.logger.Warn("diagnostics: prometheus global totals failed", u.logger.Err(err))
		}
		if pt, err := u.prom.NodeThroughput(ctx, since, until); err == nil {
			promByNode = pt
		} else {
			u.logger.Warn("diagnostics: prometheus node throughput failed", u.logger.Err(err))
		}
	}

	// Per-node merge: узлы из CH + те, что есть только в Prometheus (orphan).
	d.Nodes = make([]DiagNode, 0, len(ch.Items))
	seen := make(map[string]bool, len(ch.Items))
	for _, it := range ch.Items {
		p := promByNode[it.Node]
		d.Nodes = append(d.Nodes, DiagNode{
			Node: it.Node, CHIn: it.In, CHOut: it.Out, CHErrors: it.Errors,
			PromIn: f2u(p.In), PromOut: f2u(p.Out), PromErrors: f2u(p.Errors),
		})
		seen[it.Node] = true
	}
	for node, p := range promByNode {
		if seen[node] {
			continue
		}
		d.Nodes = append(d.Nodes, DiagNode{
			Node: node, PromIn: f2u(p.In), PromOut: f2u(p.Out), PromErrors: f2u(p.Errors),
		})
	}
	return d
}
