package http

import (
	"errors"
	"net/http"
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
//	GET /api/metrics/nodes/{id}   — KPI + ряд графика узла (ClickHouse).
type MetricsHandler struct {
	uc     *usecase.MetricsUsecase
	logger logging.Logger
}

func NewMetricsHandler(uc *usecase.MetricsUsecase, logger logging.Logger) *MetricsHandler {
	return &MetricsHandler{uc: uc, logger: logger}
}

// rangeBuckets — допустимые окна диапазона и число бакетов для графика.
var rangeBuckets = map[string]struct {
	d       time.Duration
	buckets int
}{
	"15m": {15 * time.Minute, 30},
	"1h":  {time.Hour, 60},
	"24h": {24 * time.Hour, 48},
	"7d":  {7 * 24 * time.Hour, 84},
}

// parseRange — окно из query-параметра range; дефолт 1h.
func parseRange(s string) (time.Duration, int) {
	if rb, ok := rangeBuckets[s]; ok {
		return rb.d, rb.buckets
	}
	return time.Hour, 60
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
// @Summary  Глобальные KPI панели за 24ч (§21).
// @Description  Источник — Prometheus. Без настроенного prometheus.url возвращает нули с prometheus_available=false.
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
	Node   string `json:"node"`
	In     uint64 `json:"in"`
	Out    uint64 `json:"out"`
	Errors uint64 `json:"errors"`
}

// NodesOverview godoc
// @Summary  Per-node throughput за окно (§21).
// @Description  Источник — Prometheus (sum by node). Ключ node = path узла. Без Prometheus — пустой список с prometheus_available=false.
// @Tags     metrics
// @Produce  json
// @Param    range  query  string  false  "15m | 1h | 24h | 7d (default 1h)"
// @Success  200  {object}  map[string]any
// @Security CookieAuth
// @Security ApiTokenAuth
// @Router   /api/metrics/nodes [get]
func (h *MetricsHandler) NodesOverview(c *gin.Context) {
	window, _ := parseRange(c.Query("range"))
	res := h.uc.NodesOverview(c.Request.Context(), window)
	items := make([]nodeThroughputDTO, 0, len(res.Items))
	for _, it := range res.Items {
		items = append(items, nodeThroughputDTO{Node: it.Node, In: it.In, Out: it.Out, Errors: it.Errors})
	}
	c.JSON(http.StatusOK, gin.H{
		"items":                items,
		"prometheus_available": res.PrometheusAvailable,
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
// @Description  Источник — ClickHouse (точные перцентили). Узел без таблицы логов → нули с chart_available=false.
// @Tags     metrics
// @Produce  json
// @Param    id     path   string  true   "node id"
// @Param    range  query  string  false  "15m | 1h | 24h | 7d (default 1h)"
// @Success  200  {object}  map[string]any
// @Failure  404  {object}  map[string]string
// @Security CookieAuth
// @Security ApiTokenAuth
// @Router   /api/metrics/nodes/{id} [get]
func (h *MetricsHandler) Node(c *gin.Context) {
	nodeID := c.Param("id")
	rng, buckets := parseRange(c.Query("range"))
	res, err := h.uc.NodeMetrics(c.Request.Context(), nodeID, currentTeamID(c), rng, buckets)
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
