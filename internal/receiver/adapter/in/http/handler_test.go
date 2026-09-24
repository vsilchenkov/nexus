package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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

	// newCtx — контекст запроса; resolved — узел найден, и наблюдатель резолва
	// положил его метки (§101.3: другого источника меток нет).
	newCtx := func(resolved bool) (*gin.Context, *httptest.ResponseRecorder) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/vika/telephony", nil)
		if resolved {
			c.Set(metrics.NodeLabelKey, "telephony")
			c.Set(metrics.TeamLabelKey, "vika")
		}
		return c, w
	}
	h := &Handler{metrics: metrics.New("receiver"), logger: logging.NewNoop()}

	t.Run("node not found hides the client-supplied path", func(t *testing.T) {
		t.Parallel()
		c, w := newCtx(false)
		h.replyDomainError(c, domain.ErrNodeNotFound, "telephony", "test")
		require.Equal(t, http.StatusNotFound, w.Code)
		assert.Equal(t, string(domain.RejectReasonNodeNotFound), c.GetString(RejectReasonKey))
		assert.Equal(t, metrics.NodeUnresolved, c.GetString(metrics.NodeLabelKey))
		// §101.3: у <unresolved> команды нет — иначе ряд 404 делился бы по слогам.
		assert.Empty(t, c.GetString(metrics.TeamLabelKey))
	})

	t.Run("found node answering not found keeps its label", func(t *testing.T) {
		t.Parallel()
		// §101.3: узел найден, но наружу «узла нет» (не тот тип за sync-адресом,
		// pull-узел по короткому адресу) — ряд ограничен числом узлов.
		c, w := newCtx(true)
		h.replyDomainError(c, domain.ErrNodeNotFound, "telephony", "test")
		require.Equal(t, http.StatusNotFound, w.Code)
		assert.Equal(t, string(domain.RejectReasonNodeNotFound), c.GetString(RejectReasonKey))
		assert.Equal(t, "telephony", c.GetString(metrics.NodeLabelKey))
		assert.Equal(t, "vika", c.GetString(metrics.TeamLabelKey))
	})

	t.Run("existing node keeps its label", func(t *testing.T) {
		t.Parallel()
		// §82.3: узел есть, просто дёрнули не тот эндпоинт — путь в метке
		// настоящий, и ряд ограничен числом узлов.
		c, w := newCtx(true)
		h.replyAsyncError(c, domain.ErrNodeNotAsyncIngress, "telephony", "test")
		require.Equal(t, http.StatusNotFound, w.Code)
		assert.Equal(t, string(domain.RejectReasonNodeNotFound), c.GetString(RejectReasonKey))
		assert.Equal(t, "telephony", c.GetString(metrics.NodeLabelKey))
		assert.Equal(t, "vika", c.GetString(metrics.TeamLabelKey))
	})

	t.Run("bus failure is not a client reject", func(t *testing.T) {
		t.Parallel()
		c, w := newCtx(true)
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

// TestReplyClientGone — §109.4 (Sentry NEXUS-S): запрос, который оборвал сам
// клиент, получает 499, строку лога Debug и не попадает в журнал отказов §94;
// раньше — 502 «internal routing error» и ERROR в Sentry. Отмена при ЖИВОМ
// контексте запроса и другие ошибки при ушедшем клиенте остаются прежними.
func TestReplyClientGone(t *testing.T) {
	t.Parallel()

	canceledSend := fmt.Errorf("sender.Send: %w", context.Canceled)
	cases := []struct {
		name       string
		clientGone bool
		err        error
		async      bool
		wantStatus int
		wantError  bool // строка уровня ERROR (уходит в Sentry)
		wantReject bool
	}{
		{name: "sync: клиент ушёл", clientGone: true, err: canceledSend, wantStatus: statusClientClosedRequest},
		{name: "async: клиент ушёл", clientGone: true, err: canceledSend, async: true, wantStatus: statusClientClosedRequest},
		{name: "клиент ушёл, но ошибка другая → прежний 502", clientGone: true, err: assert.AnError, wantStatus: http.StatusBadGateway, wantError: true},
		{name: "отмена при живом запросе → прежний 502", err: canceledSend, wantStatus: http.StatusBadGateway, wantError: true},
		{name: "клиент ушёл во время поиска узла (PG/Redis) → 499", clientGone: true, err: fmt.Errorf("lookup: %w", context.Canceled), wantStatus: statusClientClosedRequest},
		{name: "404 при живом запросе → в журнал", err: domain.ErrNodeNotFound, wantStatus: http.StatusNotFound, wantReject: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var logBuf bytes.Buffer
			logger := logging.NewLogger(slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
			h := &Handler{logger: logger}

			req := httptest.NewRequest(http.MethodPost, "/api/v1/vika/telephony", nil)
			sink, m, w := runReject(t, req, func(c *gin.Context) {
				ctx, cancel := context.WithCancel(c.Request.Context())
				defer cancel()
				if tc.clientGone {
					cancel()
				}
				c.Request = c.Request.WithContext(ctx)
				if tc.async {
					h.replyAsyncError(c, tc.err, "telephony", "test")
				} else {
					h.replyDomainError(c, tc.err, "telephony", "test")
				}
			})

			assert.Equal(t, tc.wantStatus, w.Code)
			assert.Equal(t, tc.wantError, strings.Contains(logBuf.String(), `"level":"ERROR"`), logBuf.String())
			if tc.wantReject {
				assert.Len(t, sink.samples, 1)
				return
			}
			assert.Empty(t, sink.samples, "в журнал отказов не пишется")
			assert.Empty(t, m.calls)
		})
	}
}

// TestReplySenderUnavailable — §109.3 (Sentry NEXUS-T/S): Sender не поднялся
// за ready_wait_ms — клиент получает 503 + Retry-After (вызов не отправлялся,
// повтор безопасен), строка лога WARN вместо ERROR, в журнал отказов §94 не
// пишется. Раньше — 502 «internal routing error» и событие Sentry на каждый
// запрос окна выката.
func TestReplySenderUnavailable(t *testing.T) {
	t.Parallel()

	var logBuf bytes.Buffer
	logger := logging.NewLogger(slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	h := &Handler{logger: logger}

	err := fmt.Errorf("sender.Send: %w: rpc error: code = Unavailable desc = connection refused", domain.ErrSenderUnavailable)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/vika/telephony", nil)
	sink, m, w := runReject(t, req, func(c *gin.Context) {
		h.replyDomainError(c, err, "telephony", "receiver.sync")
	})

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Equal(t, "5", w.Header().Get("Retry-After"))
	assert.JSONEq(t, `{"error":"sender unavailable"}`, w.Body.String())
	assert.Contains(t, logBuf.String(), `"level":"WARN"`)
	assert.NotContains(t, logBuf.String(), `"level":"ERROR"`)
	assert.Empty(t, sink.samples, "сбой шины — не отказ клиенту")
	assert.Empty(t, m.calls)
}

// TestReplyClientGone_NoPathSecretInLog — ревизия §109: клиент ушёл во время
// поиска узла, метки резолва ещё нет, и путь целиком задал он сам. У
// passthrough-адреса за путём узла идёт токен бота — в debug-строке его быть не
// должно (§51.9); остаётся первый сегмент, как в error-строке (§101.13).
func TestReplyClientGone_NoPathSecretInLog(t *testing.T) {
	t.Parallel()

	const secret = "AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw_"
	var logBuf bytes.Buffer
	logger := logging.NewLogger(slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	h := &Handler{logger: logger}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/telegram/bot1:"+secret+"/getUpdates", nil)
	_, _, w := runReject(t, req, func(c *gin.Context) {
		ctx, cancel := context.WithCancel(c.Request.Context())
		cancel()
		c.Request = c.Request.WithContext(ctx)
		h.replyDomainError(c, fmt.Errorf("lookup: %w", context.Canceled), "telegram/bot1:"+secret+"/getUpdates", "test")
	})

	assert.Equal(t, statusClientClosedRequest, w.Code)
	assert.Contains(t, logBuf.String(), "canceled by client")
	assert.NotContains(t, logBuf.String(), secret)
}

// TestReplyDomainError_NotFoundMasksBotToken — ревизия §109 (стенд): отказ до
// резолва (404 на адрес с токеном) пишет в debug путь из адреса — он нужен для
// разбора «почему 404», но токен бота в нём маскируется (§51.9).
func TestReplyDomainError_NotFoundMasksBotToken(t *testing.T) {
	t.Parallel()

	const secret = "AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw_"
	var logBuf bytes.Buffer
	logger := logging.NewLogger(slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	h := &Handler{logger: logger}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/nope/bot1:"+secret+"/getUpdates", nil)
	_, _, w := runReject(t, req, func(c *gin.Context) {
		h.replyDomainError(c, domain.ErrNodeNotFound, "nope/bot1:"+secret+"/getUpdates", "test")
	})

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, logBuf.String(), "receiver request rejected")
	assert.Contains(t, logBuf.String(), "nope/bot1:***/getUpdates", "форма пути для разбора 404 остаётся")
	assert.NotContains(t, logBuf.String(), secret)
}
