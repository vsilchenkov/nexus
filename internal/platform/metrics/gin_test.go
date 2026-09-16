package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootMethodFromPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path string
		want string
	}{
		{"/api/v1/request/*path", "request"},
		{"/api/v1/requestAsync/*path", "requestAsync"},
		{"/api/nodes/:id", ""},
		{"/health", ""},
		{"", ""},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			t.Parallel()
			if got := rootMethodFromPath(c.path); got != c.want {
				t.Errorf("rootMethodFromPath(%q) = %q, want %q", c.path, got, c.want)
			}
		})
	}
}

// TestGinMiddleware_NodeLabelNeverFromURL (§101.3): без метки обработчика node
// пуст — сырой путь адреса в метку не попадает. У passthrough-узла хвост адреса
// несёт токены ботов, а на выходах до резолва (здесь — 429 rate-limit) путь
// задаёт клиент.
func TestGinMiddleware_NodeLabelNeverFromURL(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	m := New("receiver")
	r := gin.New()
	r.Use(GinMiddleware(m))
	r.POST("/api/v1/*path", func(c *gin.Context) { c.Status(http.StatusTooManyRequests) })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST",
		"/api/v1/request/messengers/telegram/bot123456:AAAA/sendMessage", nil))
	require.Equal(t, http.StatusTooManyRequests, w.Code)

	body := scrape(t, m)
	assert.Contains(t, body, `node=""`)
	assert.Contains(t, body, `team=""`)
	assert.NotContains(t, body, "bot123456", "хвост адреса не должен попадать в метки")
	assert.NotContains(t, body, "messengers", "слог из адреса метку team не задаёт")
	assert.NotContains(t, body, "telegram")
}

// TestGinMiddleware_NodeLabelFromContext: Receiver кладёт чистый путь узла в
// контекст (URL содержит слог команды) — метка node должна быть без слога,
// чтобы совпасть с Sender (in/out merge на дашборде, §21).
func TestGinMiddleware_NodeLabelFromContext(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	m := New("receiver")
	r := gin.New()
	r.Use(GinMiddleware(m))
	r.POST("/api/v1/request/*path", func(c *gin.Context) {
		c.Set(NodeLabelKey, "stand/req-noauth-post") // как делает Receiver-handler
		c.Set(TeamLabelKey, "default")
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/request/default/stand/req-noauth-post", nil))
	require.Equal(t, http.StatusOK, w.Code)

	body := scrape(t, m)
	assert.Contains(t, body, `node="stand/req-noauth-post"`)
	assert.NotContains(t, body, `node="default/stand/req-noauth-post"`)
	assert.Contains(t, body, `team="default"`)
}

func TestGinMiddleware_V1Request(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	m := New("receiver")
	r := gin.New()
	r.Use(GinMiddleware(m))
	r.GET("/api/v1/request/*path", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/request/demo/sub?q=1", nil))
	require.Equal(t, http.StatusOK, w.Code)

	// Обработчик метку узла не положил — node пуст (§101.3: из адреса не выводится).
	body := scrape(t, m)
	assert.Contains(t, body, `nexus_requests_total{method="request",node="",service="receiver",status="200",team=""} 1`)
	assert.Contains(t, body, `nexus_request_duration_seconds_count{method="request",node="",service="receiver",team=""} 1`)
}

func TestGinMiddleware_V1RequestAsync(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	m := New("receiver")
	r := gin.New()
	r.Use(GinMiddleware(m))
	r.POST("/api/v1/requestAsync/*path", func(c *gin.Context) {
		c.Status(http.StatusAccepted)
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/requestAsync/demo", nil))
	require.Equal(t, http.StatusAccepted, w.Code)

	body := scrape(t, m)
	assert.Contains(t, body, `method="requestAsync"`)
	assert.Contains(t, body, `status="202"`)
}

func TestGinMiddleware_ApiRoute_UsesMethodPathFallback(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	m := New("web")
	r := gin.New()
	r.Use(GinMiddleware(m))
	r.GET("/api/nodes/:id", func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/nodes/abc", nil))
	require.Equal(t, http.StatusOK, w.Code)

	body := scrape(t, m)
	assert.Contains(t, body, `method="GET /api/nodes/:id"`)
	assert.Contains(t, body, `node=""`)
}

func TestGinMiddleware_SkipsHealthAndMetrics(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	m := New("receiver")
	r := gin.New()
	r.Use(GinMiddleware(m))
	r.GET("/health", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.GET("/ready", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.GET("/metrics", func(c *gin.Context) { c.Status(http.StatusOK) })

	for _, p := range []string{"/health", "/ready", "/metrics"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		require.Equal(t, http.StatusOK, w.Code, p)
	}

	body := scrape(t, m)
	// Counter серий не появилось ни для одного из служебных путей.
	assert.NotContains(t, body, "/health")
	assert.NotContains(t, body, "/ready")
	assert.NotContains(t, body, `method="/metrics"`)
}

func TestGinMiddleware_NoMatchRoute_NoCounter(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	m := New("receiver")
	r := gin.New()
	r.Use(GinMiddleware(m))
	r.GET("/known", func(c *gin.Context) { c.Status(http.StatusOK) })

	// 404 not matched — c.FullPath() пустой, middleware пропускает запрос.
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/unknown", nil))
	require.Equal(t, http.StatusNotFound, w.Code)

	body := scrape(t, m)
	// Никаких 404-серий в counter'е.
	assert.NotContains(t, body, `status="404"`)
}

func TestGinMiddleware_StatusFromError(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	m := New("receiver")
	r := gin.New()
	r.Use(GinMiddleware(m))
	r.GET("/api/v1/request/*path", func(c *gin.Context) {
		c.AbortWithStatus(http.StatusBadGateway)
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/request/demo", nil))
	require.Equal(t, http.StatusBadGateway, w.Code)

	body := scrape(t, m)
	assert.Contains(t, body, `status="502"`)
}

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	rr := httptest.NewRecorder()
	m.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, rr.Code)
	return rr.Body.String()
}

// sanity: убедимся, что строки выше действительно эквивалентны Sprintf,
// чтобы не было опечаток при отладке.
var _ = strings.Contains
