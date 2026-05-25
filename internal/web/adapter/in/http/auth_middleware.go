package http

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"bus/internal/domain"
	"bus/internal/platform/config"
	"bus/internal/web/usecase"
)

const ctxSessionKey = "databus.session"

// AuthMiddleware — проверяет session-cookie через AuthUsecase.Check.
// При неуспехе — 401 + Abort.
//
// Если cookie отсутствует или сессия истекла — 401. Если Redis недоступен
// (Check вернёт не ErrSessionNotFound) — 503 (§9.4 ТЗ: «Web API при
// недоступности Redis отвечает 503 на эндпоинты, требующие авторизации»).
func AuthMiddleware(auth *usecase.AuthUsecase, cfg *config.WebSection) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, err := c.Cookie(cfg.SessionCookieName)
		if err != nil || token == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			c.Abort()
			return
		}
		s, err := auth.Check(c.Request.Context(), token)
		if err != nil {
			if errors.Is(err, domain.ErrSessionNotFound) {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "session expired"})
			} else {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "auth backend unavailable"})
			}
			c.Abort()
			return
		}
		c.Set(ctxSessionKey, s)
		c.Next()
	}
}

// RequireRole — middleware, требующий конкретную роль.
func RequireRole(role domain.UserRole) gin.HandlerFunc {
	return func(c *gin.Context) {
		s, ok := sessionFromCtx(c)
		if !ok || s.Role != role {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			c.Abort()
			return
		}
		c.Next()
	}
}

func sessionFromCtx(c *gin.Context) (*domain.Session, bool) {
	v, ok := c.Get(ctxSessionKey)
	if !ok {
		return nil, false
	}
	s, ok := v.(*domain.Session)
	return s, ok
}
