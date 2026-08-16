package http

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"nexus/internal/platform/logging"
)

func newCSRFTestRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(CSRFOriginCheck(logging.NewNoop()))
	ok := func(c *gin.Context) { c.Status(http.StatusNoContent) }
	r.GET("/api/x", ok)
	r.POST("/api/x", ok)
	r.DELETE("/api/x", ok)
	return r
}

func TestCSRFOriginCheck(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		method     string
		origin     string
		authHeader string
		wantStatus int
	}{
		{"GET без Origin", http.MethodGet, "", "", http.StatusNoContent},
		{"GET с чужим Origin (read-only не проверяем)", http.MethodGet, "https://evil.example", "", http.StatusNoContent},
		{"POST без Origin (curl/скрипты)", http.MethodPost, "", "", http.StatusNoContent},
		{"POST same-origin", http.MethodPost, "http://example.com", "", http.StatusNoContent},
		{"POST same-origin https", http.MethodPost, "https://example.com", "", http.StatusNoContent},
		{"POST cross-origin", http.MethodPost, "https://evil.example", "", http.StatusForbidden},
		{"POST Origin null (sandboxed iframe)", http.MethodPost, "null", "", http.StatusForbidden},
		{"DELETE cross-origin", http.MethodDelete, "https://evil.example", "", http.StatusForbidden},
		{"POST cross-origin с Bearer (API-токен)", http.MethodPost, "https://evil.example", "Bearer db_xxx", http.StatusNoContent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := newCSRFTestRouter(t)
			req := httptest.NewRequest(tt.method, "http://example.com/api/x", nil)
			req.Host = "example.com"
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			assert.Equal(t, tt.wantStatus, w.Code)
		})
	}
}

func TestSecurityHeaders(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(SecurityHeaders())
	r.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.GET("/swagger/web/index.html", func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "DENY", w.Header().Get("X-Frame-Options"))
	assert.Equal(t, "same-origin", w.Header().Get("Referrer-Policy"))
	csp := w.Header().Get("Content-Security-Policy")
	assert.Contains(t, csp, "default-src 'self'")
	// §89.1: шрифты локальные и встроены в бинарь. Внешних источников в политике
	// быть не должно ни одного — иначе изолированный контур снова зависит от
	// сети, а поймать это нечем: страница «просто выглядит иначе».
	assert.Contains(t, csp, "font-src 'self';")
	assert.NotContains(t, csp, "https://", "CSP не должен разрешать внешние origin'ы")

	// Swagger UI живёт на inline-скрипте конфигурации — CSP не ставим.
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/swagger/web/index.html", nil))
	assert.Empty(t, w.Header().Get("Content-Security-Policy"))
	assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
}
