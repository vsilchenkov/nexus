package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
	"nexus/internal/web/usecase/port"
)

// §79.4/§79.5: handler метрик узла обязан читать те же фильтры, что журнал
// логов, и параметр шага графика. Проверяем через реальный usecase с фейковым
// CH-источником — так тест ловит и разрыв «handler прочитал, usecase потерял».

// captureNodeLogs — port.NodeLogMetrics, запоминающий аргументы.
type captureNodeLogs struct {
	kpi     port.NodeKPI
	q       port.LogQuery
	chart   port.ChartQuery
	chartQ  port.LogQuery
	callsCh int
}

func (c *captureNodeLogs) NodeKPI(_ context.Context, q port.LogQuery, _ bool) (port.NodeKPI, error) {
	c.q = q
	return c.kpi, nil
}

func (c *captureNodeLogs) NodeChart(_ context.Context, q port.LogQuery, cq port.ChartQuery) ([]port.SeriesPoint, error) {
	c.chartQ, c.chart = q, cq
	c.callsCh++
	return []port.SeriesPoint{{TsMs: 1, Count: 2}}, nil
}

// metricsNodeRepo — port.NodeRepo с одним узлом.
type metricsNodeRepo struct {
	port.NodeRepo
	node *domain.Node
}

func (r *metricsNodeRepo) Get(_ context.Context, _ string) (*domain.Node, error) {
	return r.node, nil
}

func newMetricsRouter(t *testing.T, logs port.NodeLogMetrics) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	repo := &metricsNodeRepo{node: &domain.Node{
		ID: "n1", Path: "demo", TeamID: "", ClickHouseTable: "db.t", Status: domain.NodeStatusEnabled,
	}}
	uc := usecase.NewMetricsUsecase(nil, logs, repo, nil, nil, logging.NewNoop())
	h := NewMetricsHandler(uc, logging.NewNoop())
	r := gin.New()
	r.GET("/api/metrics/nodes/:id", h.Node)
	return r
}

func TestNodeMetrics_ParsesStepAndFilters(t *testing.T) {
	t.Parallel()
	logs := &captureNodeLogs{kpi: port.NodeKPI{Total: 42, Delivered: 40}}
	r := newMetricsRouter(t, logs)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/api/metrics/nodes/n1?range=14d&step=24h&method=POST&client_host=h1&status=err&q=order", nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	assert.Equal(t, "POST", logs.q.Method, "фильтр метода обязан доехать (§79.4)")
	assert.Equal(t, "h1", logs.q.ClientHost)
	assert.Equal(t, "err", logs.q.Status)
	assert.NotNil(t, logs.q.QExpr, "полнотекстовый фильтр разобран")
	assert.EqualValues(t, 86400, logs.chart.StepSec, "шаг 24ч (§79.5)")

	var resp struct {
		StepSeconds int64  `json:"step_seconds"`
		ChartUnit   string `json:"chart_unit"`
		ChartAvail  bool   `json:"chart_available"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.EqualValues(t, 86400, resp.StepSeconds, "фактический шаг обязан уехать клиенту")
	assert.Equal(t, usecase.ChartUnitRecords, resp.ChartUnit)
	assert.True(t, resp.ChartAvail)
}

func TestNodeMetrics_BadSearchQuery400(t *testing.T) {
	t.Parallel()
	r := newMetricsRouter(t, &captureNodeLogs{})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/metrics/nodes/n1?q=%28&q_regex=1", nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code,
		"кривой regex — ввод пользователя, а не повод для 500")
}

// Без параметра step поведение прежнее: шаг выводится из окна (сутки → 48
// столбцов по 30 минут). Регресс на случай, если «Шаг графика» когда-нибудь
// станет обязательным.
func TestNodeMetrics_AutoStepUnchanged(t *testing.T) {
	t.Parallel()
	logs := &captureNodeLogs{kpi: port.NodeKPI{Total: 1}}
	r := newMetricsRouter(t, logs)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/metrics/nodes/n1?range=24h", nil))
	require.Equal(t, http.StatusOK, w.Code)
	assert.EqualValues(t, 1800, logs.chart.StepSec)
}
