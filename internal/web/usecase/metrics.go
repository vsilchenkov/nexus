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
// Источники гибридные:
//   - Prometheus (prom) — глобальные KPI Overview, очередь Kafka, per-node
//     throughput. Опционален: при пустом prometheus.url prom == nil и методы
//     отдают деградацию (PrometheusAvailable=false), не ошибку.
//   - ClickHouse (ch) — per-node KPI и временной ряд на странице узла (точные
//     перцентили). Опционален: при отсутствии ClickHouse ch == nil, узел без
//     таблицы → ChartAvailable=false с нулевыми значениями.
type MetricsUsecase struct {
	prom   port.PromMetrics // может быть nil
	ch     port.CHMetrics   // может быть nil
	nodes  port.NodeRepo
	logger logging.Logger
}

func NewMetricsUsecase(prom port.PromMetrics, ch port.CHMetrics, nodes port.NodeRepo, logger logging.Logger) *MetricsUsecase {
	return &MetricsUsecase{prom: prom, ch: ch, nodes: nodes, logger: logger}
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

// NodeThroughputRow — строка per-node throughput для таблицы Overview.
type NodeThroughputRow struct {
	Node   string
	In     uint64
	Out    uint64
	Errors uint64
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
	ChartAvailable bool // есть ли CH-источник (узел с таблицей + CH доступен)
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

// NodesOverview — per-node throughput за окно (по умолчанию 1ч). Деградирует
// без Prometheus.
func (u *MetricsUsecase) NodesOverview(ctx context.Context, window time.Duration) NodesOverview {
	if u.prom == nil {
		return NodesOverview{Items: []NodeThroughputRow{}}
	}
	if window <= 0 {
		window = time.Hour
	}
	m, err := u.prom.NodeThroughput(ctx, window)
	if err != nil {
		u.logger.Warn("prometheus node throughput failed", u.logger.Err(err))
		return NodesOverview{Items: []NodeThroughputRow{}}
	}
	items := make([]NodeThroughputRow, 0, len(m))
	for node, t := range m {
		items = append(items, NodeThroughputRow{
			Node:   node,
			In:     f2u(t.In),
			Out:    f2u(t.Out),
			Errors: f2u(t.Errors),
		})
	}
	return NodesOverview{Items: items, PrometheusAvailable: true}
}

// NodeMetrics — KPI + ряд графика одного узла за окно rng, разбитый на buckets.
// Узел резолвится с проверкой team-scope (как в LogsUsecase). Узел без
// ClickHouseTable или отсутствие CH-провайдера → нулевые значения с
// ChartAvailable=false (штатное состояние, не ошибка).
func (u *MetricsUsecase) NodeMetrics(ctx context.Context, nodeID, teamID string, rng time.Duration, buckets int) (NodeMetrics, error) {
	n, err := u.nodes.Get(ctx, nodeID)
	if err != nil {
		return NodeMetrics{}, err
	}
	if teamID != "" && n.TeamID != teamID {
		return NodeMetrics{}, domain.ErrNodeNotFound
	}
	if rng <= 0 {
		rng = time.Hour
	}
	if buckets <= 0 {
		buckets = 48
	}
	res := NodeMetrics{RangeMs: rng.Milliseconds(), Series: []port.SeriesPoint{}}

	if u.ch == nil || n.ClickHouseTable == "" {
		return res, nil
	}
	until := time.Now()
	since := until.Add(-rng)
	sinceMs := since.UnixMilli()
	untilMs := until.UnixMilli()

	kpi, err := u.ch.NodeKPI(ctx, n.ClickHouseTable, sinceMs, untilMs)
	if err != nil {
		return NodeMetrics{}, err
	}
	series, err := u.ch.NodeSeries(ctx, n.ClickHouseTable, sinceMs, untilMs, buckets)
	if err != nil {
		return NodeMetrics{}, err
	}
	res.KPI = kpi
	res.Series = series
	res.ChartAvailable = true
	return res, nil
}
