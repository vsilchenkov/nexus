package http

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
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
)

// secretTail — хвост адреса passthrough-узла с токеном бота: ровно такие ряды
// §101.1 нашёл в боевом Prometheus.
const secretTail = "bot123456:AAAA"

// ingressLabels — метки узла, с которыми запрос дошёл до конца цепочки.
type ingressLabels struct{ node, team string }

// captureLabels — middleware группы /api/v1: после обработчика запоминает метки
// узла из контекста — те, что возьмёт metrics.GinMiddleware.
func captureLabels(got *ingressLabels) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
		got.node = c.GetString(metrics.NodeLabelKey)
		got.team = c.GetString(metrics.TeamLabelKey)
	}
}

// requestNodeLabels — значения метки node у всех рядов nexus_requests_total.
func requestNodeLabels(t *testing.T, m *metrics.Metrics) []string {
	t.Helper()
	families, err := m.Registry().Gather()
	require.NoError(t, err)
	var nodes []string
	for _, f := range families {
		if f.GetName() != "nexus_requests_total" {
			continue
		}
		for _, metric := range f.GetMetric() {
			for _, l := range metric.GetLabel() {
				if l.GetName() == "node" {
					nodes = append(nodes, l.GetValue())
				}
			}
		}
	}
	return nodes
}

// assertNoLabelContains — ни одна метка ни одной метрики не содержит needle.
func assertNoLabelContains(t *testing.T, m *metrics.Metrics, needle string) {
	t.Helper()
	families, err := m.Registry().Gather()
	require.NoError(t, err)
	for _, f := range families {
		for _, metric := range f.GetMetric() {
			for _, l := range metric.GetLabel() {
				assert.NotContains(t, l.GetValue(), needle, "%s{%s}", f.GetName(), l.GetName())
			}
		}
	}
}

// denyLimiter — rate-limiter, отказывающий всем.
type denyLimiter struct{}

func (denyLimiter) Allow(context.Context, string, int) (bool, error) { return false, nil }

// TestIngress_NodeLabelsFromResolvedNode (§101.3) — метки узла берутся из
// НАЙДЕННОГО узла, а не из адреса. До §101 Receiver клал в метку путь из адреса
// до резолва: хвост passthrough-адреса (токен бота) оседал в TSDB, а у
// legacy-адреса без слога метка теряла первый сегмент пути.
func TestIngress_NodeLabelsFromResolvedNode(t *testing.T) {
	tg := shortURLNode("tg", domain.RootMethodRequest)
	tg.PathPassthrough = true
	off := shortURLNode("off", domain.RootMethodRequest)
	off.Status = domain.NodeStatusDisabled
	paused := shortURLNode("p", domain.RootMethodRequest)
	paused.Status = domain.NodeStatusPaused
	nodes := map[string]*domain.Node{
		"webhook|tg":   tg,
		"webhook|q":    shortURLNode("q", domain.RootMethodRequestAsync),
		"webhook|off":  off,
		"webhook|p":    paused,
		"webhook|pull": shortURLNode("pull", domain.RootMethodRabbitMQAsync),
		"|a/b":         shortURLNode("a/b", domain.RootMethodRequest),
		"|solo":        shortURLNode("solo", domain.RootMethodRequest),
	}
	cases := []struct {
		name       string
		method     string
		url        string
		wantStatus int
		want       ingressLabels
	}{
		{"passthrough: хвост с токеном в метку не попадает", http.MethodPost,
			"/api/v1/request/webhook/tg/" + secretTail + "/sendMessage", http.StatusOK, ingressLabels{"tg", "webhook"}},
		{"passthrough по короткому адресу", http.MethodPost,
			"/api/v1/webhook/tg/" + secretTail + "/getUpdates", http.StatusOK, ingressLabels{"tg", "webhook"}},
		{"legacy-адрес: первый сегмент оказался частью пути", http.MethodPost,
			"/api/v1/request/a/b", http.StatusOK, ingressLabels{"a/b", domain.DefaultTeamSlug}},
		{"короткий адрес без слога", http.MethodPost,
			"/api/v1/solo", http.StatusOK, ingressLabels{"solo", domain.DefaultTeamSlug}},
		{"async", http.MethodPost,
			"/api/v1/requestAsync/webhook/q", http.StatusOK, ingressLabels{"q", "webhook"}},
		{"paused sync уходит в очередь (§3.6)", http.MethodPost,
			"/api/v1/request/webhook/p", http.StatusAccepted, ingressLabels{"p", "webhook"}},
		{"выключенный узел: наружу 404, но узел найден", http.MethodPost,
			"/api/v1/webhook/off", http.StatusNotFound, ingressLabels{"off", "webhook"}},
		{"sync-адрес async-узла: наружу 404, но узел найден", http.MethodPost,
			"/api/v1/request/webhook/q", http.StatusNotFound, ingressLabels{"q", "webhook"}},
		{"pull-узел по короткому адресу: наружу 404, но узел найден", http.MethodPost,
			"/api/v1/webhook/pull", http.StatusNotFound, ingressLabels{"pull", "webhook"}},
		{"несуществующий узел", http.MethodPost,
			"/api/v1/request/webhook/nope/" + secretTail, http.StatusNotFound, ingressLabels{metrics.NodeUnresolved, ""}},
		{"callback не-POST: 405 до резолва", http.MethodGet,
			"/api/v1/callback/webhook/tg/" + secretTail, http.StatusMethodNotAllowed, ingressLabels{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := metrics.New("receiver")
			var got ingressLabels
			r, _ := newIngressHandler(t, nodes, m, nil, captureLabels(&got))

			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(tc.method, tc.url, strings.NewReader(`{}`)))
			require.Equal(t, tc.wantStatus, w.Code, w.Body.String())

			assert.Equal(t, tc.want, got)
			assert.Equal(t, []string{tc.want.node}, requestNodeLabels(t, m))
			assertNoLabelContains(t, m, secretTail)
		})
	}
}

// TestIngress_NodeLabelsEmptyBeforeResolve (§101.3) — отказ до резолва не несёт
// меток узла вовсе: путь в этот момент задан клиентом и может быть чем угодно.
func TestIngress_NodeLabelsEmptyBeforeResolve(t *testing.T) {
	tg := shortURLNode("tg", domain.RootMethodRequest)
	tg.PathPassthrough = true
	nodes := map[string]*domain.Node{"webhook|tg": tg}
	url := "/api/v1/request/webhook/tg/" + secretTail + "/sendMessage"

	t.Run("413: тело больше лимита", func(t *testing.T) {
		m := metrics.New("receiver")
		var got ingressLabels
		limits := usecase.NewBodyLimitsProvider(16, 16, logging.NewNoop())
		r, _ := newIngressHandler(t, nodes, m, limits, captureLabels(&got))

		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, url, strings.NewReader(strings.Repeat("x", 64))))
		require.Equal(t, http.StatusRequestEntityTooLarge, w.Code)

		assert.Equal(t, ingressLabels{}, got)
		assert.Equal(t, []string{""}, requestNodeLabels(t, m))
		assertNoLabelContains(t, m, secretTail)
	})

	t.Run("429: rate-limit отказал до обработчика", func(t *testing.T) {
		m := metrics.New("receiver")
		var got ingressLabels
		r, _ := newIngressHandler(t, nodes, m, nil,
			captureLabels(&got), RateLimitMiddleware(denyLimiter{}, 10, logging.NewNoop()))

		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, url, strings.NewReader(`{}`)))
		require.Equal(t, http.StatusTooManyRequests, w.Code)

		assert.Equal(t, ingressLabels{}, got)
		assert.Equal(t, []string{""}, requestNodeLabels(t, m))
		assertNoLabelContains(t, m, secretTail)
	})
}

// failingReader — NodeReader при лежащем хранилище конфигурации узлов.
type failingReader struct{}

func (failingReader) Get(context.Context, string, string) (*domain.Node, error) {
	return nil, errors.New("redis: connection refused")
}

// TestIngress_InternalErrorLogWithoutURLTail (§101.13) — сбой резолва (502)
// пишется error-строкой, а она уходит и в Sentry. Узел не найден, путь задал
// клиент, и хвост passthrough-адреса с токеном бота в строку не попадает.
func TestIngress_InternalErrorLogWithoutURLTail(t *testing.T) {
	for _, url := range []string{
		"/api/v1/request/webhook/tg/" + secretTail + "/sendMessage",
		"/api/v1/requestAsync/webhook/tg/" + secretTail + "/sendMessage",
		"/api/v1/webhook/tg/" + secretTail + "/getUpdates",
	} {
		t.Run(url, func(t *testing.T) {
			var buf bytes.Buffer
			logger := logging.NewLogger(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError})))
			route := usecase.NewRouteUsecase(failingReader{}, syncStubSender{}, 5, logger)
			routeAsync := usecase.NewRouteAsyncUsecase(failingReader{}, &asyncStubQueue{}, "nexus.async", 5, logger)
			h := New(route, routeAsync, usecase.NewBodyLimitsProvider(0, 0, logger), nil, logger)
			gin.SetMode(gin.TestMode)
			r := gin.New()
			h.Register(r)

			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, url, strings.NewReader(`{}`)))
			require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())

			require.Contains(t, buf.String(), `"node":"tg/…"`, "первый сегмент пути остаётся в строке")
			assert.NotContains(t, buf.String(), secretTail)
		})
	}
}
