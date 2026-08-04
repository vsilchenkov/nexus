package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/metrics"
	"nexus/internal/receiver/usecase"
	senderv1 "nexus/proto/sender/v1"
)

// TestSplitVerb — контракт разбора ведущего сегмента-метода (§78.2). Остаток
// обязан выглядеть ровно так, как его отдавал catch-all каждого из трёх прежних
// маршрутов: с ведущим '/' и без сегмента метода.
func TestSplitVerb(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		raw      string
		wantVerb string
		wantRest string
	}{
		{"legacy sync", "/request/acme/order", "request", "/acme/order"},
		{"legacy async", "/requestAsync/acme/order", "requestAsync", "/acme/order"},
		{"legacy callback", "/callback/acme/order", "callback", "/acme/order"},
		{"legacy без слога", "/request/order", "request", "/order"},
		{"legacy без пути", "/request", "request", "/"},
		{"короткий адрес", "/webhook/sbp-qr", "", "/webhook/sbp-qr"},
		{"короткий адрес без слога", "/sbp-qr", "", "/sbp-qr"},
		// Регистр значим: сегменты-методы писались ровно так в прежних маршрутах.
		{"regex-регистр не метод", "/requestasync/x", "", "/requestasync/x"},
		{"узел с похожим префиксом", "/requests/x", "", "/requests/x"},
		{"пусто", "", "", ""},
		{"только слеш", "/", "", "/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotVerb, gotRest := SplitVerb(tc.raw)
			assert.Equal(t, tc.wantVerb, gotVerb, "verb")
			assert.Equal(t, tc.wantRest, gotRest, "rest")
		})
	}
}

// shortURLReader — NodeReader, отдающий узел только по точному (team, path).
type shortURLReader struct{ nodes map[string]*domain.Node }

func (r shortURLReader) Get(_ context.Context, team, path string) (*domain.Node, error) {
	if n, ok := r.nodes[team+"|"+path]; ok {
		return n, nil
	}
	return nil, domain.ErrNodeNotFound
}

// asyncStubQueue — usecase.AsyncProducer: принимает сообщение, ничего не делая.
type asyncStubQueue struct{ published int }

func (q *asyncStubQueue) Produce(_ context.Context, _, _ string, _ []byte, _ map[string]string) error {
	q.published++
	return nil
}

func shortURLNode(path string, root domain.RootMethod) *domain.Node {
	return &domain.Node{
		Path:             path,
		RootMethod:       root,
		Status:           domain.NodeStatusEnabled,
		URLMode:          domain.URLModeStatic,
		TargetURL:        "http://upstream.local/hook",
		IncomingMethod:   domain.HTTPMethodAny,
		OutgoingMethod:   domain.HTTPMethodPOST,
		AuthType:         domain.AuthTypeNone,
		IncomingAuthType: domain.IncomingAuthTypeNone,
	}
}

// newShortURLHandler собирает Handler с обеими ветками маршрутизации поверх
// заданного набора узлов.
func newShortURLHandler(t *testing.T, nodes map[string]*domain.Node, m *metrics.Metrics) (*gin.Engine, *asyncStubQueue) {
	t.Helper()
	reader := shortURLReader{nodes: nodes}
	sender := syncStubSender{resp: &senderv1.SendResponse{
		StatusCode: http.StatusOK,
		Body:       []byte(`{"ok":true}`),
		Headers:    map[string]string{"Content-Type": "application/json"},
	}}
	queue := &asyncStubQueue{}
	logger := logging.NewNoop()
	route := usecase.NewRouteUsecase(reader, sender, 5, logger)
	routeAsync := usecase.NewRouteAsyncUsecase(reader, queue, "nexus.async", 5, logger)

	h := New(route, routeAsync, 0, m, logger)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	if m != nil {
		r.Use(metrics.GinMiddleware(m))
	}
	h.Register(r)
	return r, queue
}

// TestShortURL_DispatchesByRootMethod (§78.1) — адрес без сегмента метода ведёт
// запрос по ветке, заданной root_method узла, а legacy-адреса продолжают
// работать ровно как раньше.
func TestShortURL_DispatchesByRootMethod(t *testing.T) {
	nodes := map[string]*domain.Node{
		"webhook|sbp-qr":       shortURLNode("sbp-qr", domain.RootMethodRequest),
		"webhook|sbp-qr-async": shortURLNode("sbp-qr-async", domain.RootMethodRequestAsync),
		"webhook|pull":         shortURLNode("pull", domain.RootMethodRabbitMQAsync),
		"|solo":                shortURLNode("solo", domain.RootMethodRequest),
	}

	t.Run("sync-узел по короткому адресу отдаёт ответ получателя", func(t *testing.T) {
		r, _ := newShortURLHandler(t, nodes, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/webhook/sbp-qr", strings.NewReader(`{}`)))
		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, `{"ok":true}`, w.Body.String())
	})

	t.Run("async-узел по короткому адресу ставится в очередь", func(t *testing.T) {
		r, queue := newShortURLHandler(t, nodes, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/webhook/sbp-qr-async", strings.NewReader(`{}`)))
		require.Equal(t, http.StatusOK, w.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		assert.Equal(t, true, body["result"])
		assert.NotEmpty(t, body["id"])
		assert.Equal(t, 1, queue.published, "сообщение обязано уйти в Kafka")
	})

	t.Run("короткий адрес без слога резолвится в команду default", func(t *testing.T) {
		r, _ := newShortURLHandler(t, nodes, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/solo", strings.NewReader(`{}`)))
		require.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("pull-узел по короткому адресу — 404, факт существования не раскрыт", func(t *testing.T) {
		r, _ := newShortURLHandler(t, nodes, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/webhook/pull", strings.NewReader(`{}`)))
		require.Equal(t, http.StatusNotFound, w.Code)
		assert.Contains(t, w.Body.String(), "node not found")
	})

	t.Run("несуществующий узел по короткому адресу — 404", func(t *testing.T) {
		r, _ := newShortURLHandler(t, nodes, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/webhook/nope", strings.NewReader(`{}`)))
		require.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("legacy sync-адрес работает", func(t *testing.T) {
		r, _ := newShortURLHandler(t, nodes, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/request/webhook/sbp-qr", strings.NewReader(`{}`)))
		require.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("legacy sync-адрес async-узла по-прежнему 404", func(t *testing.T) {
		// Проверка root_method у legacy-формы сохранена (§78.1): метод задан URL.
		r, _ := newShortURLHandler(t, nodes, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/request/webhook/sbp-qr-async", strings.NewReader(`{}`)))
		require.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("legacy async-адрес sync-узла по-прежнему принимается", func(t *testing.T) {
		// На этом стоит §3.6 (paused sync → async), поведение не меняем.
		r, queue := newShortURLHandler(t, nodes, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/requestAsync/webhook/sbp-qr", strings.NewReader(`{}`)))
		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, 1, queue.published)
	})

	t.Run("пустой путь — 400", func(t *testing.T) {
		r, _ := newShortURLHandler(t, nodes, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/", strings.NewReader(`{}`)))
		require.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("запрос без префикса /api/v1 — прежняя подсказка", func(t *testing.T) {
		r, _ := newShortURLHandler(t, nodes, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/request/webhook/sbp-qr", nil))
		require.Equal(t, http.StatusNotFound, w.Code)
		assert.Contains(t, w.Body.String(), "API version required")
	})
}

// TestShortURL_PausedSyncNodeQueues (§3.6) — sync-узел на паузе и по короткому
// адресу отвечает 202 queued, как и по legacy-адресу.
func TestShortURL_PausedSyncNodeQueues(t *testing.T) {
	paused := shortURLNode("sbp-qr", domain.RootMethodRequest)
	paused.Status = domain.NodeStatusPaused
	r, queue := newShortURLHandler(t, map[string]*domain.Node{"webhook|sbp-qr": paused}, nil)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/webhook/sbp-qr", strings.NewReader(`{}`)))

	require.Equal(t, http.StatusAccepted, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, true, body["queued"])
	assert.Equal(t, string(domain.NodeStatusPaused), body["node_status"])
	assert.Equal(t, 1, queue.published)
}

// TestShortURL_DisabledNodeNotFound — отключённый узел неотличим от
// несуществующего и по короткому адресу (§78.1).
func TestShortURL_DisabledNodeNotFound(t *testing.T) {
	off := shortURLNode("sbp-qr", domain.RootMethodRequest)
	off.Status = domain.NodeStatusDisabled
	r, _ := newShortURLHandler(t, map[string]*domain.Node{"webhook|sbp-qr": off}, nil)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/webhook/sbp-qr", strings.NewReader(`{}`)))
	require.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), "node not found")
}

// TestCallback_NonPostReturns405 (§78.2) — callback остаётся POST-only. До §78
// другой метод падал в NoRoute (404); теперь отвечаем честным 405.
func TestCallback_NonPostReturns405(t *testing.T) {
	r, _ := newShortURLHandler(t, map[string]*domain.Node{
		"webhook|hook": shortURLNode("hook", domain.RootMethodRequestAsync),
	}, nil)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/callback/webhook/hook", nil))
	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

// TestShortURL_MetricsLabelsMatchLegacy (§78.2) — метка method не зависит от
// формы адреса: смена формы не должна разводить один и тот же трафик по разным
// рядам Prometheus (на method="requestAsync" стоит дашборд Kafka, на "request" —
// алерт латентности).
func TestShortURL_MetricsLabelsMatchLegacy(t *testing.T) {
	nodes := map[string]*domain.Node{
		"webhook|sbp-qr":       shortURLNode("sbp-qr", domain.RootMethodRequest),
		"webhook|sbp-qr-async": shortURLNode("sbp-qr-async", domain.RootMethodRequestAsync),
	}
	cases := []struct {
		name       string
		url        string
		wantMethod string
		wantNode   string
	}{
		{"короткий sync", "/api/v1/webhook/sbp-qr", "request", "sbp-qr"},
		{"legacy sync", "/api/v1/request/webhook/sbp-qr", "request", "sbp-qr"},
		{"короткий async", "/api/v1/webhook/sbp-qr-async", "requestAsync", "sbp-qr-async"},
		{"legacy async", "/api/v1/requestAsync/webhook/sbp-qr-async", "requestAsync", "sbp-qr-async"},
		{"callback", "/api/v1/callback/webhook/sbp-qr-async", metrics.RootMethodCallback, "sbp-qr-async"},
		// Мусорные обращения по короткому адресу не подмешиваются в боевые ряды.
		{"неизвестный узел", "/api/v1/webhook/nope", metrics.RootMethodShortURL, "nope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := metrics.New("receiver")
			r, _ := newShortURLHandler(t, nodes, m)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, tc.url, strings.NewReader(`{}`)))

			found := metricLabelPairs(t, m, tc.wantMethod, tc.wantNode)
			assert.True(t, found, "ожидался ряд nexus_requests_total{method=%q,node=%q}", tc.wantMethod, tc.wantNode)
		})
	}
}

// metricLabelPairs — есть ли в nexus_requests_total ряд с такими method/node.
func metricLabelPairs(t *testing.T, m *metrics.Metrics, method, node string) bool {
	t.Helper()
	families, err := m.Registry().Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() != "nexus_requests_total" {
			continue
		}
		for _, metric := range f.GetMetric() {
			var gotMethod, gotNode string
			for _, l := range metric.GetLabel() {
				switch l.GetName() {
				case "method":
					gotMethod = l.GetValue()
				case "node":
					gotNode = l.GetValue()
				}
			}
			if gotMethod == method && gotNode == node {
				return true
			}
		}
	}
	return false
}

// TestRateLimitKey_SameForBothURLForms (§78.2) — ключ лимита не зависит от формы
// адреса. Иначе клиент удваивал бы квоту простой сменой формы.
func TestRateLimitKey_SameForBothURLForms(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rl := &recordingLimiter{}
	mw := RateLimitMiddleware(rl, 10, logging.NewNoop())

	r := gin.New()
	r.Use(mw)
	r.Any("/api/v1/*path", func(c *gin.Context) { c.Status(http.StatusOK) })

	for _, u := range []string{
		"/api/v1/request/webhook/sbp-qr",
		"/api/v1/requestAsync/webhook/sbp-qr",
		"/api/v1/callback/webhook/sbp-qr",
		"/api/v1/webhook/sbp-qr",
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, u, nil))
	}

	require.Len(t, rl.keys, 4)
	for _, k := range rl.keys {
		assert.Equal(t, "webhook/sbp-qr", k, "ключ лимита обязан быть путём узла без сегмента метода")
	}
}

// recordingLimiter запоминает ключи, с которыми его звали.
type recordingLimiter struct{ keys []string }

func (l *recordingLimiter) Allow(_ context.Context, key string, _ int) (bool, error) {
	l.keys = append(l.keys, key)
	return true, nil
}
