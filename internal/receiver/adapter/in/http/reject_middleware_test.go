package http

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/receiver/adapter/out/rejectlog"
)

// captureSink — приёмник журнала для тестов.
type captureSink struct{ samples []rejectlog.Sample }

func (s *captureSink) Add(x rejectlog.Sample) { s.samples = append(s.samples, x) }

// captureMetrics — счётчик отказов для тестов.
type captureMetrics struct{ calls [][2]string }

func (m *captureMetrics) IncIngressRejected(reason, status string) {
	m.calls = append(m.calls, [2]string{reason, status})
}

// runReject прогоняет один запрос через цепочку с журналом отказов.
// handler имитирует боевой обработчик: ставит статус и, если задана причина, —
// её код в контексте.
func runReject(t *testing.T, req *http.Request, handler gin.HandlerFunc) (*captureSink, *captureMetrics, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	sink := &captureSink{}
	m := &captureMetrics{}

	r := gin.New()
	r.Use(RejectLogMiddleware(sink, m))
	r.Any("/api/v1/*path", handler)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return sink, m, w
}

func rejectWith(status int, reason domain.RejectReason) gin.HandlerFunc {
	return func(c *gin.Context) {
		if reason != "" {
			SetRejectReason(c, reason)
		}
		c.JSON(status, gin.H{"error": "nope"})
	}
}

// TestRejectLogMiddleware_RecordsDomainReason: причина берётся из контекста, а
// не из статуса. Ключевой случай — 503 «узел выключен»: по коду он неотличим
// от сбоя шины, и без причины запись не появилась бы вовсе (§94.2).
func TestRejectLogMiddleware_RecordsDomainReason(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/vika/telephony", nil)
	req.Header.Set("User-Agent", "axios/1.6")
	sink, m, w := runReject(t, req, rejectWith(http.StatusServiceUnavailable, domain.RejectReasonNodeDisabled))

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Len(t, sink.samples, 1)
	s := sink.samples[0]
	assert.Equal(t, domain.RejectReasonNodeDisabled, s.Key.Reason)
	assert.Equal(t, "vika", s.Key.TeamSlug)
	assert.Equal(t, "telephony", s.Key.NodePath)
	assert.Equal(t, http.MethodPost, s.Key.HTTPMethod)
	assert.Equal(t, int32(http.StatusServiceUnavailable), s.Status)
	assert.Equal(t, "axios/1.6", s.UserAgent)
	require.Len(t, m.calls, 1)
	// Слога команды в метке нет: при 404 его задаёт клиент (§94.8).
	assert.Equal(t, [2]string{"node_disabled", "503"}, m.calls[0])
}

// TestRejectLogMiddleware_FallsBackToStatus: отказы, вернувшиеся не из
// доменного слоя (413 из чтения тела, 429 из лимита), причины в контексте не
// несут — она выводится из статуса.
func TestRejectLogMiddleware_FallsBackToStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
		want   domain.RejectReason
	}{
		{name: "413 body too large", status: http.StatusRequestEntityTooLarge, want: domain.RejectReasonBodyTooLarge},
		{name: "429 rate limited", status: http.StatusTooManyRequests, want: domain.RejectReasonRateLimited},
		{name: "508 loop", status: http.StatusLoopDetected, want: domain.RejectReasonLoopDetected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/vika/telephony", nil)
			sink, _, _ := runReject(t, req, rejectWith(tt.status, ""))
			require.Len(t, sink.samples, 1)
			assert.Equal(t, tt.want, sink.samples[0].Key.Reason)
		})
	}
}

// TestRejectLogMiddleware_IgnoresSuccessAndBusFailures: журнал ведётся про
// клиентов, а не про шину — 2xx и 5xx (кроме размеченных обработчиком) в него
// не попадают.
func TestRejectLogMiddleware_IgnoresSuccessAndBusFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
	}{
		{name: "200 ok", status: http.StatusOK},
		{name: "202 queued", status: http.StatusAccepted},
		{name: "502 bus failure", status: http.StatusBadGateway},
		{name: "500 bus failure", status: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/vika/telephony", nil)
			sink, m, _ := runReject(t, req, rejectWith(tt.status, ""))
			assert.Empty(t, sink.samples)
			assert.Empty(t, m.calls)
		})
	}
}

// TestRejectLogMiddleware_MasksQuery: имена query-параметров сохраняются,
// значения — нет. Именно там ездят токены авторизации §41 (source=query).
func TestRejectLogMiddleware_MasksQuery(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/vika/telephony?token=super-secret&id=42", nil)
	sink, _, _ := runReject(t, req, rejectWith(http.StatusNotFound, domain.RejectReasonNodeNotFound))

	require.Len(t, sink.samples, 1)
	got := sink.samples[0].RawPath
	assert.Equal(t, "/api/v1/vika/telephony?id=***&token=***", got)
	assert.NotContains(t, got, "super-secret")
	assert.NotContains(t, got, "42")
}

// TestRejectLogMiddleware_HeaderWhitelist: в сэмпл попадает только отобранный
// список, значение авторизации заменяется схемой, чужие заголовки не сохраняются.
func TestRejectLogMiddleware_HeaderWhitelist(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/vika/telephony", nil)
	req.Header.Set("User-Agent", "curl/8.4")
	req.Header.Set("Authorization", "Bearer super-secret-token")
	req.Header.Set("X-Api-Key", "another-secret")
	req.Header.Set("Content-Type", "application/json")
	sink, _, _ := runReject(t, req, rejectWith(http.StatusUnauthorized, domain.RejectReasonUnauthorized))

	require.Len(t, sink.samples, 1)
	h := sink.samples[0].Headers
	assert.Equal(t, "curl/8.4", h["User-Agent"])
	assert.Equal(t, "application/json", h["Content-Type"])
	assert.Equal(t, "Bearer ***", h["Authorization"], "значение токена не сохраняется")
	assert.NotContains(t, h, "X-Api-Key", "заголовки вне белого списка не сохраняются")
	for _, v := range h {
		assert.NotContains(t, v, "super-secret-token")
		assert.NotContains(t, v, "another-secret")
	}
}

// TestRejectLogMiddleware_ShortFormTeam: короткая форма адреса §78.1 (без
// слога) относится к команде default — иначе один узел давал бы две группы.
func TestRejectLogMiddleware_ShortFormTeam(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/telephony", nil)
	sink, m, _ := runReject(t, req, rejectWith(http.StatusNotFound, domain.RejectReasonNodeNotFound))

	require.Len(t, sink.samples, 1)
	assert.Equal(t, domain.DefaultTeamSlug, sink.samples[0].Key.TeamSlug)
	assert.Equal(t, "telephony", sink.samples[0].Key.NodePath)
	require.Len(t, m.calls, 1)
}

// TestRejectLogMiddleware_LegacyVerbForm: форма с сегментом метода
// (/request/...) даёт тот же путь узла, что короткая, — иначе один узел
// разъезжался бы по двум группам.
func TestRejectLogMiddleware_LegacyVerbForm(t *testing.T) {
	t.Parallel()

	for _, path := range []string{
		"/api/v1/request/vika/telephony",
		"/api/v1/requestAsync/vika/telephony",
		"/api/v1/vika/telephony",
	} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		sink, _, _ := runReject(t, req, rejectWith(http.StatusNotFound, domain.RejectReasonNodeNotFound))
		require.Len(t, sink.samples, 1, path)
		assert.Equal(t, "vika", sink.samples[0].Key.TeamSlug, path)
		assert.Equal(t, "telephony", sink.samples[0].Key.NodePath, path)
	}
}

// TestRejectLogMiddleware_MetricsWithoutSink: выключенный журнал не выключает
// метрику — иначе выключение сбора делало бы шину слепой целиком.
func TestRejectLogMiddleware_MetricsWithoutSink(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	m := &captureMetrics{}
	r := gin.New()
	r.Use(RejectLogMiddleware(nil, m))
	r.Any("/api/v1/*path", rejectWith(http.StatusNotFound, domain.RejectReasonNodeNotFound))

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/vika/telephony", nil))

	require.Equal(t, http.StatusNotFound, w.Code)
	require.Len(t, m.calls, 1)
	assert.Equal(t, "node_not_found", m.calls[0][0])
}

// TestRejectLogMiddleware_UnknownReasonInContext: чужое значение в контексте не
// уезжает в БД как есть (там CHECK на множество причин) и не теряется —
// становится "other".
func TestRejectLogMiddleware_UnknownReasonInContext(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/vika/telephony", nil)
	sink, _, _ := runReject(t, req, func(c *gin.Context) {
		c.Set(RejectReasonKey, "выдуманная причина")
		c.JSON(http.StatusNotFound, gin.H{"error": "nope"})
	})

	require.Len(t, sink.samples, 1)
	assert.Equal(t, domain.RejectReasonOther, sink.samples[0].Key.Reason)
}

// TestRejectLogMiddleware_IgnoresProxiedUpstreamStatus: ответ, ПРОБРОШЕННЫЙ от
// внешней системы, в журнал отказов не попадает — какой бы у него ни был код.
//
// Найдено на бою 23.08.2026: узлы с path_passthrough отдают клиенту ответ
// апстрима как есть, и штатный 400 от Telegram («message is not modified») или
// Green API оседал в журнале как отказ шины. Шина при этом отработала
// правильно: запрос дошёл, ответ доставлен и уже записан в лог узла.
func TestRejectLogMiddleware_IgnoresProxiedUpstreamStatus(t *testing.T) {
	t.Parallel()

	for _, status := range []int{http.StatusBadRequest, http.StatusNotFound, http.StatusConflict} {
		req := httptest.NewRequest(http.MethodPost,
			"/api/v1/messengers/telegram/bot123/editMessageReplyMarkup", nil)
		sink, m, w := runReject(t, req, func(c *gin.Context) {
			MarkProxiedResponse(c)
			c.Data(status, "application/json", []byte(`{"ok":false}`))
		})

		assert.Equal(t, status, w.Code, "статус апстрима доходит до клиента без изменений")
		assert.Empty(t, sink.samples, "проброшенный ответ %d — не отказ шины", status)
		assert.Empty(t, m.calls, "и метрику отказов он тоже не двигает")
	}
}

// TestRejectLogMiddleware_ProxiedDoesNotHideRealReason: пометка проброса НЕ
// перебивает явную причину. Порядок зафиксирован тестом, чтобы правило не
// зависело от того, в каком порядке обработчик расставил вызовы.
func TestRejectLogMiddleware_ProxiedDoesNotHideRealReason(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/messengers/telegram/x", nil)
	sink, _, _ := runReject(t, req, func(c *gin.Context) {
		SetRejectReason(c, domain.RejectReasonUnauthorized)
		MarkProxiedResponse(c)
		c.Status(http.StatusUnauthorized)
	})

	require.Len(t, sink.samples, 1, "названная обработчиком причина главнее пометки проброса")
	assert.Equal(t, domain.RejectReasonUnauthorized, sink.samples[0].Key.Reason)
}
