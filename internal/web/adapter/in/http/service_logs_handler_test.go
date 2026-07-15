package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
)

// stubServiceLogReader — фейк порт-ридера для handler-тестов.
type stubServiceLogReader struct {
	entries map[string][]domain.ServiceLogEntry
}

func (s *stubServiceLogReader) Tail(_ context.Context, service string, limit int) ([]domain.ServiceLogEntry, error) {
	out := s.entries[service]
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func newServiceLogsRouter(t *testing.T, reader *stubServiceLogReader) *gin.Engine {
	t.Helper()
	uc := usecase.NewServiceLogsUsecase(reader, logging.NewNoop())
	h := NewServiceLogsHandler(uc, logging.NewNoop())
	r := gin.New()
	r.GET("/api/logs", h.List)
	r.GET("/api/logs/download", h.Download)
	return r
}

func serviceLogsFixture() *stubServiceLogReader {
	base := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	return &stubServiceLogReader{entries: map[string][]domain.ServiceLogEntry{
		"receiver": {{TS: base.Add(2 * time.Second), Level: "info", Service: "receiver", Msg: "r-msg"}},
		"sender": {{TS: base.Add(3 * time.Second), Level: "warn", Service: "sender", Msg: "s-msg",
			Attrs: map[string]any{"node": "n1"}}},
		"web": {{TS: base.Add(1 * time.Second), Level: "error", Service: "web", Msg: "w-msg"}},
	}}
}

func doServiceLogs(t *testing.T, r *gin.Engine, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func TestServiceLogsHandler_Get_OK(t *testing.T) {
	t.Parallel()
	r := newServiceLogsRouter(t, serviceLogsFixture())

	w := doServiceLogs(t, r, "/api/logs")

	require.Equal(t, http.StatusOK, w.Code)
	var got []domain.ServiceLogEntry
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got, 3)
	assert.Equal(t, "s-msg", got[0].Msg, "freshest first")
	assert.Equal(t, "sender", got[0].Service)
	assert.Equal(t, "warn", got[0].Level)
	assert.Equal(t, "n1", got[0].Attrs["node"])
}

func TestServiceLogsHandler_Get_ServiceAndLevelFilters(t *testing.T) {
	t.Parallel()
	r := newServiceLogsRouter(t, serviceLogsFixture())

	w := doServiceLogs(t, r, "/api/logs?service=sender")
	require.Equal(t, http.StatusOK, w.Code)
	var got []domain.ServiceLogEntry
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got, 1)
	assert.Equal(t, "sender", got[0].Service)

	w = doServiceLogs(t, r, "/api/logs?min_level=error")
	require.Equal(t, http.StatusOK, w.Code)
	got = nil
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got, 1)
	assert.Equal(t, "w-msg", got[0].Msg)
}

func TestServiceLogsHandler_Get_EmptyIsJSONArray(t *testing.T) {
	t.Parallel()
	r := newServiceLogsRouter(t, &stubServiceLogReader{})

	w := doServiceLogs(t, r, "/api/logs")

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "[]", strings.TrimSpace(w.Body.String()), "empty tail must be [], not null")
}

func TestServiceLogsHandler_Get_BadParams(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, path string
	}{
		{"unknown service", "/api/logs?service=oops"},
		{"invalid min_level", "/api/logs?min_level=verbose"},
		{"non-numeric limit", "/api/logs?limit=abc"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newServiceLogsRouter(t, serviceLogsFixture())
			w := doServiceLogs(t, r, tc.path)
			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	}
}

func TestServiceLogsHandler_Download_HeadersAndBody(t *testing.T) {
	t.Parallel()
	fixture := serviceLogsFixture()
	r := newServiceLogsRouter(t, fixture)

	w := doServiceLogs(t, r, "/api/logs/download?service=sender")

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "text/plain; charset=utf-8", w.Header().Get("Content-Type"))
	cd := w.Header().Get("Content-Disposition")
	assert.Contains(t, cd, "attachment")
	assert.Contains(t, cd, "nexus-logs-sender-")
	assert.Contains(t, cd, ".log")
	// Защита от header-инъекции: имя файла проходит safeFilePart.
	assert.NotContains(t, cd, "\r")
	assert.NotContains(t, cd, "\n")
	assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))

	// Тело — построчно в формате FormatServiceLogLine (хронологический порядок).
	want := FormatServiceLogLine(fixture.entries["sender"][0]) + "\n"
	assert.Equal(t, want, w.Body.String())
}

func TestServiceLogsHandler_Download_FilenameSanitized(t *testing.T) {
	t.Parallel()
	r := newServiceLogsRouter(t, serviceLogsFixture())

	// Невалидный сервис не дойдёт до имени файла (400), а вот csv-значение
	// с валидными сервисами внутри имени должно быть безопасным.
	w := doServiceLogs(t, r, "/api/logs/download?service=receiver,web")
	require.Equal(t, http.StatusOK, w.Code)
	cd := w.Header().Get("Content-Disposition")
	assert.Contains(t, cd, "nexus-logs-receiver-web-")
}

func TestServiceLogsHandler_Download_ChronologicalOrder(t *testing.T) {
	t.Parallel()
	r := newServiceLogsRouter(t, serviceLogsFixture())

	w := doServiceLogs(t, r, "/api/logs/download")

	require.Equal(t, http.StatusOK, w.Code)
	lines := strings.Split(strings.TrimSpace(w.Body.String()), "\n")
	require.Len(t, lines, 3)
	assert.Contains(t, lines[0], "w-msg", "file starts with the oldest entry")
	assert.Contains(t, lines[2], "s-msg", "file ends with the freshest entry")
}
