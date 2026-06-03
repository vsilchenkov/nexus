package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"

	"nexus/internal/platform/logging"
)

// stubLimiter — простой mock rateAllower'а. Возвращает allow/err из полей.
// Параметры запроса (key, limit) кладёт в lastKey/lastLimit для проверки.
type stubLimiter struct {
	mu        sync.Mutex
	allow     bool
	err       error
	calls     int
	lastKey   string
	lastLimit int
}

func (s *stubLimiter) Allow(_ context.Context, key string, limit int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.lastKey = key
	s.lastLimit = limit
	return s.allow, s.err
}

func newRouter(rl rateAllower, limitPerMin int) (*gin.Engine, *bool) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RateLimitMiddleware(rl, limitPerMin, logging.NewNoop()))
	called := false
	r.POST("/api/v1/request/*path", func(c *gin.Context) {
		called = true
		c.Status(http.StatusOK)
	})
	r.POST("/api/v1/requestAsync/*path", func(c *gin.Context) {
		called = true
		c.Status(http.StatusOK)
	})
	return r, &called
}

func TestRateLimitMiddleware_LimitZero_NoOp(t *testing.T) {
	t.Parallel()
	rl := &stubLimiter{allow: false, err: errors.New("must-not-call")}
	r, called := newRouter(rl, 0)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/request/demo", nil))

	if w.Code != http.StatusOK {
		t.Errorf("status %d, want 200", w.Code)
	}
	if !*called {
		t.Error("handler not called")
	}
	if rl.calls != 0 {
		t.Errorf("limiter called %d times, want 0", rl.calls)
	}
}

func TestRateLimitMiddleware_Allowed(t *testing.T) {
	t.Parallel()
	rl := &stubLimiter{allow: true}
	r, called := newRouter(rl, 10)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/request/demo/path", nil))

	if w.Code != http.StatusOK {
		t.Errorf("status %d, want 200", w.Code)
	}
	if !*called {
		t.Error("handler not called")
	}
	if rl.calls != 1 {
		t.Errorf("limiter calls = %d, want 1", rl.calls)
	}
	if rl.lastKey != "demo/path" {
		t.Errorf("limiter key = %q, want demo/path", rl.lastKey)
	}
	if rl.lastLimit != 10 {
		t.Errorf("limiter limit = %d, want 10", rl.lastLimit)
	}
}

func TestRateLimitMiddleware_Denied(t *testing.T) {
	t.Parallel()
	rl := &stubLimiter{allow: false}
	r, called := newRouter(rl, 5)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/request/demo", nil))

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status %d, want 429", w.Code)
	}
	if *called {
		t.Error("handler must not be called when denied")
	}
}

func TestRateLimitMiddleware_FailOpenOnError(t *testing.T) {
	t.Parallel()
	rl := &stubLimiter{allow: false, err: errors.New("redis down")}
	r, called := newRouter(rl, 5)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/request/demo", nil))

	// fail-open: error → пропускаем запрос
	if w.Code != http.StatusOK {
		t.Errorf("status %d, want 200 (fail-open)", w.Code)
	}
	if !*called {
		t.Error("handler must be called on limiter error (fail-open)")
	}
}

func TestRateLimitMiddleware_EmptyNodePath_Skips(t *testing.T) {
	t.Parallel()
	rl := &stubLimiter{allow: false, err: errors.New("must-not-call")}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	called := false
	// Endpoint без path-параметра — middleware просто пропускает.
	r.Use(RateLimitMiddleware(rl, 5, logging.NewNoop()))
	r.GET("/no-param", func(c *gin.Context) {
		called = true
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/no-param", nil))

	if w.Code != http.StatusOK {
		t.Errorf("status %d, want 200", w.Code)
	}
	if !called {
		t.Error("handler not called")
	}
	if rl.calls != 0 {
		t.Errorf("limiter called %d times, want 0", rl.calls)
	}
}

func TestRateLimitMiddleware_AsyncPath_KeyStripped(t *testing.T) {
	t.Parallel()
	rl := &stubLimiter{allow: true}
	r, _ := newRouter(rl, 10)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/requestAsync/some/sub", nil))

	if w.Code != http.StatusOK {
		t.Errorf("status %d, want 200", w.Code)
	}
	if rl.lastKey != "some/sub" {
		t.Errorf("limiter key = %q, want some/sub", rl.lastKey)
	}
}
