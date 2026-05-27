package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"nexus/internal/domain"
	"nexus/internal/platform/config"
)

// stubSessionChecker — мок sessionChecker.
type stubSessionChecker struct {
	session *domain.Session
	err     error
	calls   int
	lastTok string
}

func (s *stubSessionChecker) Check(_ context.Context, token string) (*domain.Session, error) {
	s.calls++
	s.lastTok = token
	return s.session, s.err
}

func webCfg() *config.WebSection {
	return &config.WebSection{SessionCookieName: "nexus_session"}
}

func TestAuthMiddleware_NoCookie_401(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(AuthMiddleware(&stubSessionChecker{}, webCfg()))
	r.GET("/", func(c *gin.Context) { c.Status(200) })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestAuthMiddleware_SessionExpired_401(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	checker := &stubSessionChecker{err: domain.ErrSessionNotFound}
	r := gin.New()
	r.Use(AuthMiddleware(checker, webCfg()))
	r.GET("/", func(c *gin.Context) { c.Status(200) })

	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: "nexus_session", Value: "stale"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, "stale", checker.lastTok)
}

func TestAuthMiddleware_RedisDown_503(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	checker := &stubSessionChecker{err: errors.New("redis: connection refused")}
	r := gin.New()
	r.Use(AuthMiddleware(checker, webCfg()))
	r.GET("/", func(c *gin.Context) { c.Status(200) })

	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: "nexus_session", Value: "tok"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestAuthMiddleware_Valid_PassesAndStoresSession(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	want := &domain.Session{UserID: "u1", Role: domain.UserRoleAdmin}
	checker := &stubSessionChecker{session: want}

	var seen *domain.Session
	r := gin.New()
	r.Use(AuthMiddleware(checker, webCfg()))
	r.GET("/", func(c *gin.Context) {
		if v, ok := c.Get(ctxSessionKey); ok {
			seen, _ = v.(*domain.Session)
		}
		c.Status(200)
	})

	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: "nexus_session", Value: "tok"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Same(t, want, seen)
}

func TestAuthMiddleware_AlreadySet_SkipsCheck(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	checker := &stubSessionChecker{err: errors.New("must-not-call")}
	r := gin.New()
	// «Перед» auth middleware заранее кладём session (как делает API-token middleware).
	r.Use(func(c *gin.Context) {
		c.Set(ctxSessionKey, &domain.Session{UserID: "via-token"})
		c.Next()
	})
	r.Use(AuthMiddleware(checker, webCfg()))
	r.GET("/", func(c *gin.Context) { c.Status(200) })

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, 0, checker.calls)
}

func TestRequireRole(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		session *domain.Session
		require domain.UserRole
		status  int
	}{
		{"admin requires admin → ok", &domain.Session{Role: domain.UserRoleAdmin}, domain.UserRoleAdmin, 200},
		{"viewer requires admin → 403", &domain.Session{Role: domain.UserRoleViewer}, domain.UserRoleAdmin, 403},
		{"viewer requires viewer → ok", &domain.Session{Role: domain.UserRoleViewer}, domain.UserRoleViewer, 200},
		{"no session → 403", nil, domain.UserRoleAdmin, 403},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			gin.SetMode(gin.TestMode)
			r := gin.New()
			if c.session != nil {
				r.Use(func(ctx *gin.Context) {
					ctx.Set(ctxSessionKey, c.session)
					ctx.Next()
				})
			}
			r.Use(RequireRole(c.require))
			r.GET("/", func(ctx *gin.Context) { ctx.Status(200) })

			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
			assert.Equal(t, c.status, w.Code)
		})
	}
}

func TestSessionFromCtx(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	t.Run("present", func(t *testing.T) {
		t.Parallel()
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		want := &domain.Session{UserID: "u"}
		c.Set(ctxSessionKey, want)
		got, ok := sessionFromCtx(c)
		assert.True(t, ok)
		assert.Same(t, want, got)
	})
	t.Run("absent", func(t *testing.T) {
		t.Parallel()
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		_, ok := sessionFromCtx(c)
		assert.False(t, ok)
	})
	t.Run("wrong type", func(t *testing.T) {
		t.Parallel()
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Set(ctxSessionKey, "not-a-session")
		_, ok := sessionFromCtx(c)
		assert.False(t, ok)
	})
}
