package sentry

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/getsentry/sentry-go"
	"github.com/gin-gonic/gin"

	"nexus/internal/platform/metrics"
)

// TestHttpStatusToSpanStatus — мэппинг HTTP-кодов на SpanStatus (§14.3).
func TestHttpStatusToSpanStatus(t *testing.T) {
	t.Parallel()
	cases := map[int]sentry.SpanStatus{
		200: sentry.SpanStatusOK,
		204: sentry.SpanStatusOK,
		401: sentry.SpanStatusUnauthenticated,
		403: sentry.SpanStatusPermissionDenied,
		404: sentry.SpanStatusNotFound,
		429: sentry.SpanStatusResourceExhausted,
		400: sentry.SpanStatusInvalidArgument,
		418: sentry.SpanStatusInvalidArgument,
		500: sentry.SpanStatusInternalError,
		503: sentry.SpanStatusInternalError,
	}
	for code, want := range cases {
		if got := httpStatusToSpanStatus(code); got != want {
			t.Errorf("code %d: got %v, want %v", code, got, want)
		}
	}
}

// TestGinMiddleware_NoSentry — middleware не падает, когда Sentry не
// инициализирован (Init не вызывался). Запрос корректно обрабатывается.
func TestGinMiddleware_NoSentry(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(GinMiddleware("test"))
	r.GET("/api/v1/request/*path", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/request/demo/path?q=1", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
}

// TestSkipSentry — инфра-пути исключаются из Sentry-трейсинга, прочие — нет.
func TestSkipSentry(t *testing.T) {
	t.Parallel()
	skip := []string{"", "/metrics", "/health", "/ready"}
	for _, p := range skip {
		if !skipSentry(p) {
			t.Errorf("skipSentry(%q) = false, want true", p)
		}
	}
	keep := []string{"/api/v1/request/*path", "/api/users", "/api/teams", "/api/v1/requestAsync/*path"}
	for _, p := range keep {
		if skipSentry(p) {
			t.Errorf("skipSentry(%q) = true, want false", p)
		}
	}
}

// TestGinMiddleware_SkipsInfraPaths — для /metrics транзакция не стартует
// (нет шума в Sentry от скрейпов Prometheus), а для прикладного маршрута —
// стартует. Запросы при этом отрабатывают нормально.
func TestGinMiddleware_SkipsInfraPaths(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	var metricsHadTx, apiHadTx bool
	r := gin.New()
	r.Use(GinMiddleware("test"))
	r.GET("/metrics", func(c *gin.Context) {
		metricsHadTx = sentry.TransactionFromContext(c.Request.Context()) != nil
		c.Status(http.StatusOK)
	})
	r.GET("/api/v1/request/*path", func(c *gin.Context) {
		apiHadTx = sentry.TransactionFromContext(c.Request.Context()) != nil
		c.Status(http.StatusOK)
	})

	for _, p := range []string{"/metrics", "/api/v1/request/demo"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d", p, w.Code)
		}
	}

	if metricsHadTx {
		t.Error("/metrics НЕ должен стартовать Sentry-транзакцию")
	}
	if !apiHadTx {
		t.Error("/api/v1/request должен стартовать Sentry-транзакцию")
	}
}

// TestRootMethod — §78.2: теги node/root_method берутся из того, что положил
// обработчик. Выводить их из имени маршрута больше нельзя: у боевого трафика
// Receiver'а он один на все формы адреса (/api/v1/*path), а сырой path-параметр
// содержит ещё и сегмент метода.
func TestRootMethod(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.GET("/api/v1/request/*path", func(c *gin.Context) {
		c.Set(metrics.RootMethodLabelKey, "request")
		c.Set(metrics.NodeLabelKey, "demo")
		if got := rootMethod(c); got != "request" {
			t.Errorf("/api/v1/request: got %q", got)
		}
		if got := nodePathFromGin(c); got != "demo" {
			t.Errorf("/api/v1/request node: got %q, want путь узла без сегмента метода", got)
		}
		c.Status(200)
	})
	r.GET("/api/v1/requestAsync/*path", func(c *gin.Context) {
		c.Set(metrics.RootMethodLabelKey, "requestAsync")
		if got := rootMethod(c); got != "requestAsync" {
			t.Errorf("/api/v1/requestAsync: got %q", got)
		}
		// Без метки в контексте — фолбэк на сырой параметр (прежнее поведение).
		if got := nodePathFromGin(c); got != "x" {
			t.Errorf("/api/v1/requestAsync node fallback: got %q", got)
		}
		c.Status(200)
	})
	r.GET("/health", func(c *gin.Context) {
		if got := rootMethod(c); got != "" {
			t.Errorf("/health: got %q", got)
		}
		c.Status(200)
	})

	for _, p := range []string{"/api/v1/request/demo", "/api/v1/requestAsync/x", "/health"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		if w.Code != 200 {
			t.Errorf("%s: status %d", p, w.Code)
		}
	}
}
