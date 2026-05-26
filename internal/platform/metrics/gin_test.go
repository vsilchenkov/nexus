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
		{"/v1/request/*path", "request"},
		{"/v1/requestAsync/*path", "requestAsync"},
		{"/api/nodes/:id", ""},
		{"/health", ""},
		{"", ""},
	}
	for _, c := range cases {
		c := c
		t.Run(c.path, func(t *testing.T) {
			t.Parallel()
			if got := rootMethodFromPath(c.path); got != c.want {
				t.Errorf("rootMethodFromPath(%q) = %q, want %q", c.path, got, c.want)
			}
		})
	}
}

func TestNodePathFromGin(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name  string
		param string
		want  string
	}{
		{"with leading slash", "/demo/path", "demo/path"},
		{"no leading slash", "demo", "demo"},
		{"empty", "", ""},
		{"single slash", "/", ""},
		{"nested deep", "/a/b/c", "a/b/c"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Params = gin.Params{{Key: "path", Value: c.param}}
			if got := nodePathFromGin(ctx); got != c.want {
				t.Errorf("nodePathFromGin(%q) = %q, want %q", c.param, got, c.want)
			}
		})
	}
}

func TestGinMiddleware_V1Request(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	m := New("receiver")
	r := gin.New()
	r.Use(GinMiddleware(m))
	r.GET("/v1/request/*path", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/v1/request/demo/sub?q=1", nil))
	require.Equal(t, http.StatusOK, w.Code)

	body := scrape(t, m)
	assert.Contains(t, body, `databus_requests_total{method="request",node="demo/sub",service="receiver",status="200"} 1`)
	assert.Contains(t, body, `databus_request_duration_seconds_count{method="request",node="demo/sub",service="receiver"} 1`)
}

func TestGinMiddleware_V1RequestAsync(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	m := New("receiver")
	r := gin.New()
	r.Use(GinMiddleware(m))
	r.POST("/v1/requestAsync/*path", func(c *gin.Context) {
		c.Status(http.StatusAccepted)
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/v1/requestAsync/demo", nil))
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
	r.GET("/v1/request/*path", func(c *gin.Context) {
		c.AbortWithStatus(http.StatusBadGateway)
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/v1/request/demo", nil))
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
