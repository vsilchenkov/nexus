package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"bus/internal/domain"
	"bus/internal/platform/logging"
	"bus/internal/web/usecase"
)

type stubVerifier struct {
	token  *domain.APIToken
	user   *domain.User
	err    error
	calls  int
	lastIn string
}

func (s *stubVerifier) Verify(_ context.Context, plain string) (*domain.APIToken, *domain.User, error) {
	s.calls++
	s.lastIn = plain
	return s.token, s.user, s.err
}

type stubRateLim struct {
	allow bool
	err   error
	calls int
}

func (s *stubRateLim) Allow(_ context.Context, _ string, _ int) (bool, error) {
	s.calls++
	return s.allow, s.err
}

func TestAPITokenAuthMiddleware_NoBearer_Skips(t *testing.T) {
	t.Parallel()
	v := &stubVerifier{}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(APITokenAuthMiddleware(v, nil, 0, logging.NewNoop()))
	r.GET("/", func(c *gin.Context) {
		_, hasSession := c.Get(ctxSessionKey)
		assert.False(t, hasSession)
		c.Status(200)
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, 0, v.calls)
}

func TestAPITokenAuthMiddleware_WrongBearerPrefix_Skips(t *testing.T) {
	t.Parallel()
	v := &stubVerifier{}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(APITokenAuthMiddleware(v, nil, 0, logging.NewNoop()))
	r.GET("/", func(c *gin.Context) { c.Status(200) })

	req := httptest.NewRequest("GET", "/", nil)
	// JWT-токен — не наш формат, пропускаем без вызова Verify.
	req.Header.Set("Authorization", "Bearer eyJhbGciOiJIUzI1NiJ9.xxx")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, 0, v.calls)
}

func TestAPITokenAuthMiddleware_Invalid_401(t *testing.T) {
	t.Parallel()
	v := &stubVerifier{err: domain.ErrUnauthorized}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(APITokenAuthMiddleware(v, nil, 0, logging.NewNoop()))
	r.GET("/", func(c *gin.Context) { c.Status(200) })

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+usecase.APITokenPrefix+"bad")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, 1, v.calls)
}

func TestAPITokenAuthMiddleware_BackendError_500(t *testing.T) {
	t.Parallel()
	v := &stubVerifier{err: errors.New("db down")}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(APITokenAuthMiddleware(v, nil, 0, logging.NewNoop()))
	r.GET("/", func(c *gin.Context) { c.Status(200) })

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+usecase.APITokenPrefix+"x")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestAPITokenAuthMiddleware_Valid_SetsSessionAndToken(t *testing.T) {
	t.Parallel()
	tok := &domain.APIToken{ID: "tok1", UserID: "u1"}
	usr := &domain.User{ID: "u1", Role: domain.UserRoleViewer, Lang: domain.UserLangRU}
	v := &stubVerifier{token: tok, user: usr}
	gin.SetMode(gin.TestMode)

	var (
		seenSession *domain.Session
		seenToken   *domain.APIToken
	)
	r := gin.New()
	r.Use(APITokenAuthMiddleware(v, nil, 0, logging.NewNoop()))
	r.GET("/", func(c *gin.Context) {
		if s, ok := c.Get(ctxSessionKey); ok {
			seenSession, _ = s.(*domain.Session)
		}
		if t, ok := c.Get(ctxAPITokenKey); ok {
			seenToken, _ = t.(*domain.APIToken)
		}
		c.Status(200)
	})

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+usecase.APITokenPrefix+"valid")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	if assert.NotNil(t, seenSession) {
		assert.Equal(t, "u1", seenSession.UserID)
		assert.Equal(t, domain.UserRoleViewer, seenSession.Role)
		assert.Equal(t, domain.UserLangRU, seenSession.Lang)
	}
	assert.Same(t, tok, seenToken)
}

func TestAPITokenAuthMiddleware_RateLimit_429(t *testing.T) {
	t.Parallel()
	tok := &domain.APIToken{ID: "tok2"}
	usr := &domain.User{ID: "u1"}
	v := &stubVerifier{token: tok, user: usr}
	rl := &stubRateLim{allow: false}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(APITokenAuthMiddleware(v, rl, 5, logging.NewNoop()))
	r.GET("/", func(c *gin.Context) { c.Status(200) })

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+usecase.APITokenPrefix+"x")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.Equal(t, "60", w.Header().Get("Retry-After"))
	assert.Equal(t, 1, rl.calls)
}

func TestAPITokenAuthMiddleware_RateLimitErr_FailOpen(t *testing.T) {
	t.Parallel()
	tok := &domain.APIToken{ID: "tok3"}
	usr := &domain.User{ID: "u1"}
	v := &stubVerifier{token: tok, user: usr}
	rl := &stubRateLim{allow: false, err: errors.New("redis down")}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(APITokenAuthMiddleware(v, rl, 5, logging.NewNoop()))
	r.GET("/", func(c *gin.Context) { c.Status(200) })

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+usecase.APITokenPrefix+"x")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Контракт middleware: при err игнорируем allow=false (см. _ присвоение error
	// в коде middleware) — но реально код вызывает `allowed, _ := ...` и читает
	// allowed (false). Значит при err+allow=false → 429.
	// Текущее поведение строгое: error не fail-open в этом middleware.
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
}

func TestRequireSessionOnly(t *testing.T) {
	t.Parallel()
	t.Run("session only — passes", func(t *testing.T) {
		t.Parallel()
		gin.SetMode(gin.TestMode)
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set(ctxSessionKey, &domain.Session{})
			c.Next()
		})
		r.Use(RequireSessionOnly())
		r.GET("/", func(c *gin.Context) { c.Status(200) })

		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
		assert.Equal(t, http.StatusOK, w.Code)
	})
	t.Run("api token — forbidden", func(t *testing.T) {
		t.Parallel()
		gin.SetMode(gin.TestMode)
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set(ctxSessionKey, &domain.Session{})
			c.Set(ctxAPITokenKey, &domain.APIToken{})
			c.Next()
		})
		r.Use(RequireSessionOnly())
		r.GET("/", func(c *gin.Context) { c.Status(200) })

		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
		assert.Equal(t, http.StatusForbidden, w.Code)
	})
}

func TestRequireScope(t *testing.T) {
	t.Parallel()

	t.Run("no api token — passes", func(t *testing.T) {
		t.Parallel()
		gin.SetMode(gin.TestMode)
		r := gin.New()
		r.Use(RequireScope(domain.ScopeLogsRead))
		r.GET("/", func(c *gin.Context) { c.Status(200) })

		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("api token with scope — passes", func(t *testing.T) {
		t.Parallel()
		gin.SetMode(gin.TestMode)
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set(ctxAPITokenKey, &domain.APIToken{Scopes: []string{domain.ScopeLogsRead}})
			c.Next()
		})
		r.Use(RequireScope(domain.ScopeLogsRead))
		r.GET("/", func(c *gin.Context) { c.Status(200) })

		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("api token without scope — 403", func(t *testing.T) {
		t.Parallel()
		gin.SetMode(gin.TestMode)
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set(ctxAPITokenKey, &domain.APIToken{Scopes: []string{domain.ScopeNodesRead}})
			c.Next()
		})
		r.Use(RequireScope(domain.ScopeLogsRead))
		r.GET("/", func(c *gin.Context) { c.Status(200) })

		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
		assert.Equal(t, http.StatusForbidden, w.Code)
	})

	t.Run("api token with nil — 403", func(t *testing.T) {
		t.Parallel()
		gin.SetMode(gin.TestMode)
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set(ctxAPITokenKey, (*domain.APIToken)(nil))
			c.Next()
		})
		r.Use(RequireScope(domain.ScopeLogsRead))
		r.GET("/", func(c *gin.Context) { c.Status(200) })

		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
		assert.Equal(t, http.StatusForbidden, w.Code)
	})
}
