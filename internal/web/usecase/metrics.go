package usecase

import (
	"context"
	"time"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// MetricsUsecase — дашборды метрик панели (§21).
//
// Единый источник — Prometheus (prom): глобальные KPI Overview, очередь Kafka,
// per-node throughput и per-node KPI/график на странице узла. Опционален: при
// пустом prometheus.url prom == nil и методы отдают деградацию
// (PrometheusAvailable / ChartAvailable = false), не ошибку. ClickHouse в
// метриках больше не участвует — он остаётся чисто хранилищем логов.
type MetricsUsecase struct {
	prom   port.PromMetrics // может быть nil
	nodes  port.NodeRepo
	logger logging.Logger
}

func NewMetricsUsecase(prom port.PromMetrics, nodes port.NodeRepo, logger logging.Logger) *MetricsUsecase {
	return &MetricsUsecase{prom: prom, nodes: nodes, logger: logger}
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

// NodesOverview — per-node throughput за период (since, until]. Деградирует
// без Prometheus. Пустой период нормализуется в последний час.
func (u *MetricsUsecase) NodesOverview(ctx context.Context, since, until time.Time) NodesOverview {
	if u.prom == nil {
		return NodesOverview{Items: []NodeThroughputRow{}}
	}
	if until.IsZero() {
		until = time.Now()
	}
	if since.IsZero() || !since.Before(until) {
		since = until.Add(-time.Hour)
	}
	m, err := u.prom.NodeThroughput(ctx, since, until)
	if err != nil {
		u.logger.Warn("prometheus node throughput failed", u.logger.Err(err))
		return NodesOverview{Items: []NodeThroughputRow{}}
	}
	// Спарклайн (12 точек) одним range-запросом на весь список. Ошибка
	// спарклайна не валит throughput — деградируем до пустых рядов.
	const sparkBuckets = 12
	series, err := u.prom.NodeSeries(ctx, since, until, sparkBuckets)
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

// NodeMetrics — KPI + ряд графика одного узла за окно, разбитый на buckets.
// Узел резолвится с проверкой team-scope (как в LogsUsecase). Источник —
// Prometheus (по метке node = path узла). Без Prometheus (prom == nil) или при
// ошибке запроса → нулевые значения с ChartAvailable=false (штатная деградация,
// не 500) — единообразно с Overview/NodesOverview, чтобы поллинг UI не спамил
// ошибками.
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

	if u.prom == nil {
		return res, nil
	}
	kpi, err := u.prom.NodeKPI(ctx, n.Path, since, until)
	if err != nil {
		u.logger.Warn("prometheus node kpi failed", u.logger.Err(err))
		return res, nil
	}
	series, err := u.prom.NodeChart(ctx, n.Path, since, until, buckets)
	if err != nil {
		u.logger.Warn("prometheus node chart failed", u.logger.Err(err))
		return res, nil
	}
	res.KPI = kpi
	res.Series = series
	res.ChartAvailable = true
	return res, nil
}
