package http

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/domain/logsearch"
	"nexus/internal/platform/i18n"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
	"nexus/internal/web/usecase/port"
)

// exportServiceFake проигрывает поток: Begin → строки → Finish (если не
// задана ошибка до потока).
type exportServiceFake struct {
	err   error
	stats port.LogExportStats
	meta  usecase.LogsExportMeta
	rows  []port.LogExportRow
	sum   usecase.LogsExportSummary

	gotOpt   usecase.LogsExportOptions
	gotQuery port.LogQuery
	gotActor usecase.Actor
	gotTeam  string
}

func (f *exportServiceFake) Limits() usecase.LogsExportLimits {
	return usecase.LogsExportLimits{MaxRows: 50_000, MaxBytes: 100 << 20, MaxBodyChars: 32_767}
}

func (f *exportServiceFake) Estimate(_ context.Context, _, _ string, q port.LogQuery) (port.LogExportStats, error) {
	f.gotQuery = q
	return f.stats, f.err
}

func (f *exportServiceFake) Export(ctx context.Context, actor usecase.Actor, _, teamID string,
	q port.LogQuery, opt usecase.LogsExportOptions, sink usecase.LogsExportSink,
) (usecase.LogsExportSummary, error) {
	f.gotOpt, f.gotQuery, f.gotActor, f.gotTeam = opt, q, actor, teamID
	if f.err != nil {
		return usecase.LogsExportSummary{}, f.err
	}
	if err := sink.Begin(ctx, f.meta); err != nil {
		return usecase.LogsExportSummary{}, err
	}
	for _, r := range f.rows {
		if err := sink.Row(ctx, r); err != nil {
			return usecase.LogsExportSummary{}, err
		}
	}
	return f.sum, sink.Finish(ctx, f.sum)
}

func exportEngine(svc logsExportService) *gin.Engine {
	return exportEngineLog(svc, logging.NewNoop())
}

func exportEngineLog(svc logsExportService, logger logging.Logger) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(i18n.GinMiddleware(), func(c *gin.Context) {
		c.Set(ctxSessionKey, &domain.Session{UserID: "u1", Login: "alice", CurrentTeamID: "t1"})
		c.Next()
	})
	h := NewLogsExportHandler(svc, "v1.41.0", logger)
	r.GET("/api/nodes/:id/logs/export.xlsx", h.Export)
	r.GET("/api/nodes/:id/logs/export/estimate", h.Estimate)
	return r
}

func exportGet(r *gin.Engine, url string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Accept-Language", "ru")
	r.ServeHTTP(w, req)
	return w
}

// bookParts — содержимое частей книги из тела ответа.
func bookParts(t *testing.T, b []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	require.NoError(t, err)
	parts := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		require.NoError(t, err)
		data, err := io.ReadAll(rc)
		require.NoError(t, err)
		require.NoError(t, rc.Close())
		parts[f.Name] = string(data)
	}
	return parts
}

func exportMeta(bodies bool) usecase.LogsExportMeta {
	m := usecase.LogsExportMeta{
		Node:  &domain.Node{ID: "n1", Path: "gate/PDT_Exchange", ClickHouseTable: "gate.logs"},
		Query: port.LogQuery{Status: "err", Q: "order", QRegex: true},
		Actor: usecase.Actor{UserLogin: "alice"}, Rows: 50_000, Bodies: bodies, Selected: 2,
		StartedAt: time.Date(2026, 10, 7, 9, 30, 0, 0, time.UTC),
		Limits:    usecase.LogsExportLimits{MaxBytes: 100 << 20},
	}
	if bodies {
		m.BodyChars = 40 // маркер обрезки (~36 символов) + 4 символа тела
	}
	return m
}

func TestLogsExportHandler_StreamsWorkbook(t *testing.T) {
	t.Parallel()
	req := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	svc := &exportServiceFake{
		meta: exportMeta(true),
		rows: []port.LogExportRow{
			{Rec: domain.LogRecord{
				ID: "a", Type: domain.RootMethodRequest, Request: "0123456789", Response: "=HYPERLINK(\"x\")",
				Status: 500, DateCreate: req.Truncate(24 * time.Hour), DateRequest: req,
				DateResponse: time.Unix(0, 0).UTC(), // незавершённый прогон
				RequestSize:  1 << 20,
			}, ReqRunes: 1 << 20, RespRunes: 16},
			{Rec: domain.LogRecord{ID: "b", Request: "short", Done: true, DateRequest: req}, ReqRunes: 5},
		},
		sum: usecase.LogsExportSummary{Status: usecase.LogsExportIncomplete, Reason: "timeout", Written: 2},
	}
	w := exportGet(exportEngine(svc), "/api/nodes/n1/logs/export.xlsx?rows=1000&bodies=1&tz=Europe/Moscow&status=err&q=order&q_regex=1")

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, xlsxContentType, w.Header().Get("Content-Type"))
	assert.Equal(t, `attachment; filename="nexus-logs-gate_PDT_Exchange-20261007-123000.xlsx"`, w.Header().Get("Content-Disposition"),
		"имя файла — путь узла и время начала в поясе браузера")
	assert.Equal(t, "no", w.Header().Get("X-Accel-Buffering"))
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Equal(t, "2", w.Header().Get("X-Nexus-Export-Rows"))
	assert.Equal(t, usecase.LogsExportOptions{Rows: 1000, Bodies: true}, svc.gotOpt)
	assert.Equal(t, "err", svc.gotQuery.Status)
	assert.Equal(t, "t1", svc.gotTeam)
	assert.Equal(t, "u1", svc.gotActor.UserID)

	parts := bookParts(t, w.Body.Bytes())
	logs := parts["xl/worksheets/sheet1.xml"]
	assert.Contains(t, logs, `<t xml:space="preserve">checksum_request</t>`, "шапка — имена колонок ClickHouse")
	assert.Contains(t, logs, `<t xml:space="preserve">request</t>`)
	// Урезанное тело несёт маркер с полной длиной и укладывается в 40 символов.
	assert.Contains(t, logs, ">0123…[обрезано, всего символов: 1048576]</t>")
	assert.NotContains(t, logs, "<t xml:space=\"preserve\"></t>", "пустые строки ячейками не пишутся")
	assert.NotContains(t, logs, "<f>", "формулы не пишутся — =HYPERLINK остаётся текстом")
	assert.Contains(t, logs, `=HYPERLINK("x")`)
	// 09:00 UTC = 12:00 MSK; эпоха date_response — пустая ячейка.
	assert.Contains(t, logs, `<c r="L2" s="2"><v>46302.5</v></c>`)
	assert.NotContains(t, logs, `r="M2"`)

	params := parts["xl/worksheets/sheet2.xml"]
	assert.Contains(t, params, "НЕПОЛНАЯ: ClickHouse отказал посреди выгрузки — таймаут запроса")
	assert.Contains(t, params, "gate/PDT_Exchange (n1)")
	assert.Contains(t, params, "order  [.*]")
	assert.Contains(t, params, "Europe/Moscow")
	assert.Contains(t, params, "v1.41.0")
	assert.Contains(t, parts["xl/workbook.xml"], `<sheet name="Логи"`)
	assert.Contains(t, parts["xl/workbook.xml"], `activeTab="1"`, "неполная выгрузка открывается на «Параметрах»")
}

// TestLogsExportHandler_AstralBodyOverflowGetsMarker — тело в пределах
// max_body_chars по символам, но длиннее ячейки Excel в UTF-16 (эмодзи — две
// единицы): до ревизии §115 писатель обрезал его молча, без маркера и мимо
// счётчика урезанных тел.
func TestLogsExportHandler_AstralBodyOverflowGetsMarker(t *testing.T) {
	t.Parallel()
	meta := exportMeta(true)
	meta.BodyChars = 32_767
	body := strings.Repeat("😀", 20_000) // 20 000 символов, 40 000 единиц UTF-16
	svc := &exportServiceFake{
		meta: meta,
		rows: []port.LogExportRow{{Rec: domain.LogRecord{ID: "a", Request: body}, ReqRunes: 20_000}},
		sum:  usecase.LogsExportSummary{Status: usecase.LogsExportComplete, Written: 1},
	}
	w := exportGet(exportEngine(svc), "/api/nodes/n1/logs/export.xlsx?bodies=1")
	require.Equal(t, http.StatusOK, w.Code)

	parts := bookParts(t, w.Body.Bytes())
	assert.Contains(t, parts["xl/worksheets/sheet1.xml"], "…[обрезано, всего символов: 20000]</t>")
	params := parts["xl/worksheets/sheet2.xml"]
	assert.Regexp(t, `Обрезано тел</t></is></c><c r="B\d+"><v>1</v>`, params, "тело учтено в урезанных")
	assert.Regexp(t, `Ячеек обрезано по пределу Excel</t></is></c><c r="B\d+"><v>0</v>`, params, "писателю резать нечего")
}

func TestLogsExportHandler_WithoutBodies(t *testing.T) {
	t.Parallel()
	svc := &exportServiceFake{
		meta: exportMeta(false),
		rows: []port.LogExportRow{{Rec: domain.LogRecord{ID: "a"}}},
		sum:  usecase.LogsExportSummary{Status: usecase.LogsExportComplete, Written: 1},
	}
	w := exportGet(exportEngine(svc), "/api/nodes/n1/logs/export.xlsx")
	require.Equal(t, http.StatusOK, w.Code)
	parts := bookParts(t, w.Body.Bytes())
	assert.NotContains(t, parts["xl/worksheets/sheet1.xml"], `>request<`)
	assert.NotContains(t, parts["xl/workbook.xml"], `activeTab="1"`, "полная выгрузка открывается на «Логах»")
	assert.Equal(t, usecase.LogsExportOptions{}, svc.gotOpt, "без параметров — потолок строк и без тел")
}

func TestLogsExportHandler_ErrorsBeforeStream(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		err        error
		code       int
		retryAfter string
	}{
		{"bad query", logsearch.ErrBadQuery, http.StatusBadRequest, ""},
		{"node not found", domain.ErrNodeNotFound, http.StatusNotFound, ""},
		{"logs not configured", domain.ErrNodeLogsNotConfigured, http.StatusNotFound, ""},
		{"rate limited", usecase.ErrLogsExportRateLimited, http.StatusTooManyRequests, "60"},
		{"busy", usecase.ErrLogsExportBusy, http.StatusServiceUnavailable, "30"},
		// Отказ общего ClickHouse — не 502/503/504: их nginx повторил бы на
		// соседней реплике тем же тяжёлым запросом (ревизия §115).
		{"clickhouse unavailable", domain.ErrLogsBackendUnavailable, http.StatusInternalServerError, ""},
		{"keys timeout", context.DeadlineExceeded, http.StatusInternalServerError, ""},
		{"other", errors.New("boom"), http.StatusInternalServerError, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := exportGet(exportEngine(&exportServiceFake{err: tt.err}), "/api/nodes/n1/logs/export.xlsx")
			assert.Equal(t, tt.code, w.Code)
			assert.Equal(t, tt.retryAfter, w.Header().Get("Retry-After"))
			assert.Contains(t, w.Header().Get("Content-Type"), "application/json", "отказ до потока — обычный JSON")
		})
	}
}

// TestLogsExportHandler_ClickHouseFailureText — таймаут отбора ключей (адаптер
// помечает его и как недоступность) подсказывает сузить выгрузку, а не
// сообщает о недоступном ClickHouse.
func TestLogsExportHandler_ClickHouseFailureText(t *testing.T) {
	t.Parallel()
	timeout := errors.Join(fmt.Errorf("export keys: %w", context.DeadlineExceeded), domain.ErrLogsBackendUnavailable)
	w := exportGet(exportEngine(&exportServiceFake{err: timeout}), "/api/nodes/n1/logs/export.xlsx")
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Contains(t, w.Body.String(), "задайте период или сузьте поиск")

	w = exportGet(exportEngine(&exportServiceFake{err: domain.ErrLogsBackendUnavailable}), "/api/nodes/n1/logs/export.xlsx")
	assert.Contains(t, w.Body.String(), "временно недоступен")
}

// TestLogsExportHandler_EstimateClientGoneIsQuiet — форма рвёт оценку при
// закрытии; адаптер помечает отмену и как ErrLogsBackendUnavailable, и до
// ревизии §115 каждое закрытие давало Warn «ClickHouse недоступен».
func TestLogsExportHandler_EstimateClientGoneIsQuiet(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	canceled := errors.Join(fmt.Errorf("export stats: %w", context.Canceled), domain.ErrLogsBackendUnavailable)
	r := exportEngineLog(&exportServiceFake{err: canceled}, bufferLogger(&buf))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/nodes/n1/logs/export/estimate", nil).WithContext(ctx))

	assert.NotContains(t, buf.String(), `"level":"WARN"`)
	assert.Contains(t, buf.String(), "client gone")
	assert.Empty(t, w.Body.String(), "клиенту, который ушёл, ответ не пишется")
}

func TestLogsExportHandler_Estimate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		err            error
		code           int
		wantConfigured bool
		wantAvailable  bool
		wantRecords    uint64
	}{
		{"ok", nil, http.StatusOK, true, true, 7},
		{"not configured", domain.ErrNodeLogsNotConfigured, http.StatusOK, false, false, 0},
		{"clickhouse unavailable", domain.ErrLogsBackendUnavailable, http.StatusOK, true, false, 0},
		{"count timeout", context.DeadlineExceeded, http.StatusOK, true, false, 0},
		{"bad query", logsearch.ErrBadQuery, http.StatusBadRequest, false, false, 0},
		{"node not found", domain.ErrNodeNotFound, http.StatusNotFound, false, false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			svc := &exportServiceFake{err: tt.err, stats: port.LogExportStats{Records: 7, Rows: 9, ReqBytes: 100}}
			w := exportGet(exportEngine(svc), "/api/nodes/n1/logs/export/estimate?status=ok")
			require.Equal(t, tt.code, w.Code)
			if tt.code != http.StatusOK {
				return
			}
			var got LogsExportEstimateResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
			assert.Equal(t, tt.wantConfigured, got.LogsConfigured)
			assert.Equal(t, tt.wantAvailable, got.LogsAvailable)
			assert.Equal(t, tt.wantRecords, got.Records)
			assert.Equal(t, 50_000, got.MaxRows, "лимиты отдаются и при деградации — форма их показывает")
			assert.Equal(t, 32_767, got.MaxBodyChars)
			assert.Equal(t, "ok", svc.gotQuery.Status)
		})
	}
}

func TestExportLocation(t *testing.T) {
	t.Parallel()
	log := logging.NewNoop()
	assert.Equal(t, "Europe/Moscow", exportLocation("Europe/Moscow", log).String())
	for _, bad := range []string{"", "Mars/Olympus", "../../etc/passwd", "/etc/localtime"} {
		assert.Equal(t, time.UTC, exportLocation(bad, log), bad)
	}
}

func TestExportFileName_SafeForHeader(t *testing.T) {
	t.Parallel()
	m := usecase.LogsExportMeta{
		Node:      &domain.Node{Path: `a/b"; x=привет`},
		StartedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}
	name := exportFileName(m, time.UTC)
	assert.Equal(t, "nexus-logs-a_bx-20260102-030405.xlsx", name)
	assert.False(t, strings.ContainsAny(name, "\"; "))
}

func TestXLSXSink_StatusLabelsTranslated(t *testing.T) {
	t.Parallel()
	for _, st := range []usecase.LogsExportStatus{
		usecase.LogsExportComplete, usecase.LogsExportLimitedRows, usecase.LogsExportLimitedBytes,
		usecase.LogsExportIncomplete, usecase.LogsExportAborted,
	} {
		for _, lang := range []i18n.Lang{i18n.LangEN, i18n.LangRU} {
			key := exportStatusKey(st)
			assert.NotEqual(t, key, i18n.Translate(lang, key), "%s/%s", lang, st)
		}
	}
	for _, reason := range []string{"timeout", "unavailable", "query_failed"} {
		key := "logs_export.reason." + reason
		assert.NotEqual(t, key, i18n.Translate(i18n.LangRU, key))
	}
}
