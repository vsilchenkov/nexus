package http

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"nexus/internal/platform/logging"
)

// rateAllower — consumer-side interface для rate-limiter'а (§17.4 ТЗ).
// Удовлетворяется *ratelimit.Limiter; объявлен здесь, чтобы middleware
// можно было unit-тестировать без Redis.
type rateAllower interface {
	Allow(ctx context.Context, key string, limitPerMin int) (bool, error)
}

// RateLimitMiddleware применяет глобальный rate-limit per node:
// ключ — путь узла после сегмента метода (/api/v1/request/, /api/v1/requestAsync/,
// /api/v1/callback/) либо весь путь при короткой форме адреса §78.1.
//
// §78.2: сегмент метода срезается тем же SplitVerb, что и в handler'е. Иначе
// legacy- и короткая форма адреса одного узла попали бы в РАЗНЫЕ корзины, и
// клиент удваивал бы квоту простой сменой формы (а у всех узлов разом сменились
// бы ключи Redis).
//
// Если limit=0, middleware no-op. При недоступности Redis — fail-open
// (§9.4 ТЗ).
func RateLimitMiddleware(rl rateAllower, limitPerMin int, logger logging.Logger) gin.HandlerFunc {
	if limitPerMin <= 0 {
		return func(c *gin.Context) { c.Next() }
	}
	return func(c *gin.Context) {
		_, rest := SplitVerb(c.Param("path"))
		nodePath := strings.TrimPrefix(rest, "/")
		if nodePath == "" {
			c.Next()
			return
		}
		allowed, err := rl.Allow(c.Request.Context(), nodePath, limitPerMin)
		if err != nil {
			// fail-open + warning
			logger.Warn("ratelimit redis error, pass-through",
				logger.Str("node", nodePath),
				logger.Err(err))
			c.Next()
			return
		}
		if !allowed {
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error": "rate limit exceeded",
			})
			c.Abort()
			return
		}
		c.Next()
	}
}
