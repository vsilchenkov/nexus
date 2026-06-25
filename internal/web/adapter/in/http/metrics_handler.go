package http

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
)

// MetricsHandler — дашборды метрик панели (§21).
//
//	GET /api/metrics/overview     — глобальные KPI (Prometheus).
//	GET /api/metrics/nodes        — per-node throughput за окно (Prometheus).
//	GET /api/metrics/nodes/{id}   — KPI + ряд графика узла (Prometheus).
type MetricsHandler struct {
	uc     *usecase.MetricsUsecase
	logger logging.Logger
}

func NewMetricsHandler(uc *usecase.MetricsUsecase, logger logging.Logger) *MetricsHandler {
	return &MetricsHandler{uc: uc, logger: logger}
}

// rangeBuckets — допустимые окна-пресеты и число бакетов для графика (§28
// Пункт 4): 1h/3h/24h/7d/14d/30d. 15m оставлен для обратной совместимости.
var rangeBuckets = map[string]struct {
	d       time.Duration
	buckets int
}{
	"15m": {15 * time.Minute, 30},
	"1h":  {time.Hour, 60},
	"3h":  {3 * time.Hour, 60},
	"24h": {24 * time.Hour, 48},
	"7d":  {7 * 24 * time.Hour, 84},
	"14d": {14 * 24 * time.Hour, 84},
	"30d": {30 * 24 * time.Hour, 90},
}

// parseRange — окно-пресет из query-параметра range; дефолт 1h.
func parseRange(s string) (time.Duration, int) {
	if rb, ok := rangeBuckets[s]; ok {
		return rb.d, rb.buckets
	}
	return time.Hour, 60
}

// parseTimeParam парсит метку времени из query: RFC3339 или UnixMilli (число).
func parseTimeParam(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	if ms, err := strconv.ParseInt(s, 10, 64); err == nil && ms > 0 {
		return time.UnixMilli(ms), true
	}
	return time.Time{}, false
}

// resolveWindow определяет период метрик из query (§28 Пункт 4):
//   - произвольный календарный период: from+to (RFC3339 или UnixMilli);
//   - иначе пресет range (1h/3h/24h/7d/14d/30d), окно = [now-d, now].
//
// Возвращает (since, until, buckets). Для произвольного периода buckets берётся
// от ближайшего пресета по длительности (для разумной плотности графика).
func resolveWindow(c *gin.Context) (since, until time.Time, buckets int) {
	from, okF := parseTimeParam(c.Query("from"))
	to, okT := parseTimeParam(c.Query("to"))
	if okF && okT && from.Before(to) {
		return from, to, bucketsForDuration(to.Sub(from))
	}
	d, b := parseRange(c.Query("range"))
	now := time.Now()
	return now.Add(-d), now, b
}

// bucketsForDuration подбирает число бакетов графика по длительности окна.
func bucketsForDuration(d time.Duration) int {
	switch {
	case d <= time.Hour:
		return 60
	case d <= 24*time.Hour:
		return 48
	case d <= 7*24*time.Hour:
		return 84
	default:
		return 90
	}
}

type overviewKPIDTO struct {
	Incoming24h         uint64  `json:"incoming_24h"`
	Outgoing24h         uint64  `json:"outgoing_24h"`
	KafkaQueue          uint64  `json:"kafka_queue"`
	Errors24h           uint64  `json:"errors_24h"`
	ErrorRate           float64 `json:"error_rate"`
	PrometheusAvailable bool    `json:"prometheus_available"`
}

// Overview godoc
// @Summary  KPI шапки: очередь Kafka + доступность Prometheus (§21, §44.A).
// @Description  Только очередь Kafka (мгновенный lag, Prometheus). Трафик (входящие/исходящие/ошибки) переехал в GET /api/metrics/nodes → totals (за выбранный период, ClickHouse), чтобы шапка сходилась с таблицей. Без prometheus.url — нули с prometheus_available=false.
// @Tags     metrics
// @Produce  json
// @Success  200  {object}  overviewKPIDTO
// @Security CookieAuth
// @Security ApiTokenAuth
// @Router   /api/metrics/overview [get]
func (h *MetricsHandler) Overview(c *gin.Context) {
	kpi := h.uc.Overview(c.Request.Context())
	c.JSON(http.StatusOK, overviewKPIDTO{
		Incoming24h:         kpi.Incoming24h,
		Outgoing24h:         kpi.Outgoing24h,
		KafkaQueue:          kpi.KafkaQueue,
		Errors24h:           kpi.Errors24h,
		ErrorRate:           kpi.ErrorRate,
		PrometheusAvailable: kpi.PrometheusAvailable,
	})
}

type nodeThroughputDTO struct {
	Node   string    `json:"node"`
	In     uint64    `json:"in"`
	Out    uint64    `json:"out"`
	Errors uint64    `json:"errors"`
	P95ms  float64   `json:"p95_ms"`
	Spark  []float64 `json:"spark"`
	// §41 («Down»): последний исходящий вызов узла завершился ошибкой.
	LastError bool `json:"last_error"`
}

// overviewTotalsDTO — агрегат для KPI шапки (§44.A): СУММА строк items за тот же
// период. error_rate = errors/incoming (0..1).
type overviewTotalsDTO struct {
	Incoming  uint64  `json:"incoming"`
	Outgoing  uint64  `json:"outgoing"`
	Errors    uint64  `json:"errors"`
	ErrorRate float64 `json:"error_rate"`
}

// NodesOverview godoc
// @Summary  Per-node throughput за окно + агрегат для шапки (§21, §44.A).
// @Description  Источник — ClickHouse-логи (уникальные запросы), fallback Prometheus. Ключ node = path узла. Поле totals = СУММА строк (incoming/outgoing/errors/error_rate) для KPI шапки. Без источника — пустой список с prometheus_available=false.
// @Tags     metrics
// @Produce  json
// @Param    range  query  string  false  "1h | 3h | 24h | 7d | 14d | 30d (default 1h)"
// @Param    from   query  string  false  "период с (RFC3339 или UnixMilli); вместе с to задаёт произвольный период"
// @Param    to     query  string  false  "период по (RFC3339 или UnixMilli)"
// @Success  200  {object}  NodesMetricsResponse
// @Security CookieAuth
// @Security ApiTokenAuth
// @Router   /api/metrics/nodes [get]
func (h *MetricsHandler) NodesOverview(c *gin.Context) {
	since, until, _ := resolveWindow(c)
	res := h.uc.NodesOverview(c.Request.Context(), currentTeamID(c), since, until)
	items := make([]nodeThroughputDTO, 0, len(res.Items))
	for _, it := range res.Items {
		spark := it.Spark
		if spark == nil {
			spark = []float64{}
		}
		items = append(items, nodeThroughputDTO{
			Node: it.Node, In: it.In, Out: it.Out, Errors: it.Errors,
			P95ms: it.P95ms, Spark: spark, LastError: it.LastError,
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"items": items,
		"totals": overviewTotalsDTO{
			Incoming:  res.Totals.Incoming,
			Outgoing:  res.Totals.Outgoing,
			Errors:    res.Totals.Errors,
			ErrorRate: res.Totals.ErrorRate,
		},
		"prometheus_available": res.PrometheusAvailable,
	})
}

// diagSourceDTO / diagNodeDTO / diagnosticsDTO — сверка источников (§44.E).
type diagSourceDTO struct {
	Incoming  uint64 `json:"incoming"`
	Outgoing  uint64 `json:"outgoing"`
	Errors    uint64 `json:"errors"`
	Available bool   `json:"available"`
}

type diagNodeDTO struct {
	Node       string `json:"node"`
	CHIn       uint64 `json:"ch_in"`
	CHOut      uint64 `json:"ch_out"`
	CHErrors   uint64 `json:"ch_errors"`
	PromIn     uint64 `json:"prom_in"`
	PromOut    uint64 `json:"prom_out"`
	PromErrors uint64 `json:"prom_errors"`
}

type diagnosticsDTO struct {
	SinceMs             int64         `json:"since_ms"`
	UntilMs             int64         `json:"until_ms"`
	Prometheus          diagSourceDTO `json:"prometheus"`
	ClickHouse          diagSourceDTO `json:"clickhouse"`
	Nodes               []diagNodeDTO `json:"nodes"`
	PrometheusAvailable bool          `json:"prometheus_available"`
	ClickHouseAvailable bool          `json:"clickhouse_available"`
}

// Diagnostics godoc
// @Summary  Сверка счётчиков Prometheus↔ClickHouse за окно (§44.E).
// @Description  Возвращает обе стороны (Prometheus — попытки/increase; ClickHouse — уникальные запросы) и per-node-сверку. Помогает объяснить расхождение шапки/таблицы (ретраи → outgoing>incoming в Prometheus; CH-ошибки ≥ Prometheus при 3xx/висящих; занижение increase). Деградирует при недоступном источнике.
// @Tags     metrics
// @Produce  json
// @Param    range  query  string  false  "1h | 3h | 24h | 7d | 14d | 30d (default 1h)"
// @Param    from   query  string  false  "период с (RFC3339 или UnixMilli)"
// @Param    to     query  string  false  "период по (RFC3339 или UnixMilli)"
// @Success  200  {object}  diagnosticsDTO
// @Security CookieAuth
// @Security ApiTokenAuth
// @Router   /api/metrics/diagnostics [get]
func (h *MetricsHandler) Diagnostics(c *gin.Context) {
	since, until, _ := resolveWindow(c)
	res := h.uc.Diagnostics(c.Request.Context(), currentTeamID(c), since, until)
	nodes := make([]diagNodeDTO, 0, len(res.Nodes))
	for _, n := range res.Nodes {
		nodes = append(nodes, diagNodeDTO{
			Node: n.Node, CHIn: n.CHIn, CHOut: n.CHOut, CHErrors: n.CHErrors,
			PromIn: n.PromIn, PromOut: n.PromOut, PromErrors: n.PromErrors,
		})
	}
	src := func(s usecase.DiagSource) diagSourceDTO {
		return diagSourceDTO{Incoming: s.Incoming, Outgoing: s.Outgoing, Errors: s.Errors, Available: s.Available}
	}
	c.JSON(http.StatusOK, diagnosticsDTO{
		SinceMs: res.SinceMs, UntilMs: res.UntilMs,
		Prometheus: src(res.Prometheus), ClickHouse: src(res.ClickHouse),
		Nodes:               nodes,
		PrometheusAvailable: res.PrometheusAvailable,
		ClickHouseAvailable: res.ClickHouseAvailable,
	})
}

type nodeKPIDTO struct {
	Total     uint64  `json:"total"`
	Delivered uint64  `json:"delivered"`
	Errors    uint64  `json:"errors"`
	P95ms     float64 `json:"p95_ms"`
	P99ms     float64 `json:"p99_ms"`
}

type seriesPointDTO struct {
	TsMs   int64  `json:"ts"`
	Count  uint64 `json:"count"`
	Errors uint64 `json:"errors"`
}

// Node godoc
// @Summary  KPI + временной ряд графика узла (§21).
// @Description  Источник — Prometheus (per-node счётчики Sender'а; перцентили через histogram_quantile). Без Prometheus → нули с chart_available=false.
// @Tags     metrics
// @Produce  json
// @Param    id     path   string  true   "node id"
// @Param    range  query  string  false  "1h | 3h | 24h | 7d | 14d | 30d (default 1h)"
// @Param    from   query  string  false  "период с (RFC3339 или UnixMilli); вместе с to задаёт произвольный период"
// @Param    to     query  string  false  "период по (RFC3339 или UnixMilli)"
// @Success  200  {object}  NodeMetricsResponse
// @Failure  404  {object}  ErrorResponse
// @Security CookieAuth
// @Security ApiTokenAuth
// @Router   /api/metrics/nodes/{id} [get]
func (h *MetricsHandler) Node(c *gin.Context) {
	nodeID := c.Param("id")
	since, until, buckets := resolveWindow(c)
	res, err := h.uc.NodeMetrics(c.Request.Context(), nodeID, currentTeamID(c), since, until, buckets)
	if err != nil {
		if errors.Is(err, domain.ErrNodeNotFound) {
			localizedError(c, http.StatusNotFound, "node.not_found")
			return
		}
		h.logger.ErrorWithOp("node metrics failed", err, "metrics.node",
			h.logger.Str("node_id", nodeID))
		localizedError(c, http.StatusInternalServerError, "error.internal")
		return
	}
	series := make([]seriesPointDTO, 0, len(res.Series))
	for _, p := range res.Series {
		series = append(series, seriesPointDTO{TsMs: p.TsMs, Count: p.Count, Errors: p.Errors})
	}
	c.JSON(http.StatusOK, gin.H{
		"kpi": nodeKPIDTO{
			Total:     res.KPI.Total,
			Delivered: res.KPI.Delivered,
			Errors:    res.KPI.Errors,
			P95ms:     res.KPI.P95ms,
			P99ms:     res.KPI.P99ms,
		},
		"series":          series,
		"chart_available": res.ChartAvailable,
		"range_ms":        res.RangeMs,
	})
}
