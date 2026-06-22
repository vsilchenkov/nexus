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

// --- стабы для LogsUsecase ---

// stubNodeRepoCH — узел с настроенной CH-таблицей (логирование включено).
type stubNodeRepoCH struct {
	port.NodeRepo
	node *domain.Node
}

func (s stubNodeRepoCH) Get(_ context.Context, id string) (*domain.Node, error) {
	n := *s.node
	n.ID = id
	return &n, nil
}

// stubLogReaderUnavailable — все чтения падают как «CH недоступен».
type stubLogReaderUnavailable struct{ port.LogReader }

func (stubLogReaderUnavailable) Search(_ context.Context, _ port.LogQuery) ([]*domain.LogRecord, error) {
	return nil, domain.ErrLogsBackendUnavailable
}

func (stubLogReaderUnavailable) CountFailed(_ context.Context, _, _ string, _, _ int64) (uint64, error) {
	return 0, domain.ErrLogsBackendUnavailable
}

// stubLogReaderOK — чтения успешны (пустой результат).
type stubLogReaderOK struct{ port.LogReader }

func (stubLogReaderOK) Search(_ context.Context, _ port.LogQuery) ([]*domain.LogRecord, error) {
	return nil, nil
}

func (stubLogReaderOK) CountFailed(_ context.Context, _, _ string, _, _ int64) (uint64, error) {
	return 0, nil
}

func newLogsRouter(reader port.LogReader) *gin.Engine {
	gin.SetMode(gin.TestMode)
	node := &domain.Node{ClickHouseTable: "nexus_default.t"}
	uc := usecase.NewLogsUsecase(reader, stubNodeRepoCH{node: node}, logging.NewNoop())
	h := NewLogsHandler(uc, logging.NewNoop())
	r := gin.New()
	r.GET("/nodes/:id/logs", h.List)
	r.GET("/nodes/:id/logs/failed-count", h.CountFailed)
	return r
}

// TestLogsList_CHUnavailable_Degrades — при недоступном CH List отдаёт
// 200 + logs_available=false и пустой список (а не 500), чтобы поллинг UI
// не флудил ошибками/Sentry.
func TestLogsList_CHUnavailable_Degrades(t *testing.T) {
	t.Parallel()
	r := newLogsRouter(stubLogReaderUnavailable{})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/nodes/n1/logs", nil))

	require.Equal(t, http.StatusOK, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, false, body["logs_available"])
	items, _ := body["items"].([]any)
	assert.Empty(t, items)
}

// TestLogsFailedCount_CHUnavailable_Degrades — CountFailed (KPI «неудачные
// доставки», вкладка «Очередь») при недоступном CH тоже 200 + флаг.
func TestLogsFailedCount_CHUnavailable_Degrades(t *testing.T) {
	t.Parallel()
	r := newLogsRouter(stubLogReaderUnavailable{})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/nodes/n1/logs/failed-count", nil))

	require.Equal(t, http.StatusOK, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, false, body["logs_available"])
	assert.EqualValues(t, 0, body["count"])
}

// TestLogsList_OK_AvailableTrue — счастливый путь: logs_available=true.
func TestLogsList_OK_AvailableTrue(t *testing.T) {
	t.Parallel()
	r := newLogsRouter(stubLogReaderOK{})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/nodes/n1/logs", nil))

	require.Equal(t, http.StatusOK, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, true, body["logs_available"])
}
