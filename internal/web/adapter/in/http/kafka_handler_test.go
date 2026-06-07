package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"nexus/internal/domain"
)

// fakeLimiter — подменяет *ratelimit.Limiter в тестах (без Redis).
// Записывает последний ключ/лимит и возвращает заранее заданный вердикт.
type fakeLimiter struct {
	allow   bool
	err     error
	lastKey string
	lastLim int
	calls   int
}

func (f *fakeLimiter) Allow(_ context.Context, key string, limitPerMin int) (bool, error) {
	f.calls++
	f.lastKey = key
	f.lastLim = limitPerMin
	return f.allow, f.err
}

// withKafkaRL собирает движок: опционально кладёт session в контекст, затем
// rate-limit middleware и заглушку-handler (200).
func withKafkaRL(limiter rateLimiter, limit int, session *domain.Session) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	if session != nil {
		r.Use(func(c *gin.Context) {
			c.Set(ctxSessionKey, session)
			c.Next()
		})
	}
	r.Use(KafkaRateLimitMiddleware(limiter, limit))
	r.GET("/api/kafka/overview", func(c *gin.Context) { c.Status(http.StatusOK) })
	return r
}

func TestKafkaRateLimit_AllowsUnderLimit(t *testing.T) {
	t.Parallel()
	lim := &fakeLimiter{allow: true}
	r := withKafkaRL(lim, 60, &domain.Session{UserID: "u1", Role: domain.UserRoleAdmin})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/kafka/overview", nil))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "kafka:u1", lim.lastKey)
	assert.Equal(t, 60, lim.lastLim)
	assert.Equal(t, 1, lim.calls)
}

func TestKafkaRateLimit_BlocksOverLimit(t *testing.T) {
	t.Parallel()
	lim := &fakeLimiter{allow: false}
	r := withKafkaRL(lim, 60, &domain.Session{UserID: "u1", Role: domain.UserRoleAdmin})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/kafka/overview", nil))

	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	// localizedError тело: {"error": "..."} по ключу kafka.rate_limited.
	assert.Contains(t, w.Body.String(), "error")
}

func TestKafkaRateLimit_KeyPerUser(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		session *domain.Session
		wantKey string
	}{
		{"with session → per-user key", &domain.Session{UserID: "alice"}, "kafka:alice"},
		{"no session → anon key", nil, "kafka:anon"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			lim := &fakeLimiter{allow: true}
			r := withKafkaRL(lim, 60, c.session)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("GET", "/api/kafka/overview", nil))
			assert.Equal(t, http.StatusOK, w.Code)
			assert.Equal(t, c.wantKey, lim.lastKey)
		})
	}
}

func TestKafkaRateLimit_FailOpenOnRedisError(t *testing.T) {
	t.Parallel()
	// Лимитер сам fail-open'ит (возвращает allow=true при ошибке Redis); проверяем,
	// что middleware пропускает запрос, когда Allow вернул true несмотря на err.
	lim := &fakeLimiter{allow: true, err: assert.AnError}
	r := withKafkaRL(lim, 60, &domain.Session{UserID: "u1"})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/kafka/overview", nil))
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestKafkaRateLimit_DisabledWhenZero(t *testing.T) {
	t.Parallel()
	// limit=0 → Limiter.Allow вернёт true без обращения к Redis; middleware
	// пропускает. Фейк это эмулирует, проверяем сквозной проход.
	lim := &fakeLimiter{allow: true}
	r := withKafkaRL(lim, 0, &domain.Session{UserID: "u1"})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/kafka/overview", nil))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, 0, lim.lastLim)
}
