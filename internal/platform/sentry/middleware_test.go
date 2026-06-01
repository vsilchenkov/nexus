package sentry

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/getsentry/sentry-go"
	"github.com/gin-gonic/gin"
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

func TestRootMethod(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.GET("/api/v1/request/*path", func(c *gin.Context) {
		if got := rootMethod(c); got != "request" {
			t.Errorf("/api/v1/request: got %q", got)
		}
		c.Status(200)
	})
	r.GET("/api/v1/requestAsync/*path", func(c *gin.Context) {
		if got := rootMethod(c); got != "requestAsync" {
			t.Errorf("/api/v1/requestAsync: got %q", got)
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
