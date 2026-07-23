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

func (stubLogReaderUnavailable) DistinctMethods(_ context.Context, _, _ string, _ int) ([]string, error) {
	return nil, domain.ErrLogsBackendUnavailable
}

func (stubLogReaderUnavailable) DistinctClientHosts(_ context.Context, _, _ string, _ int) ([]string, error) {
	return nil, domain.ErrLogsBackendUnavailable
}

func (stubLogReaderUnavailable) DateRange(_ context.Context, _, _ string) (int64, int64, error) {
	return 0, 0, domain.ErrLogsBackendUnavailable
}

// stubLogReaderOK — чтения успешны (пустой результат).
type stubLogReaderOK struct{ port.LogReader }

func (stubLogReaderOK) Search(_ context.Context, _ port.LogQuery) ([]*domain.LogRecord, error) {
	return nil, nil
}

func (stubLogReaderOK) CountFailed(_ context.Context, _, _ string, _, _ int64) (uint64, error) {
	return 0, nil
}

func (stubLogReaderOK) DistinctMethods(_ context.Context, _, _ string, _ int) ([]string, error) {
	return []string{"v1/a", "v1/b"}, nil
}

func (stubLogReaderOK) DistinctClientHosts(_ context.Context, _, _ string, _ int) ([]string, error) {
	return []string{"srv-1c.vz78.vozovoz.ru"}, nil
}

func (stubLogReaderOK) DateRange(_ context.Context, _, _ string) (int64, int64, error) {
	return 1_700_000_000_000, 1_800_000_000_000, nil
}

func newLogsRouter(reader port.LogReader) *gin.Engine {
	gin.SetMode(gin.TestMode)
	node := &domain.Node{ClickHouseTable: "nexus_default.t"}
	uc := usecase.NewLogsUsecase(reader, stubNodeRepoCH{node: node}, logging.NewNoop())
	h := NewLogsHandler(uc, logging.NewNoop())
	r := gin.New()
	r.GET("/nodes/:id/logs", h.List)
	r.GET("/nodes/:id/logs/failed-count", h.CountFailed)
	r.GET("/nodes/:id/logs/methods", h.Methods)
	r.GET("/nodes/:id/logs/client-hosts", h.ClientHosts)
	r.GET("/nodes/:id/logs/date-range", h.DateRange)
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

// stubLogReaderCapture — запоминает LogQuery, дошедший до адаптера
// (проверка прокидывания параметров §48 через handler → usecase).
type stubLogReaderCapture struct {
	port.LogReader
	last port.LogQuery
}

func (s *stubLogReaderCapture) Search(_ context.Context, q port.LogQuery) ([]*domain.LogRecord, error) {
	s.last = q
	return nil, nil
}

// TestLogsList_SearchParams_Parsed — параметры §48 (q/q_case/q_word/method и
// сохранённые ip/host) доходят до адаптера; QExpr распарсен usecase'ом.
func TestLogsList_SearchParams_Parsed(t *testing.T) {
	t.Parallel()
	reader := &stubLogReaderCapture{}
	r := newLogsRouter(reader)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/nodes/n1/logs?q=foo+%26+url%3Abar&q_case=1&q_word=true&method=v1%2Fx&ip=1.2.3.4&host=h1", nil))

	require.Equal(t, http.StatusOK, w.Code)
	q := reader.last
	assert.Equal(t, "foo & url:bar", q.Q)
	assert.True(t, q.QCase)
	assert.True(t, q.QWord, "q_word=true — валидное значение флага")
	assert.False(t, q.QRegex)
	assert.Equal(t, "v1/x", q.Method)
	assert.Equal(t, "1.2.3.4", q.IP)
	assert.Equal(t, "h1", q.Host)
	require.NotNil(t, q.QExpr, "usecase обязан распарсить Q в QExpr")
	assert.True(t, q.QExpr.CaseSensitive)
	assert.True(t, q.QExpr.WholeWord)
	require.Len(t, q.QExpr.Groups, 1)
	assert.Len(t, q.QExpr.Groups[0], 2)
}

// TestLogsList_BadSearchQuery_400 — невалидный поисковый запрос (regex или
// мини-язык) → 400 с локализованной ошибкой, а не 500 (§48).
func TestLogsList_BadSearchQuery_400(t *testing.T) {
	t.Parallel()
	r := newLogsRouter(stubLogReaderOK{})

	for name, target := range map[string]string{
		"невалидный regex":      "/nodes/n1/logs?q=%28&q_regex=1",
		"пустой терм в И":       "/nodes/n1/logs?q=a+%26+%26+b",
		"префикс без текста":    "/nodes/n1/logs?q=url%3A",
		"висячее экранирование": "/nodes/n1/logs?q=foo%5C",
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
		require.Equal(t, http.StatusBadRequest, w.Code, "case %q", name)
		var body map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		assert.NotEmpty(t, body["error"], "case %q", name)
	}
}

// TestLogsFacets_OK — счастливый путь фасетов §48.3: methods отсортированный
// список, date-range — min/max в миллисекундах, оба с logs_available=true.
func TestLogsFacets_OK(t *testing.T) {
	t.Parallel()
	r := newLogsRouter(stubLogReaderOK{})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/nodes/n1/logs/methods", nil))
	require.Equal(t, http.StatusOK, w.Code)
	var mBody map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &mBody))
	assert.Equal(t, []any{"v1/a", "v1/b"}, mBody["items"])
	assert.Equal(t, true, mBody["logs_available"])

	// §67: фасет «Хост клиента» — тот же контракт, что у Methods.
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/nodes/n1/logs/client-hosts", nil))
	require.Equal(t, http.StatusOK, w.Code)
	var chBody map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &chBody))
	assert.Equal(t, []any{"srv-1c.vz78.vozovoz.ru"}, chBody["items"])
	assert.Equal(t, true, chBody["logs_available"])

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/nodes/n1/logs/date-range", nil))
	require.Equal(t, http.StatusOK, w.Code)
	var dBody map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &dBody))
	assert.EqualValues(t, 1_700_000_000_000, dBody["min_ms"])
	assert.EqualValues(t, 1_800_000_000_000, dBody["max_ms"])
	assert.Equal(t, true, dBody["logs_available"])
}

// TestLogsFacets_CHUnavailable_Degrades — CH недоступен: фасеты отдают
// 200 + logs_available=false и пустой payload (не 500, не спам Sentry).
func TestLogsFacets_CHUnavailable_Degrades(t *testing.T) {
	t.Parallel()
	r := newLogsRouter(stubLogReaderUnavailable{})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/nodes/n1/logs/methods", nil))
	require.Equal(t, http.StatusOK, w.Code)
	var mBody map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &mBody))
	assert.Equal(t, false, mBody["logs_available"])
	items, _ := mBody["items"].([]any)
	assert.Empty(t, items)

	// §67: фасет «Хост клиента» деградирует так же.
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/nodes/n1/logs/client-hosts", nil))
	require.Equal(t, http.StatusOK, w.Code)
	var chBody map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &chBody))
	assert.Equal(t, false, chBody["logs_available"])
	chItems, _ := chBody["items"].([]any)
	assert.Empty(t, chItems)

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/nodes/n1/logs/date-range", nil))
	require.Equal(t, http.StatusOK, w.Code)
	var dBody map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &dBody))
	assert.Equal(t, false, dBody["logs_available"])
	assert.EqualValues(t, 0, dBody["min_ms"])
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

// TestLogsGet_BodySizes — §42-доп: Get отдаёт истинные размеры тел
// request_size/response_size (байты, до усечения).
func TestLogsGet_BodySizes(t *testing.T) {
	t.Parallel()
	reader := &stubLogReaderBody{
		preview: &domain.LogRecord{ID: "l1", RequestSize: 12_288, ResponseSize: 34_567},
	}
	r := newLogsBodyRouter(reader)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/nodes/n1/log/l1", nil))

	require.Equal(t, http.StatusOK, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.EqualValues(t, 12_288, body["request_size"])
	assert.EqualValues(t, 34_567, body["response_size"])
}

// stubLogReaderList — Search отдаёт одну запись с размерами тел (§42-доп).
type stubLogReaderList struct{ port.LogReader }

func (stubLogReaderList) Search(_ context.Context, _ port.LogQuery) ([]*domain.LogRecord, error) {
	return []*domain.LogRecord{{ID: "l1", ResponseSize: 2048}}, nil
}

// TestLogsList_BodySizes — §42-доп: размеры возвращаются и в СПИСКЕ (не только
// в detail), причём нулевой request_size присутствует в JSON (нет omitempty) —
// фронт различает «0 байт» без ветвлений на undefined.
func TestLogsList_BodySizes(t *testing.T) {
	t.Parallel()
	r := newLogsRouter(stubLogReaderList{})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/nodes/n1/logs", nil))

	require.Equal(t, http.StatusOK, w.Code)
	var body struct {
		Items []map[string]any `json:"items"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Len(t, body.Items, 1)
	assert.EqualValues(t, 2048, body.Items[0]["response_size"])
	reqSize, ok := body.Items[0]["request_size"]
	require.True(t, ok, "request_size присутствует в JSON даже при нуле")
	assert.EqualValues(t, 0, reqSize)
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
