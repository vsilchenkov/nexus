package requestid

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func TestGinMiddleware_GeneratesWhenAbsent(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(GinMiddleware())

	var seen string
	r.GET("/", func(c *gin.Context) {
		seen = FromContext(c.Request.Context())
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))

	if seen == "" {
		t.Fatal("request_id not set in context")
	}
	if _, err := uuid.Parse(seen); err != nil {
		t.Errorf("generated request_id is not a valid UUID: %q (%v)", seen, err)
	}
	if got := w.Header().Get(HeaderKey); got != seen {
		t.Errorf("response header %s = %q, want %q", HeaderKey, got, seen)
	}
}

func TestGinMiddleware_PreservesExisting(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(GinMiddleware())

	const existing = "my-correlation-id"
	var seen string
	r.GET("/", func(c *gin.Context) {
		seen = FromContext(c.Request.Context())
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set(HeaderKey, existing)
	r.ServeHTTP(w, req)

	if seen != existing {
		t.Errorf("FromContext = %q, want %q (must not overwrite)", seen, existing)
	}
	if got := w.Header().Get(HeaderKey); got != existing {
		t.Errorf("response header = %q, want %q", got, existing)
	}
}

func TestFromContext_EmptyWhenUnset(t *testing.T) {
	t.Parallel()
	if got := FromContext(t.Context()); got != "" {
		t.Errorf("FromContext = %q, want empty", got)
	}
	if got := FromContext(nil); got != "" { //nolint:staticcheck // явно проверяем nil-ctx
		t.Errorf("FromContext(nil) = %q, want empty", got)
	}
}
