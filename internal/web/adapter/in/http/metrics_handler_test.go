package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
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

	// §84.5: латентность считается конкурентно с графиком — запись полей под
	// мьютексом, иначе -race красит тест, а не код.
	latencyStep int64
	latencyErr  error

	// §102: рейтинг методов — та же конкурентная группа.
	methods      []port.MethodStat
	methodsCut   bool
	methodsErr   error
	methodsCalls int
	methodsLim   int

	mu sync.Mutex
}

func (c *captureNodeLogs) NodeKPI(_ context.Context, q port.LogQuery, _ bool) (port.NodeKPI, error) {
	c.q = q
	return c.kpi, nil
}

func (c *captureNodeLogs) NodeChart(_ context.Context, q port.LogQuery, cq port.ChartQuery) ([]port.SeriesPoint, error) {
	c.mu.Lock()
	c.chartQ, c.chart = q, cq
	c.callsCh++
	c.mu.Unlock()
	return []port.SeriesPoint{{TsMs: 1, Count: 2}}, nil
}

func (c *captureNodeLogs) NodeLatencyChart(_ context.Context, _ port.LogQuery, stepSec int64) ([]port.LatencyPoint, error) {
	c.mu.Lock()
	c.latencyStep = stepSec
	err := c.latencyErr
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return []port.LatencyPoint{{TsMs: 1, P50ms: 5, P95ms: 30, Attempts: 2}}, nil
}

func (c *captureNodeLogs) NodeMethodStats(_ context.Context, _ port.LogQuery, _ bool, limit int) ([]port.MethodStat, bool, error) {
	c.mu.Lock()
	c.methodsCalls++
	c.methodsLim = limit
	rows, cut, err := c.methods, c.methodsCut, c.methodsErr
	c.mu.Unlock()
	return rows, cut, err
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

// TestNodeMetrics_MethodsFlag — §102: рейтинг методов приезжает только по
// требованию, и только его флаг отвечает за деградацию разбивки.
func TestNodeMetrics_MethodsFlag(t *testing.T) {
	t.Parallel()

	type methodsResp struct {
		Methods []struct {
			Method    string `json:"method"`
			Total     uint64 `json:"total"`
			Delivered uint64 `json:"delivered"`
		} `json:"methods"`
		Available bool `json:"methods_available"`
		Truncated bool `json:"methods_truncated"`
	}

	t.Run("без флага порт не зовётся, разбивки в ответе нет", func(t *testing.T) {
		t.Parallel()
		logs := &captureNodeLogs{kpi: port.NodeKPI{Total: 42, Delivered: 40}}
		r := newMetricsRouter(t, logs)

		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/metrics/nodes/n1?range=24h", nil))
		require.Equal(t, http.StatusOK, w.Code)

		assert.Zero(t, logs.methodsCalls, "вкладка «Обзор» ходит тем же эндпоинтом — лишний запрос к CH")
		var resp methodsResp
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.False(t, resp.Available)
		assert.Empty(t, resp.Methods)
	})

	t.Run("with_methods=1 — рейтинг в ответе, лимит задаёт сервер", func(t *testing.T) {
		t.Parallel()
		logs := &captureNodeLogs{
			kpi: port.NodeKPI{Total: 42, Delivered: 40},
			methods: []port.MethodStat{
				{Method: "v1/GetParcelsInfo", Total: 30, Delivered: 29},
				{Method: "", Total: 12, Delivered: 11},
			},
			methodsCut: true,
		}
		r := newMetricsRouter(t, logs)

		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/metrics/nodes/n1?range=24h&with_methods=1", nil))
		require.Equal(t, http.StatusOK, w.Code)

		assert.Equal(t, 1, logs.methodsCalls)
		assert.Equal(t, usecase.MethodsLimit, logs.methodsLim, "размер выдачи не берётся из запроса (§102.4)")

		var resp methodsResp
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.Len(t, resp.Methods, 2)
		assert.Equal(t, "v1/GetParcelsInfo", resp.Methods[0].Method)
		assert.EqualValues(t, 30, resp.Methods[0].Total)
		assert.Empty(t, resp.Methods[1].Method, "записи без подпути — законная строка рейтинга, а не пропуск")
		assert.True(t, resp.Available)
		assert.True(t, resp.Truncated)
	})

	t.Run("пустой рейтинг — массив, а не null", func(t *testing.T) {
		t.Parallel()
		r := newMetricsRouter(t, &captureNodeLogs{kpi: port.NodeKPI{Total: 1}})

		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/metrics/nodes/n1?with_methods=1", nil))
		require.Equal(t, http.StatusOK, w.Code)

		var raw map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &raw))
		assert.JSONEq(t, "[]", string(raw["methods"]), "клиенту не должно приходиться различать два «нет данных»")
	})

	t.Run("ошибка рейтинга гасит только его", func(t *testing.T) {
		t.Parallel()
		logs := &captureNodeLogs{kpi: port.NodeKPI{Total: 5}, methodsErr: errors.New("memory limit exceeded")}
		r := newMetricsRouter(t, logs)

		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/metrics/nodes/n1?with_methods=1", nil))
		require.Equal(t, http.StatusOK, w.Code, "деградация разбивки — не 500")

		var resp struct {
			methodsResp
			ChartAvailable bool `json:"chart_available"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.False(t, resp.Available)
		assert.True(t, resp.ChartAvailable, "KPI и график обязаны пережить деградацию рейтинга")
	})
}

// TestNodeMetricsResponse_CoversActualBody — страж дрейфа swagger-модели.
//
// Ответ собирается gin.H, а swagger описан по структуре NodeMetricsResponse:
// swagger-drift-check сравнивает docs/ с аннотациями и такого расхождения не
// видит. Так поля §84.5 (latency/latency_available) полтора раздела жили в
// ответе, но отсутствовали в документации. Тест сверяет НАБОР ключей.
func TestNodeMetricsResponse_CoversActualBody(t *testing.T) {
	t.Parallel()

	logs := &captureNodeLogs{kpi: port.NodeKPI{Total: 1}, methods: []port.MethodStat{{Method: "a", Total: 1}}}
	r := newMetricsRouter(t, logs)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/metrics/nodes/n1?with_methods=1", nil))
	require.Equal(t, http.StatusOK, w.Code)

	var body map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))

	documented := map[string]bool{}
	for f := range reflect.TypeFor[NodeMetricsResponse]().Fields() {
		tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if tag != "" && tag != "-" {
			documented[tag] = true
		}
	}
	for key := range body {
		assert.True(t, documented[key], "поле ответа %q не описано в NodeMetricsResponse — swagger отдаёт неполную модель", key)
	}
	for key := range documented {
		_, ok := body[key]
		assert.True(t, ok, "NodeMetricsResponse обещает поле %q, которого в ответе нет", key)
	}
}
