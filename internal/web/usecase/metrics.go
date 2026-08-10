package usecase

import (
	"context"
	"time"

	"golang.org/x/sync/errgroup"

	"nexus/internal/domain"
	"nexus/internal/platform/clock"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/safego"
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
	prom       port.PromMetrics    // может быть nil
	nodeLogs   port.NodeLogMetrics // ClickHouse-логи (per-node KPI/график)
	nodes      port.NodeRepo
	settings   port.AppSettingsRepo  // §44-perf: режим подсчёта уникальных (может быть nil)
	nodeStatus port.NodeStatusReader // §46: персистентный «Down» из Redis (может быть nil)
	clock      clock.Clock           // §4: «сейчас» для окон по умолчанию
	// exactChartMax — §79.5.1: потолок записей окна, до которого график строится
	// точной формой («по итогу записи»). 0 → defaultExactChartMaxRecords.
	exactChartMax uint64
	logger        logging.Logger
}

// metricsTimeout — серверный потолок пары CH-запросов метрик узла (§79.4).
// Полнотекстовый фильтр читает тела с диска, а вкладка поллится каждые ~12 с:
// без потолка медленные запросы накладывались бы друг на друга. Превышение —
// штатная деградация ChartAvailable=false, как и любая другая ошибка CH.
const metricsTimeout = 10 * time.Second

// defaultExactChartMaxRecords — порог точной формы графика (§79.5.1). Точная
// форма держит строку на каждую запись окна (~60–80 байт): на 2 млн это ~150 МБ,
// на 10 млн — уже под гигабайт, а приблизительный режим здесь не помогает (HLL
// сжимает счётчики, но группировка по ID обязана хранить ключи).
const defaultExactChartMaxRecords uint64 = 2_000_000

// MetricsOption — функциональная опция конструктора.
type MetricsOption func(*MetricsUsecase)

// WithMetricsClock подменяет источник времени (§4 CLAUDE.md): от него зависит
// правая граница окна, когда запрос её не задал.
func WithMetricsClock(c clock.Clock) MetricsOption {
	return func(u *MetricsUsecase) { u.clock = c }
}

// WithExactChartMaxRecords задаёт порог §79.5.1 (0 = значение по умолчанию).
func WithExactChartMaxRecords(n uint64) MetricsOption {
	return func(u *MetricsUsecase) { u.exactChartMax = n }
}

// exactChartMaxRecords — действующий порог точной формы графика.
func (u *MetricsUsecase) exactChartMaxRecords() uint64 {
	if u.exactChartMax == 0 {
		return defaultExactChartMaxRecords
	}
	return u.exactChartMax
}

func NewMetricsUsecase(prom port.PromMetrics, nodeLogs port.NodeLogMetrics, nodes port.NodeRepo, settings port.AppSettingsRepo, nodeStatus port.NodeStatusReader, logger logging.Logger, opts ...MetricsOption) *MetricsUsecase {
	u := &MetricsUsecase{prom: prom, nodeLogs: nodeLogs, nodes: nodes, settings: settings, nodeStatus: nodeStatus, clock: clock.System(), logger: logger}
	for _, o := range opts {
		o(u)
	}
	return u
}

// approxCounts читает режим подсчёта уникальных из app_settings (§44-perf):
// false (дефолт) = точно (countDistinct/uniqExact), true = приблизительно
// (uniq/uniqIf, HyperLogLog). Деградирует в точный режим при nil settings или
// ошибке чтения — точность важнее, потеря производительности безопаснее ошибки.
func (u *MetricsUsecase) approxCounts(ctx context.Context) bool {
	if u.settings == nil {
		return false
	}
	s, err := u.settings.Get(ctx)
	if err != nil || s == nil || s.General.MetricsApproxCounts == nil {
		return false
	}
	return *s.General.MetricsApproxCounts
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
	// §41/§52: исход последнего исходящего вызова узла — ok (2xx) / degraded
	// (ответил не-2xx <500) / down (транспортная ошибка или 5xx). Overview
	// красит runtime-бейдж узла по этому полю.
	LastOutcome domain.NodeOutcome
}

// OverviewTotals — агрегат счётчиков шапки = СУММА строк таблицы узлов за тот же
// период и из того же источника (§44.A). Раньше шапка считалась отдельно из
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

// sumTotals — агрегат строк для шапки (§44.A). ErrorRate = доля недоставленных
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
	ChartAvailable bool // доступен ли источник (ClickHouse сконфигурирован и ответил)
	RangeMs        int64

	// StepSec — фактическая ширина столбца (§79.5). Отдаётся наружу, потому что
	// она могла быть скорректирована согласованием с окном, а клиенту нечем её
	// вывести из ряда, если в нём одна точка.
	StepSec int64

	// ChartUnit — как считаны столбцы: ChartUnitRecords (по итогу записи) или
	// ChartUnitAttempts (по прогонам в интервале, деградация на больших окнах,
	// §79.5.1). Молча подменять семантику нельзя — UI обязан сказать об этом.
	ChartUnit string

	// Latency — перцентили по тем же интервалам (§84.5). Единица — ПОПЫТКИ, а не
	// записи: это характеристика внешнего вызова (§79.5).
	Latency []port.LatencyPoint

	// LatencyAvailable — отдельный флаг, а не признак пустого ряда: пустой ряд
	// законен (в окне не было запросов), и отличить его от «запрос латентности
	// не удался» иначе нечем. Деградация латентности НЕ гасит график трафика.
	LatencyAvailable bool
}

// Единицы столбца графика (§79.5).
const (
	ChartUnitRecords  = "records"  // запись в интервале своего прихода, статус итоговый
	ChartUnitAttempts = "attempts" // запись в интервале прогона, статус по прогонам интервала
)

// NodeMetricsQuery — параметры запроса метрик узла (§79.4/§79.5).
type NodeMetricsQuery struct {
	NodeID string
	TeamID string // scope multi-tenancy: чужой узел → ErrNodeNotFound
	Since  time.Time
	Until  time.Time

	// Step — «Шаг графика»: "", "auto" или пресет 1h..30d (см. ParseChartStep).
	Step string

	// Filter — фильтры журнала логов (полнотекст, метод, хост клиента, статус).
	// Table/NodeID/окно/DateCreateAligned проставляет usecase.
	Filter port.LogQuery
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
// (входящие/исходящие/ошибки) переехал в /api/metrics/nodes → totals (§44.A):
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
		until = u.clock.Now()
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
	// §41/§52: оверлей исхода последнего вызова поверх любой ветки
	// (CH-источник его не считает, gauge живёт только в Prometheus).
	u.applyLastOutcomes(ctx, until, &res)
	// §44.A: агрегат для шапки = сумма строк (в любом источнике, за тот же
	// период) → «итог в шапке = сумме видимых строк таблицы».
	res.Totals = sumTotals(res.Items)
	return res
}

// applyLastOutcomes проставляет NodeThroughputRow.LastOutcome из ДВУХ
// источников (§46, §52): персистентный Redis (приоритет — переживает рестарт
// Sender/Web) и instant-gauge Prometheus nexus_node_last_request_error (§41,
// fallback для узлов, которых ещё нет в Redis; маппинг значений 0/1/2 —
// domain.OutcomeFromGaugeValue). Деградирует мягко: нет ни Redis, ни
// Prometheus (или ошибки запросов) → узел остаётся ok.
// §84.7: само правило приоритета переехало в LastOutcomeResolver — у него
// появился второй потребитель (бейдж на странице узла). Здесь остался только
// маппинг на строки таблицы. Поведение прежнее, в том числе трактовка
// «неизвестно» как ok: в таблице рабочего стола у бейджа нет пустого
// состояния, каждая строка обязана иметь тон.
func (u *MetricsUsecase) applyLastOutcomes(ctx context.Context, at time.Time, res *NodesOverview) {
	if len(res.Items) == 0 {
		return
	}
	paths := make([]string, len(res.Items))
	for i := range res.Items {
		paths[i] = res.Items[i].Node
	}
	got := NewLastOutcomeResolver(u.nodeStatus, u.prom, u.logger).Resolve(ctx, at, paths)
	for i := range res.Items {
		res.Items[i].LastOutcome = got[res.Items[i].Node].Outcome
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
	// §44-perf: режим подсчёта уникальных читаем ОДИН раз на весь батч (а не на
	// каждый узел) и передаём во все горутины.
	approx := u.approxCounts(ctx)
	rows := make([]NodeThroughputRow, len(nodes))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(12)
	for i, n := range nodes {
		rows[i] = NodeThroughputRow{Node: n.Path, Spark: []float64{}}
		if n.ClickHouseTable == "" {
			continue // нет логирования → нет per-node CH-метрик
		}
		// §43.1: черновичное/кривое имя таблицы (узел с выключенными логами
		// хранит недозаполненное «nexus_x.») — CH-запрос упал бы на «invalid
		// table name» и WRN-флудил на каждом поллинге дашборда. Деградируем
		// до нулей молча, как resolveNode в logs.go.
		if !domain.IsValidCHTableName(n.ClickHouseTable) {
			u.logger.Debug("nodes overview: skip node with invalid table name",
				u.logger.Str("node", n.Path), u.logger.Str("table", n.ClickHouseTable))
			continue
		}
		g.Go(func() error {
			q := port.LogQuery{
				Table:             n.ClickHouseTable,
				NodeID:            n.ID,
				SinceMs:           sinceMs,
				UntilMs:           untilMs,
				DateCreateAligned: !n.ExternalTable, // §72.4
			}
			kpi, kerr := u.nodeLogs.NodeKPI(gctx, q, approx)
			if kerr != nil {
				u.logger.Warn("nodes overview: node kpi failed",
					u.logger.Str("node", n.Path), u.logger.Err(kerr))
				return nil
			}
			// Спарклайн — отдельный запрос; для узлов без трафика (Total=0) он всё
			// равно плоский, поэтому второй запрос делаем только при наличии трафика.
			//
			// §79.5: ширина столбца выводится из окна ровно так, как раньше это
			// делал адаптер (окно/12), — картинка стола не меняется. Единица
			// столбца переходит на записи вместе с графиком узла: спарклайн
			// перестаёт расходиться с числами In/Out в своей же строке.
			spark := []float64{}
			if kpi.Total > 0 {
				sparkStep := max((untilMs-sinceMs)/nodesSparkBuckets/1000, 1)
				series, serr := u.nodeLogs.NodeChart(gctx, q, port.ChartQuery{
					StepSec:  sparkStep,
					ByRecord: kpi.Total <= u.exactChartMaxRecords(),
					Approx:   approx,
				})
				if serr == nil {
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
// §79.4/§79.5: запрос несёт фильтры журнала и «Шаг графика». Оба CH-вызова
// ограничены metricsTimeout — дорогой полнотекстовый фильтр обязан деградировать
// в ChartAvailable=false, а не копиться на 12-секундном поллинге вкладки.
func (u *MetricsUsecase) NodeMetrics(ctx context.Context, in NodeMetricsQuery) (NodeMetrics, error) {
	n, err := u.nodes.Get(ctx, in.NodeID)
	if err != nil {
		return NodeMetrics{}, err
	}
	if in.TeamID != "" && n.TeamID != in.TeamID {
		return NodeMetrics{}, domain.ErrNodeNotFound
	}
	until, since := in.Until, in.Since
	if until.IsZero() {
		until = u.clock.Now()
	}
	if since.IsZero() || !since.Before(until) {
		since = until.Add(-time.Hour)
	}
	window := until.Sub(since)
	stepSec, _ := resolveChartStep(window, ParseChartStep(in.Step))
	res := NodeMetrics{
		RangeMs:   window.Milliseconds(),
		Series:    []port.SeriesPoint{},
		StepSec:   stepSec,
		ChartUnit: ChartUnitRecords,
	}

	// Нет CH-таблицы (логирование выключено) → метрики недоступны (как «не настроено»).
	if u.nodeLogs == nil || n.ClickHouseTable == "" {
		return res, nil
	}
	// §43.1: черновичное имя таблицы («nexus_x.» у узла с выключенными логами) —
	// та же деградация, иначе WRN «invalid table name» на каждом поллинге страницы узла.
	if !domain.IsValidCHTableName(n.ClickHouseTable) {
		u.logger.Debug("node metrics: skip invalid table name",
			u.logger.Str("node", n.Path), u.logger.Str("table", n.ClickHouseTable))
		return res, nil
	}

	q := in.Filter
	// Мини-язык §48 разбирается здесь, как и у списка логов (CountLogs): один
	// разбор — один смысл фильтра на обоих экранах. Ошибка синтаксиса уезжает
	// наружу как logsearch.ErrBadQuery → 400.
	if err := parseSearch(&q); err != nil {
		return NodeMetrics{}, err
	}
	q.Table = n.ClickHouseTable
	q.NodeID = n.ID
	q.SinceMs, q.UntilMs = since.UnixMilli(), until.UnixMilli()
	q.BeforeID, q.Limit = "", 0
	q.DateCreateAligned = !n.ExternalTable // §72.4: сужение по колонке PARTITION BY

	mctx, cancel := context.WithTimeout(ctx, metricsTimeout)
	defer cancel()

	kpi, err := u.nodeLogs.NodeKPI(mctx, q, u.approxCounts(ctx))
	if err != nil {
		u.logger.Warn("clickhouse node kpi failed", u.logger.Err(err))
		return res, nil
	}
	// §79.5.1: точная форма столбца сворачивает строки в записи по всему окну и
	// держит хэш-таблицу на каждую запись. Размер окна уже известен из KPI —
	// решение бесплатно.
	byRecord := kpi.Total <= u.exactChartMaxRecords()
	if !byRecord {
		res.ChartUnit = ChartUnitAttempts
		u.logger.Debug("node metrics: chart degraded to attempts unit",
			u.logger.Str("node", n.Path), u.logger.Int("records", int(kpi.Total)))
	}
	// §84.5: график трафика и график латентности — два независимых запроса
	// (слить нельзя: точная форма трафика уже свернула строки в записи, а
	// перцентили считаются по попыткам). Идут КОНКУРЕНТНО под общим таймаутом,
	// поэтому время ответа вкладки не растёт; нагрузка на ClickHouse на этой
	// вкладке примерно удваивается — осознанная цена, названная в §84.5.
	var (
		series  []port.SeriesPoint
		latency []port.LatencyPoint
		latErr  error
	)
	g, gctx := errgroup.WithContext(mctx)
	g.Go(func() error {
		defer safego.Recover(u.logger, "web.metrics.node_chart")
		s, err := u.nodeLogs.NodeChart(gctx, q, port.ChartQuery{
			StepSec:  stepSec,
			ByRecord: byRecord,
			Approx:   u.approxCounts(ctx),
		})
		series = s
		return err
	})
	g.Go(func() error {
		defer safego.Recover(u.logger, "web.metrics.node_latency")
		t0 := u.clock.Now()
		l, err := u.nodeLogs.NodeLatencyChart(gctx, q, stepSec)
		// Ошибка латентности НЕ валит группу: она гасит только свой график.
		// Каскадить деградацию нельзя — без трафика и KPI вкладка бесполезна,
		// а без латентности всего лишь беднее.
		latency, latErr = l, err
		u.logger.Debug("node metrics: latency chart",
			u.logger.Str("node", n.Path),
			u.logger.Int("step_sec", int(stepSec)),
			u.logger.Int("points", len(l)),
			u.logger.Int("duration_ms", int(u.clock.Now().Sub(t0).Milliseconds())),
			u.logger.Any("failed", err != nil))
		return nil
	})
	if err := g.Wait(); err != nil {
		u.logger.Warn("clickhouse node chart failed", u.logger.Err(err))
		return res, nil
	}

	res.KPI = kpi
	res.Series = series
	res.ChartAvailable = true
	if latErr != nil {
		u.logger.Warn("clickhouse node latency failed", u.logger.Err(latErr))
	} else {
		res.Latency = latency
		res.LatencyAvailable = true
	}
	return res, nil
}

// DiagSource — агрегат одного источника для reconciliation (§44.E).
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

// Diagnostics — сверка счётчиков между источниками (§44.E): Prometheus (попытки,
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
		until = u.clock.Now()
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
