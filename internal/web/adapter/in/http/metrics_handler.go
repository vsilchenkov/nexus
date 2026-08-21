package http

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"nexus/internal/domain"
	"nexus/internal/domain/logsearch"
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

// rangePresets — допустимые окна-пресеты (§28 Пункт 4): 1h/3h/24h/7d/14d/30d.
// 15m оставлен для обратной совместимости.
//
// §79.5: плотность графика больше не свойство пресета — её задаёт «Шаг
// графика», а расчёт по умолчанию живёт в usecase (autoChartBuckets), рядом с
// согласованием пары (окно, шаг). Здесь остались только длительности.
var rangePresets = map[string]time.Duration{
	"15m": 15 * time.Minute,
	"1h":  time.Hour,
	"3h":  3 * time.Hour,
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
	"14d": 14 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
}

// parseRange — окно-пресет из query-параметра range; дефолт 1h.
func parseRange(s string) time.Duration {
	if d, ok := rangePresets[s]; ok {
		return d
	}
	return time.Hour
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
// §79.5: число столбцов графика отсюда ушло — его считает usecase из пары
// (окно, «Шаг графика»). Handler больше не решает, как рисовать.
func resolveWindow(c *gin.Context) (since, until time.Time) {
	from, okF := parseTimeParam(c.Query("from"))
	to, okT := parseTimeParam(c.Query("to"))
	if okF && okT && from.Before(to) {
		return from, to
	}
	now := time.Now()
	return now.Add(-parseRange(c.Query("range"))), now
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
	// NodeID — идентификатор узла (§86.7). Клиент сшивает строки таблицы с
	// метриками именно по нему: путь уникален лишь внутри команды, и в сквозном
	// режиме две команды могут иметь узлы с одинаковым путём.
	NodeID string    `json:"node_id"`
	Node   string    `json:"node"`
	In     uint64    `json:"in"`
	Out    uint64    `json:"out"`
	Errors uint64    `json:"errors"`
	P95ms  float64   `json:"p95_ms"`
	Spark  []float64 `json:"spark"`
	// SparkErr — ошибки в тех же бакетах, что и Spark (§52-доп). Отдельным
	// полем, а не заменой spark: старый клиент продолжает читать spark как
	// прежде. Пустой ряд означает «разбивки нет» (Prometheus-fallback), а не
	// «ошибок ноль» — рисовать его зелёным нельзя.
	SparkErr []float64 `json:"spark_err"`
	// §41 (back-compat): последний исходящий вызов узла завершился ошибкой
	// («любой не-2xx» = last_outcome != "ok"). UI использует last_outcome;
	// поле сохранено для внешних потребителей metrics:read.
	LastError bool `json:"last_error"`
	// §52: исход последнего исходящего вызова узла — "ok" (2xx) | "degraded"
	// (ответил не-2xx <500) | "down" (транспортная ошибка или 5xx).
	LastOutcome string `json:"last_outcome" enums:"ok,degraded,down"`
}

// overviewTotalsDTO — агрегат для KPI шапки (§44.A): СУММА строк items за тот же
// период. error_rate = errors/incoming (0..1).
type overviewTotalsDTO struct {
	Incoming  uint64  `json:"incoming"`
	Outgoing  uint64  `json:"outgoing"`
	Errors    uint64  `json:"errors"`
	ErrorRate float64 `json:"error_rate"`
}

// maxNodeIDsPerRequest — потолок узлов в одном порционном запросе метрик (§86.4).
// Фронт грузит пачками по ~50; потолок отсекает попытку затянуть весь инстанс
// одним вызовом в обход порционности.
const maxNodeIDsPerRequest = 200

// metricsScope собирает скоуп расчёта метрик из запроса (§86.4).
// ok=false — ответ уже записан.
func metricsScope(c *gin.Context) (usecase.NodesScope, bool) {
	sc := usecase.NodesScope{TeamID: currentTeamID(c)}
	if ids := splitCSV(c.Query("node_ids")); len(ids) > 0 {
		if len(ids) > maxNodeIDsPerRequest {
			c.JSON(http.StatusBadRequest, ErrorResponse{Error: "too many node_ids"})
			return sc, false
		}
		sc.NodeIDs = ids
	}
	if !wantsAllTeams(c) {
		return sc, true
	}
	userID, allowed := resolveAllTeamsUser(c)
	if !allowed {
		return sc, false
	}
	// В сквозном режиме команда сессии не участвует: скоуп задают членства.
	sc.TeamID = ""
	sc.UserID = userID
	return sc, true
}

// NodesOverview godoc
// @Summary  Per-node throughput за окно + агрегат для шапки (§21, §44.A).
// @Description  Источник — ClickHouse-логи (уникальные запросы), fallback Prometheus. Поле totals = СУММА строк (incoming/outgoing/errors/error_rate) для KPI шапки. Без источника — пустой список с prometheus_available=false. scope=all (§86.4) считает по всем командам пользователя (только session-cookie); node_ids сужает расчёт до перечисленных узлов — порционная загрузка рабочего стола. При node_ids поле totals относится только к запрошенным узлам, полный агрегат отдаёт /api/metrics/totals.
// @Tags     metrics
// @Produce  json
// @Param    range     query  string  false  "1h | 3h | 24h | 7d | 14d | 30d (default 1h)"
// @Param    from      query  string  false  "период с (RFC3339 или UnixMilli); вместе с to задаёт произвольный период"
// @Param    to        query  string  false  "период по (RFC3339 или UnixMilli)"
// @Param    scope     query  string  false  "all — все команды пользователя (§86.4)"
// @Param    node_ids  query  string  false  "id узлов через запятую, максимум 200 (§86.4)"
// @Success  200  {object}  NodesMetricsResponse
// @Failure  400  {object}  ErrorResponse
// @Failure  403  {object}  ErrorResponse
// @Security CookieAuth
// @Security ApiTokenAuth
// @Router   /api/metrics/nodes [get]
func (h *MetricsHandler) NodesOverview(c *gin.Context) {
	since, until := resolveWindow(c)
	sc, ok := metricsScope(c)
	if !ok {
		return
	}
	res := h.uc.NodesOverviewScoped(c.Request.Context(), sc, since, until)
	items := make([]nodeThroughputDTO, 0, len(res.Items))
	for _, it := range res.Items {
		spark := it.Spark
		if spark == nil {
			spark = []float64{}
		}
		sparkErr := it.SparkErrors
		if sparkErr == nil {
			sparkErr = []float64{}
		}
		items = append(items, nodeThroughputDTO{
			NodeID: it.NodeID,
			Node:   it.Node, In: it.In, Out: it.Out, Errors: it.Errors,
			P95ms: it.P95ms, Spark: spark, SparkErr: sparkErr,
			LastError:   it.LastOutcome.IsError(),
			LastOutcome: string(it.LastOutcome),
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

// nodeRankDTO — лёгкая строка среза по узлу (§86.10): без p95 и спарклайна.
// Задаёт клиенту порядок строк и статус до порционной загрузки метрик.
type nodeRankDTO struct {
	NodeID      string `json:"node_id"`
	In          uint64 `json:"in"`
	Out         uint64 `json:"out"`
	Errors      uint64 `json:"errors"`
	LastOutcome string `json:"last_outcome"`
}

// NodesTotalsResponse — агрегат шапки и срез по узлам скоупа (§86.4, §86.10).
type NodesTotalsResponse struct {
	Totals overviewTotalsDTO `json:"totals"`
	Nodes  []nodeRankDTO     `json:"nodes"`
}

// NodesTotals godoc
// @Summary  Агрегат шапки рабочего стола по всему скоупу + срез по узлам (§86.4, §86.10).
// @Description  Сумма incoming/outgoing/errors по ВСЕМ узлам скоупа за окно, плюс лёгкий срез nodes (node_id, in, out, errors, last_outcome) — без p95 и спарклайнов. Нужен сквозному режиму: там строки таблицы грузятся порционно, и шапка обязана считаться отдельно, иначе её значение зависело бы от прокрутки; срез задаёт порядок строк («проблемные первыми») и статус до дозагрузки. node_ids здесь игнорируется. Результат кешируется на несколько секунд, одновременные промахи схлопываются в один расчёт.
// @Tags     metrics
// @Produce  json
// @Param    range  query  string  false  "1h | 3h | 24h | 7d | 14d | 30d (default 1h)"
// @Param    from   query  string  false  "период с (RFC3339 или UnixMilli)"
// @Param    to     query  string  false  "период по (RFC3339 или UnixMilli)"
// @Param    scope  query  string  false  "all — все команды пользователя (§86.4)"
// @Success  200  {object}  NodesTotalsResponse
// @Failure  403  {object}  ErrorResponse
// @Security CookieAuth
// @Security ApiTokenAuth
// @Router   /api/metrics/totals [get]
func (h *MetricsHandler) NodesTotals(c *gin.Context) {
	since, until := resolveWindow(c)
	sc, ok := metricsScope(c)
	if !ok {
		return
	}
	res := h.uc.OverviewTotalsScoped(c.Request.Context(), sc, since, until)
	nodes := make([]nodeRankDTO, 0, len(res.Nodes))
	for _, n := range res.Nodes {
		nodes = append(nodes, nodeRankDTO{
			NodeID:      n.NodeID,
			In:          n.In,
			Out:         n.Out,
			Errors:      n.Errors,
			LastOutcome: string(n.LastOutcome),
		})
	}
	c.JSON(http.StatusOK, NodesTotalsResponse{
		Totals: overviewTotalsDTO{
			Incoming:  res.Totals.Incoming,
			Outgoing:  res.Totals.Outgoing,
			Errors:    res.Totals.Errors,
			ErrorRate: res.Totals.ErrorRate,
		},
		Nodes: nodes,
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
	since, until := resolveWindow(c)
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
	// §84.6: последняя активность В ОКНЕ и под текущими фильтрами (UnixMilli);
	// 0 = в окне запросов не было. Не «за всё время» — см. port.NodeKPI.
	LastSeenMs int64 `json:"last_seen_ms"`
}

type seriesPointDTO struct {
	TsMs   int64  `json:"ts"`
	Count  uint64 `json:"count"`
	Errors uint64 `json:"errors"`
}

// latencyPointDTO — точка графика латентности (§84.5). attempts — попытки
// (строки), а не записи: по нулю клиент рвёт линию, а не рисует нулевую
// латентность.
type latencyPointDTO struct {
	TsMs     int64   `json:"ts"`
	P50ms    float64 `json:"p50_ms"`
	P95ms    float64 `json:"p95_ms"`
	Attempts uint64  `json:"attempts"`
}

// Node godoc
// @Summary  KPI + временной ряд графика узла (§21).
// @Description  Источник — ClickHouse-логи узла: точные счётчики по УНИКАЛЬНЫМ запросам. §79.4: принимает те же фильтры, что журнал логов (q/method/client_host/status/done) — KPI и график считаются под ними. §79.5: step задаёт ширину столбца, фактическая возвращается в step_seconds; столбец — записи по интервалу прихода с итоговым статусом (chart_unit=records), на больших окнах деградирует до счёта по прогонам (chart_unit=attempts). Без ClickHouse или при таймауте → нули с chart_available=false.
// @Tags     metrics
// @Produce  json
// @Param    id           path   string  true   "node id"
// @Param    range        query  string  false  "1h | 3h | 24h | 7d | 14d | 30d (default 1h)"
// @Param    from         query  string  false  "период с (RFC3339 или UnixMilli); вместе с to задаёт произвольный период"
// @Param    to           query  string  false  "период по (RFC3339 или UnixMilli)"
// @Param    step         query  string  false  "шаг графика: auto (default) | 1h | 3h | 24h | 7d | 14d | 30d"
// @Param    q            query  string  false  "полнотекстовый фильтр (§48)"
// @Param    q_case       query  string  false  "1 — учитывать регистр"
// @Param    q_word       query  string  false  "1 — слово целиком"
// @Param    q_regex      query  string  false  "1 — режим регулярного выражения"
// @Param    method       query  string  false  "фильтр по колонке method"
// @Param    client_host  query  string  false  "фильтр по хосту клиента (§67)"
// @Param    status       query  string  false  "ok | err (§72.1)"
// @Param    done         query  string  false  "yes | no"
// @Success  200  {object}  NodeMetricsResponse
// @Failure  400  {object}  ErrorResponse  "некорректный поисковый запрос"
// @Failure  404  {object}  ErrorResponse
// @Security CookieAuth
// @Security ApiTokenAuth
// @Router   /api/metrics/nodes/{id} [get]
func (h *MetricsHandler) Node(c *gin.Context) {
	nodeID := c.Param("id")
	since, until := resolveWindow(c)
	// §79.4: фильтры читаются тем же кодом, что у списка логов (мини-язык §48
	// разбирает usecase) — иначе метрики и журнал под одинаковыми фильтрами
	// показывали бы разное.
	res, err := h.uc.NodeMetrics(c.Request.Context(), usecase.NodeMetricsQuery{
		NodeID: nodeID,
		TeamID: currentTeamID(c),
		Since:  since,
		Until:  until,
		Step:   c.Query("step"),
		Filter: logQueryFromContext(c),
	})
	if err != nil {
		if errors.Is(err, logsearch.ErrBadQuery) {
			localizedError(c, http.StatusBadRequest, "error.bad_search_query")
			return
		}
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
	// Ряд латентности всегда массив, а не null: пустой ряд законен (в окне не
	// было запросов), и клиенту не приходится различать два «нет данных».
	latency := make([]latencyPointDTO, 0, len(res.Latency))
	for _, p := range res.Latency {
		latency = append(latency, latencyPointDTO{
			TsMs:     p.TsMs,
			P50ms:    p.P50ms,
			P95ms:    p.P95ms,
			Attempts: p.Attempts,
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"kpi": nodeKPIDTO{
			Total:      res.KPI.Total,
			Delivered:  res.KPI.Delivered,
			Errors:     res.KPI.Errors,
			P95ms:      res.KPI.P95ms,
			P99ms:      res.KPI.P99ms,
			LastSeenMs: res.KPI.LastSeenMs,
		},
		"series":            series,
		"chart_available":   res.ChartAvailable,
		"range_ms":          res.RangeMs,
		"step_seconds":      res.StepSec,
		"chart_unit":        res.ChartUnit,
		"latency":           latency,
		"latency_available": res.LatencyAvailable,
	})
}
