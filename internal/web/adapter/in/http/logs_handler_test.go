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

// --- §42: превью тел и постраничная подгрузка/скачивание ---

// stubLogReaderBody — фиксирует аргументы GetBodyChunk и отдаёт настроенные
// превью/срез. Встраивает port.LogReader: неиспользуемые методы — заглушки.
type stubLogReaderBody struct {
	port.LogReader
	preview   *domain.LogRecord
	reqLen    int64
	respLen   int64
	chunk     string
	total     int64
	gotWhich  string
	gotOffset int
	gotLimit  int
}

func (s *stubLogReaderBody) GetByIDPreview(_ context.Context, _, _ string, _ int) (*domain.LogRecord, int64, int64, error) {
	return s.preview, s.reqLen, s.respLen, nil
}

func (s *stubLogReaderBody) GetBodyChunk(_ context.Context, _, _, which string, offset, limit int) (string, int64, error) {
	s.gotWhich, s.gotOffset, s.gotLimit = which, offset, limit
	return s.chunk, s.total, nil
}

func newLogsBodyRouter(reader port.LogReader) *gin.Engine {
	gin.SetMode(gin.TestMode)
	node := &domain.Node{ClickHouseTable: "nexus_default.t"}
	uc := usecase.NewLogsUsecase(reader, stubNodeRepoCH{node: node}, logging.NewNoop())
	h := NewLogsHandler(uc, logging.NewNoop())
	r := gin.New()
	r.GET("/nodes/:id/log/:logId", h.Get)
	r.GET("/nodes/:id/log/:logId/body", h.GetBody)
	r.GET("/nodes/:id/log/:logId/body/download", h.GetBodyDownload)
	return r
}

// TestLogsGet_PreviewLengths — Get отдаёт превью тел + полные длины request_len/
// response_len (§42).
func TestLogsGet_PreviewLengths(t *testing.T) {
	t.Parallel()
	reader := &stubLogReaderBody{
		preview: &domain.LogRecord{ID: "l1", Request: "req-preview", Response: "resp-preview"},
		reqLen:  100_000,
		respLen: 20_000_000,
	}
	r := newLogsBodyRouter(reader)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/nodes/n1/log/l1", nil))

	require.Equal(t, http.StatusOK, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "req-preview", body["request"])
	assert.EqualValues(t, 100_000, body["request_len"])
	assert.EqualValues(t, 20_000_000, body["response_len"])
}

// TestLogsBody_InvalidWhich_400 — без валидного which → 400.
func TestLogsBody_InvalidWhich_400(t *testing.T) {
	t.Parallel()
	r := newLogsBodyRouter(&stubLogReaderBody{})
	for _, q := range []string{"", "?which=bogus", "?which=headers"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/nodes/n1/log/l1/body"+q, nil))
		assert.Equal(t, http.StatusBadRequest, w.Code, "which=%q", q)
	}
}

// TestLogsBody_OK — срез возвращается с total/returned/eof.
func TestLogsBody_OK(t *testing.T) {
	t.Parallel()
	reader := &stubLogReaderBody{chunk: "abc", total: 10}
	r := newLogsBodyRouter(reader)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/nodes/n1/log/l1/body?which=response&offset=0", nil))

	require.Equal(t, http.StatusOK, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "response", body["which"])
	assert.Equal(t, "abc", body["chunk"])
	assert.EqualValues(t, 10, body["total"])
	assert.EqualValues(t, 3, body["returned"])
	assert.Equal(t, false, body["eof"])
}

// TestLogsBody_LimitClamped — limit выше капа клампится до bodyChunkMaxRunes.
func TestLogsBody_LimitClamped(t *testing.T) {
	t.Parallel()
	reader := &stubLogReaderBody{chunk: "x", total: 1}
	r := newLogsBodyRouter(reader)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/nodes/n1/log/l1/body?which=request&limit=999999999", nil))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, bodyChunkMaxRunes, reader.gotLimit)
	assert.Equal(t, "request", reader.gotWhich)
}

// TestLogsBodyDownload_OK — стрим всего тела как attachment с Content-Disposition.
func TestLogsBodyDownload_OK(t *testing.T) {
	t.Parallel()
	reader := &stubLogReaderBody{chunk: "full-body", total: int64(len([]rune("full-body")))}
	r := newLogsBodyRouter(reader)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/nodes/n1/log/l1/body/download?which=response", nil))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "full-body", w.Body.String())
	assert.Contains(t, w.Header().Get("Content-Disposition"), "attachment")
	assert.Contains(t, w.Header().Get("Content-Disposition"), "response-l1.txt")
	assert.Equal(t, "text/plain; charset=utf-8", w.Header().Get("Content-Type"))
}
