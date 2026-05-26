package http

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"bus/internal/domain"
	"bus/internal/platform/logging"
	"bus/internal/web/usecase"
)

const ctxAPITokenKey = "databus.api_token"

// apiTokenVerifier — consumer-side interface для APITokenUsecase.Verify (§17.4).
type apiTokenVerifier interface {
	Verify(ctx context.Context, plain string) (*domain.APIToken, *domain.User, error)
}

// tokenRateAllower — то же, что rateAllower в receiver/http; локальный.
type tokenRateAllower interface {
	Allow(ctx context.Context, key string, limitPerMin int) (bool, error)
}

// APITokenAuthMiddleware — пытается аутентифицировать запрос по
// Authorization: Bearer db_... При успехе кладёт session/токен
// в context и пропускает. При наличии заголовка, но невалидном
// токене — 401. Если заголовка нет — c.Next() (дальше попробует
// session-cookie middleware).
//
// rate-limit per-token: cfg.Web.api_token_rate_limit_per_min (§7.14).
func APITokenAuthMiddleware(
	uc apiTokenVerifier,
	rl tokenRateAllower,
	rateLimitPerMin int,
	logger logging.Logger,
) gin.HandlerFunc {
	return func(c *gin.Context) {
		got := c.GetHeader("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(got, prefix) {
			c.Next()
			return
		}
		value := got[len(prefix):]
		if !strings.HasPrefix(value, usecase.APITokenPrefix) {
			// Не наш токен; пусть последующее middleware (session-cookie) обработает.
			c.Next()
			return
		}
		token, user, err := uc.Verify(c.Request.Context(), value)
		if err != nil {
			if errors.Is(err, domain.ErrUnauthorized) || errors.Is(err, domain.ErrUserInactive) {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid api token"})
			} else {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "auth backend unavailable"})
			}
			c.Abort()
			return
		}

		if rateLimitPerMin > 0 && rl != nil {
			allowed, _ := rl.Allow(c.Request.Context(), "apitoken:"+token.ID, rateLimitPerMin)
			if !allowed {
				c.Header("Retry-After", "60")
				c.JSON(http.StatusTooManyRequests, gin.H{"error": "rate limit exceeded"})
				c.Abort()
				return
			}
		}

		// Кладём «псевдо-сессию» в тот же ключ — handlers по-прежнему
		// могут использовать sessionFromCtx без отдельной ветки.
		s := &domain.Session{
			Token:      "",
			UserID:     user.ID,
			Role:       user.Role,
			Lang:       user.Lang,
			CreatedAt:  time.Now(),
			LastSeenAt: time.Now(),
		}
		c.Set(ctxSessionKey, s)
		c.Set(ctxAPITokenKey, token)
		c.Next()
	}
}

// RequireSessionOnly — middleware, который отклоняет вызовы,
// аутентифицированные API-токеном (Bearer db_...). Используется для
// эндпоинтов типа SSE live-tail, доступных только UI-сессиям (§7.14).
func RequireSessionOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, ok := c.Get(ctxAPITokenKey); ok {
			c.JSON(http.StatusForbidden, gin.H{"error": "endpoint not available via API token"})
			c.Abort()
			return
		}
		c.Next()
	}
}

// RequireScope — middleware для эндпоинтов, которые могут вызываться по
// API-токену. Если в context'е есть APIToken и у него нет нужного scope
// → 403. Если токена нет (обычная session-cookie) — пропускает (роль
// уже проверена RequireRole).
func RequireScope(scope string) gin.HandlerFunc {
	return func(c *gin.Context) {
		v, ok := c.Get(ctxAPITokenKey)
		if !ok {
			c.Next()
			return
		}
		t, _ := v.(*domain.APIToken)
		if t == nil || !t.HasScope(scope) {
			c.JSON(http.StatusForbidden, gin.H{"error": "missing scope: " + scope})
			c.Abort()
			return
		}
		c.Next()
	}
}
