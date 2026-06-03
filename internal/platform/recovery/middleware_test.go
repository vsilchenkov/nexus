package recovery

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"nexus/internal/platform/logging"
	"nexus/internal/platform/requestid"
)

func TestGinMiddleware_RecoversAndReturns500(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(requestid.GinMiddleware(), GinMiddleware(logging.NewNoop()))
	r.GET("/boom", func(_ *gin.Context) {
		panic("kaboom")
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/boom", nil)
	req.Header.Set(requestid.HeaderKey, "rid-123")

	// Не должно паниковать наружу (процесс/тест живёт).
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body not JSON: %v (%q)", err, w.Body.String())
	}
	if body["request_id"] != "rid-123" {
		t.Errorf("body request_id = %q, want rid-123", body["request_id"])
	}
	if body["error"] == "" {
		t.Error("body error is empty")
	}
}

func TestGinMiddleware_NoPanicPassesThrough(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(GinMiddleware(logging.NewNoop()))
	r.GET("/ok", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/ok", nil))

	if w.Code != http.StatusOK || w.Body.String() != "ok" {
		t.Errorf("got %d %q, want 200 \"ok\"", w.Code, w.Body.String())
	}
}

func TestGinMiddleware_AfterPanicServerStillServes(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(GinMiddleware(logging.NewNoop()))
	r.GET("/boom", func(_ *gin.Context) { panic("x") })
	r.GET("/ok", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/boom", nil))

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/ok", nil))
	if w.Code != http.StatusOK {
		t.Errorf("after panic, /ok status = %d, want 200", w.Code)
	}
}
