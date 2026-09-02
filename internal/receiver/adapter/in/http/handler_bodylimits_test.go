package http

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/receiver/usecase"
	senderv1 "nexus/proto/sender/v1"
)

// Разделение лимитов тела: sync (max_body_bytes) и async (max_async_body_bytes).
// Sync идёт напрямую по gRPC и принимает большие тела; async уезжает в Kafka в
// base64-конверте, поэтому его потолок ниже. До разделения лимит был один, и
// поднять sync было нельзя, не сломав публикацию в топик.

func bodyLimitsNode(path string, root domain.RootMethod, status domain.NodeStatus) *domain.Node {
	return &domain.Node{
		Path:             path,
		RootMethod:       root,
		Status:           status,
		URLMode:          domain.URLModeStatic,
		TargetURL:        "http://upstream.local/hook",
		IncomingMethod:   domain.HTTPMethodAny,
		OutgoingMethod:   domain.HTTPMethodPOST,
		AuthType:         domain.AuthTypeNone,
		IncomingAuthType: domain.IncomingAuthTypeNone,
	}
}

// newBodyLimitsHandler собирает Handler с обеими ветками поверх одного узла.
func newBodyLimitsHandler(t *testing.T, node *domain.Node, maxBody, maxAsyncBody int) (*gin.Engine, *asyncStubQueue) {
	t.Helper()
	// Ключ reader'а — "<team>|<path>"; у legacy-URL без слога команды team пуст.
	reader := shortURLReader{nodes: map[string]*domain.Node{"|" + node.Path: node}}
	sender := syncStubSender{resp: &senderv1.SendResponse{
		StatusCode: http.StatusOK,
		Body:       []byte(`{"ok":true}`),
		Headers:    map[string]string{"Content-Type": "application/json"},
	}}
	queue := &asyncStubQueue{}
	logger := logging.NewNoop()
	h := New(
		usecase.NewRouteUsecase(reader, sender, 5, logger),
		usecase.NewRouteAsyncUsecase(reader, queue, "nexus.async", 5, logger),
		usecase.NewBodyLimitsProvider(maxBody, maxAsyncBody, logger), nil, logger,
	)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h.Register(r)
	return r, queue
}

func TestBodyLimits_SyncAcceptsWhatAsyncRejects(t *testing.T) {
	t.Parallel()

	const (
		maxBody      = 100
		maxAsyncBody = 10
	)
	body := strings.Repeat("X", 50) // между лимитами: sync можно, async нельзя

	t.Run("sync принимает тело больше async-лимита", func(t *testing.T) {
		t.Parallel()
		r, _ := newBodyLimitsHandler(t,
			bodyLimitsNode("demo", domain.RootMethodRequest, domain.NodeStatusEnabled),
			maxBody, maxAsyncBody)

		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/request/demo", strings.NewReader(body)))
		require.Equal(t, http.StatusOK, w.Code, "sync ограничен только max_body_bytes")
	})

	t.Run("async отклоняет то же тело", func(t *testing.T) {
		t.Parallel()
		r, queue := newBodyLimitsHandler(t,
			bodyLimitsNode("demo", domain.RootMethodRequestAsync, domain.NodeStatusEnabled),
			maxBody, maxAsyncBody)

		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/requestAsync/demo", strings.NewReader(body)))
		require.Equal(t, http.StatusRequestEntityTooLarge, w.Code, "async ограничен max_async_body_bytes")
		assert.Zero(t, queue.published, "отклонённое тело не должно попадать в очередь")
	})

	t.Run("callback отклоняет то же тело", func(t *testing.T) {
		t.Parallel()
		node := bodyLimitsNode("demo", domain.RootMethodRequestAsync, domain.NodeStatusEnabled)
		r, queue := newBodyLimitsHandler(t, node, maxBody, maxAsyncBody)

		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/callback/demo", strings.NewReader(body)))
		require.Equal(t, http.StatusRequestEntityTooLarge, w.Code, "callback уезжает в ту же очередь")
		assert.Zero(t, queue.published)
	})

	t.Run("async принимает тело в пределах своего лимита", func(t *testing.T) {
		t.Parallel()
		r, queue := newBodyLimitsHandler(t,
			bodyLimitsNode("demo", domain.RootMethodRequestAsync, domain.NodeStatusEnabled),
			maxBody, maxAsyncBody)

		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/requestAsync/demo", strings.NewReader("XXXXX")))
		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, 1, queue.published, "тело в пределах лимита должно уехать в очередь")
	})
}

// §3.6: sync-запрос к paused-узлу уходит в очередь, поэтому к нему применяется
// АСИНХРОННЫЙ потолок. Без этой проверки клиент получал бы 202 «принято», а
// сообщение отвергалось бы брокером при публикации — потеря без следа.
func TestBodyLimits_PausedNodeUsesAsyncLimit(t *testing.T) {
	t.Parallel()

	const (
		maxBody      = 100
		maxAsyncBody = 10
	)

	t.Run("тело больше async-лимита → 413, в очередь не уходит", func(t *testing.T) {
		t.Parallel()
		r, queue := newBodyLimitsHandler(t,
			bodyLimitsNode("demo", domain.RootMethodRequest, domain.NodeStatusPaused),
			maxBody, maxAsyncBody)

		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/request/demo",
			strings.NewReader(strings.Repeat("X", 50))))

		require.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
		assert.Contains(t, w.Body.String(), "paused", "причина отказа — состояние узла, а не размер сам по себе")
		assert.Zero(t, queue.published)
	})

	t.Run("тело в пределах async-лимита → 202 queued", func(t *testing.T) {
		t.Parallel()
		r, queue := newBodyLimitsHandler(t,
			bodyLimitsNode("demo", domain.RootMethodRequest, domain.NodeStatusPaused),
			maxBody, maxAsyncBody)

		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/request/demo", strings.NewReader("XXXXX")))

		require.Equal(t, http.StatusAccepted, w.Code, "§3.6: paused-узел ставит запрос в очередь")
		assert.Equal(t, 1, queue.published)
	})
}

// Провайдер страхует от конфигурации, где async-потолок выше sync-потолка или
// не задан вовсе: в обоих случаях действует один общий потолок (прежнее
// поведение конструктора до §97).
func TestBodyLimits_AsyncLimitFallsBackToSync(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		maxAsync   int
		wantLimits int
	}{
		{name: "не задан", maxAsync: 0, wantLimits: 100},
		{name: "отрицательный", maxAsync: -1, wantLimits: 100},
		{name: "больше sync-лимита", maxAsync: 500, wantLimits: 100},
		{name: "меньше sync-лимита", maxAsync: 10, wantLimits: 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := usecase.NewBodyLimitsProvider(100, tc.maxAsync, logging.NewNoop())
			assert.Equal(t, tc.wantLimits, p.Async())
		})
	}
}
