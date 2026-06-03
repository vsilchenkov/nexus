package http

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"nexus/internal/domain"
	"nexus/internal/platform/config"
)

const ctxSessionKey = "nexus.session"

// sessionChecker — consumer-side interface для AuthUsecase.Check (§17.4 ТЗ).
// Удовлетворяется *usecase.AuthUsecase; вынесен сюда, чтобы middleware
// можно было unit-тестировать без users/sessions репозиториев.
type sessionChecker interface {
	Check(ctx context.Context, token string) (*domain.Session, error)
}

// AuthMiddleware — проверяет session-cookie через AuthUsecase.Check.
// При неуспехе — 401 + Abort.
//
// Если cookie отсутствует или сессия истекла — 401. Если Redis недоступен
// (Check вернёт не ErrSessionNotFound) — 503 (§9.4 ТЗ: «Web API при
// недоступности Redis отвечает 503 на эндпоинты, требующие авторизации»).
func AuthMiddleware(auth sessionChecker, cfg *config.WebSection) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Если API-token middleware уже выставил session — пропускаем.
		if _, ok := c.Get(ctxSessionKey); ok {
			c.Next()
			return
		}
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

// RequireMinRole — middleware, требующий роль не ниже min по иерархии
// viewer < manager < admin (§26). Admin проходит любую manager-проверку.
func RequireMinRole(min domain.UserRole) gin.HandlerFunc {
	return func(c *gin.Context) {
		s, ok := sessionFromCtx(c)
		if !ok || !s.Role.AtLeast(min) {
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

// currentTeamID — UUID команды в контексте текущей сессии (multi-tenancy
// v2). Возвращает пустую строку, если сессии нет или CurrentTeamID не
// выставлен (последнее — только в legacy unit-тестах). Handler'ы должны
// использовать его как scope для всех list/get/mutate-операций.
func currentTeamID(c *gin.Context) string {
	s, ok := sessionFromCtx(c)
	if !ok {
		return ""
	}
	return s.CurrentTeamID
}
