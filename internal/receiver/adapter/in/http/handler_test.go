package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/metrics"
	"nexus/internal/receiver/usecase"
	senderv1 "nexus/proto/sender/v1"
)

// TestSplitTeamSlugAndPath фиксирует контракт парсинга URL Receiver'а
// (Phase 10.E.1): /api/v1/request/<team_slug>/<node_path> для multi-tenancy
// и /api/v1/request/<node_path> для legacy default-team.
//
// Cross-team изоляция в Receiver работает на уровне БД: NodeReader.Get
// делает SELECT через JOIN с teams (WHERE slug=$1 AND path=$2). Если
// клиент указал чужой team_slug, узел не найдётся — вернётся
// ErrNodeNotFound (404), а не утечка существования узла другой команды.
func TestSplitTeamSlugAndPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		raw          string
		wantTeamSlug string
		wantNodePath string
	}{
		// Catch-all gin всегда передаёт строку с ведущим '/'.
		{"multi-tenant simple", "/acme/order", "acme", "order"},
		{"multi-tenant nested", "/acme/order/v2", "acme", "order/v2"},
		{"multi-tenant deep", "/globex/foo/bar/baz", "globex", "foo/bar/baz"},
		{"legacy single segment", "/order", "", "order"},
		{"legacy with subpath", "/order_root", "", "order_root"},
		// Trim ведущего '/' и пустой ввод — конкретный handler потом
		// вернёт 400 на пустой node_path.
		{"empty raw", "", "", ""},
		{"only slash", "/", "", ""},
		// Без ведущего '/' (на всякий случай — Gin его всегда даёт, но
		// функция остаётся robust):
		{"no leading slash", "acme/order", "acme", "order"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotTeam, gotPath := splitTeamSlugAndPath(tc.raw)
			assert.Equal(t, tc.wantTeamSlug, gotTeam, "team_slug mismatch")
			assert.Equal(t, tc.wantNodePath, gotPath, "node_path mismatch")
		})
	}
}

// TestClassifyDomainError фиксирует маппинг доменных ошибок в HTTP-коды и коды
// причин журнала отказов (§3, #5/#7; §94.2) — общий для sync и async ответов.
//
// Причина проверяется здесь же, а не отдельным тестом: она возвращается тем же
// switch'ем, и разъехавшиеся код с причиной означали бы, что журнал называет
// отказ не тем, чем он был.
func TestClassifyDomainError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		err          error
		wantStatus   int
		wantReason   domain.RejectReason
		wantInternal bool
	}{
		{"not found", domain.ErrNodeNotFound, http.StatusNotFound, domain.RejectReasonNodeNotFound, false},
		{"async ingress gate", domain.ErrNodeNotAsyncIngress, http.StatusNotFound, domain.RejectReasonNodeNotFound, false},
		// §94.2: выключенный узел наружу неотличим от несуществующего (тот же
		// 404 и тот же текст), а причина в журнале — своя.
		{"disabled", domain.ErrNodeDisabled, http.StatusNotFound, domain.RejectReasonNodeDisabled, false},
		{"method not allowed", domain.ErrNodeMethodNotAllowed, http.StatusMethodNotAllowed, domain.RejectReasonMethodNotAllowed, false},
		{"url param required", domain.ErrURLParamRequired, http.StatusBadRequest, domain.RejectReasonBadRequest, false},
		{"url invalid", domain.ErrURLInvalid, http.StatusBadRequest, domain.RejectReasonBadRequest, false},
		{"ack render failed", domain.ErrAckRenderFailed, http.StatusBadRequest, domain.RejectReasonBadRequest, false},
		{"url not allowed", domain.ErrURLNotAllowed, http.StatusForbidden, domain.RejectReasonURLNotAllowed, false},
		{"loop detected", domain.ErrLoopDetected, http.StatusLoopDetected, domain.RejectReasonLoopDetected, false},
		{"unauthorized", domain.ErrUnauthorized, http.StatusUnauthorized, domain.RejectReasonUnauthorized, false},
		{"unknown -> 502 internal", assert.AnError, http.StatusBadGateway, domain.RejectReasonOther, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			status, msg, reason, internal := classifyDomainError(tc.err)
			assert.Equal(t, tc.wantStatus, status)
			assert.Equal(t, tc.wantReason, reason)
			assert.Equal(t, tc.wantInternal, internal)
			assert.NotEmpty(t, msg)
		})
	}
}

// TestReplyDomainError_MarksReject: отказ размечается для журнала §94 —
// причина уходит в контекст, а метка узла подменяется маркером, когда узла
// не существует.
//
// Подмена метки — защита кардинальности nexus_requests_total: путь при 404
// задаёт клиент, и без маркера сканер по случайным адресам плодил бы ряд на
// каждую попытку (§94.8).
func TestReplyDomainError_MarksReject(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	newCtx := func() (*gin.Context, *httptest.ResponseRecorder) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/vika/telephony", nil)
		c.Set(metrics.NodeLabelKey, "telephony")
		return c, w
	}
	h := &Handler{metrics: metrics.New("receiver"), logger: logging.NewNoop()}

	t.Run("node not found hides the client-supplied path", func(t *testing.T) {
		t.Parallel()
		c, w := newCtx()
		h.replyDomainError(c, domain.ErrNodeNotFound, "telephony", "test")
		require.Equal(t, http.StatusNotFound, w.Code)
		assert.Equal(t, string(domain.RejectReasonNodeNotFound), c.GetString(RejectReasonKey))
		assert.Equal(t, metrics.NodeUnresolved, c.GetString(metrics.NodeLabelKey))
	})

	t.Run("existing node keeps its label", func(t *testing.T) {
		t.Parallel()
		// §82.3: узел есть, просто дёрнули не тот эндпоинт — путь в метке
		// настоящий, и ряд ограничен числом узлов.
		c, w := newCtx()
		h.replyAsyncError(c, domain.ErrNodeNotAsyncIngress, "telephony", "test")
		require.Equal(t, http.StatusNotFound, w.Code)
		assert.Equal(t, string(domain.RejectReasonNodeNotFound), c.GetString(RejectReasonKey))
		assert.Equal(t, "telephony", c.GetString(metrics.NodeLabelKey))
	})

	t.Run("bus failure is not a client reject", func(t *testing.T) {
		t.Parallel()
		c, w := newCtx()
		h.replyDomainError(c, assert.AnError, "telephony", "test")
		require.Equal(t, http.StatusBadGateway, w.Code)
		assert.Empty(t, c.GetString(RejectReasonKey), "502 в журнал отказов не попадает")
		assert.Equal(t, "telephony", c.GetString(metrics.NodeLabelKey))
	})
}

// TestReplyDomainError_Loop: sync-ответ на петлю — 508 + инкремент
// nexus_loop_detected_total{mode="sync"} (§32).
func TestReplyDomainError_Loop(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	m := metrics.New("receiver")
	h := &Handler{metrics: m, logger: logging.NewNoop()}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/request/demo", nil)
	c.Request.Header.Set("X-Nexus-Hops", "5")

	h.replyDomainError(c, domain.ErrLoopDetected, "demo", "test")

	require.Equal(t, http.StatusLoopDetected, w.Code)
	assert.Equal(t, float64(1), testutil.ToFloat64(m.LoopDetectedTotal.WithLabelValues("sync")))
}

// TestReplyAsyncError_BodyShape: async-ошибка отвечает {"result":false,"message":...}
// (§3, #7), а не {"error":...} как sync-вариант.
func TestReplyAsyncError_BodyShape(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	h := &Handler{logger: logging.NewNoop()}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/requestAsync/demo", nil)

	h.replyAsyncError(c, domain.ErrNodeNotFound, "demo", "test")

	require.Equal(t, http.StatusNotFound, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, false, body["result"])
	assert.Equal(t, "node not found", body["message"])
	_, hasError := body["error"]
	assert.False(t, hasError, "async error body must not use the sync 'error' key")
}

// syncStubReader — port.NodeReader для sync-теста: всегда отдаёт заданный узел.
type syncStubReader struct{ node *domain.Node }

func (r syncStubReader) Get(_ context.Context, _, _ string) (*domain.Node, error) {
	return r.node, nil
}

// syncStubSender — usecase.SenderClient: возвращает заранее заданный ответ
// внешнего узла (тело + заголовки + статус), имитируя Sender.
type syncStubSender struct{ resp *senderv1.SendResponse }

func (s syncStubSender) Send(_ context.Context, _ *senderv1.SendRequest) (*senderv1.SendResponse, error) {
	return s.resp, nil
}

// TestHandleSync_ForwardsResponseBodyAndHeaders фиксирует сквозной проброс
// ОТВЕТА внешнего узла клиенту: тело, заголовки и статус возвращаются как есть;
// hop-by-hop-заголовки (Connection и т.п.) вырезаются (§3.1, #6).
func TestHandleSync_ForwardsResponseBodyAndHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)

	node := &domain.Node{
		Path:             "demo",
		RootMethod:       domain.RootMethodRequest,
		Status:           domain.NodeStatusEnabled,
		URLMode:          domain.URLModeStatic,
		TargetURL:        "http://upstream.local/hook",
		IncomingMethod:   domain.HTTPMethodPOST,
		OutgoingMethod:   domain.HTTPMethodPOST,
		AuthType:         domain.AuthTypeNone,
		IncomingAuthType: domain.IncomingAuthTypeNone,
	}
	sender := syncStubSender{resp: &senderv1.SendResponse{
		StatusCode: http.StatusCreated,
		Body:       []byte(`{"token":"abc","ok":true}`),
		Headers: map[string]string{
			"Content-Type":   "application/json",
			"X-Echo":         "1",
			"X-Custom-Resp":  "vovo",
			"Connection":     "keep-alive", // hop-by-hop → должен быть вырезан
			"Content-Length": "25",         // hop-by-hop в нашем смысле? нет — но gin перезапишет
		},
	}}
	route := usecase.NewRouteUsecase(syncStubReader{node: node}, sender, 5, logging.NewNoop())
	h := New(route, nil, usecase.NewBodyLimitsProvider(0, 0, logging.NewNoop()), nil, logging.NewNoop())

	r := gin.New()
	h.Register(r)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/request/demo", strings.NewReader(`{"q":1}`))
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code, "статус ответа внешнего узла должен проброситься")
	assert.Equal(t, `{"token":"abc","ok":true}`, w.Body.String(), "ТЕЛО ответа должно проброситься клиенту дословно")
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
	assert.Equal(t, "1", w.Header().Get("X-Echo"), "заголовок ответа узла должен проброситься")
	assert.Equal(t, "vovo", w.Header().Get("X-Custom-Resp"), "произвольный заголовок ответа должен проброситься")
	assert.Empty(t, w.Header().Get("Connection"), "hop-by-hop заголовок должен быть вырезан")
}

// §43-rev: тело запроса больше receiver.max_body_bytes → 413 (не 400, не 502).
// readBody общий для sync/async/callback — проверяем sync и async.
func TestHandle_BodyTooLarge_Returns413(t *testing.T) {
	gin.SetMode(gin.TestMode)
	node := &domain.Node{
		Path: "demo", RootMethod: domain.RootMethodRequest, Status: domain.NodeStatusEnabled,
		URLMode: domain.URLModeStatic, TargetURL: "http://upstream.local/hook",
		IncomingMethod: domain.HTTPMethodPOST, OutgoingMethod: domain.HTTPMethodPOST,
		AuthType: domain.AuthTypeNone, IncomingAuthType: domain.IncomingAuthTypeNone,
	}
	route := usecase.NewRouteUsecase(syncStubReader{node: node},
		syncStubSender{resp: &senderv1.SendResponse{StatusCode: 200}}, 5, logging.NewNoop())
	h := New(route, nil, usecase.NewBodyLimitsProvider(8, 8, logging.NewNoop()), nil, logging.NewNoop()) // max_body_bytes = 8
	r := gin.New()
	h.Register(r)

	big := strings.NewReader(strings.Repeat("X", 100)) // 100 > 8
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/request/demo", big))
	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code, "sync: тело > max_body_bytes → 413")
}

// readBody возвращает sentinel errBodyTooLarge при превышении; replyReadBodyError
// мапит его в 413, прочие ошибки — в 400.
func TestReadBody_LimitAndErrorMapping(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	// под лимитом — ок.
	c1, _ := gin.CreateTestContext(httptest.NewRecorder())
	c1.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader("hello"))
	body, err := readBody(c1, 10)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(body))

	// больше лимита (Content-Length известен) — ранний sentinel.
	c2, _ := gin.CreateTestContext(httptest.NewRecorder())
	c2.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("x", 50)))
	_, err = readBody(c2, 10)
	require.ErrorIs(t, err, errBodyTooLarge)

	// chunked / без Content-Length: пред-проверка по CL не срабатывает, ловим по
	// факту чтения (LimitReader + дренаж остатка).
	c2b, _ := gin.CreateTestContext(httptest.NewRecorder())
	c2b.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("x", 50)))
	c2b.Request.ContentLength = -1
	_, err = readBody(c2b, 10)
	require.ErrorIs(t, err, errBodyTooLarge)

	// маппинг: sentinel → 413.
	w := httptest.NewRecorder()
	c3, _ := gin.CreateTestContext(w)
	c3.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	replyReadBodyError(c3, errBodyTooLarge)
	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
}
