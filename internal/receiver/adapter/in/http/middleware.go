package http

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"bus/internal/platform/logging"
	"bus/internal/platform/ratelimit"
)

// RateLimitMiddleware применяет глобальный rate-limit per node:
// ключ — первый сегмент path после /v1/request/ или /v1/requestAsync/.
//
// Если limit=0, middleware no-op. При недоступности Redis — fail-open
// (§9.4 ТЗ).
func RateLimitMiddleware(rl *ratelimit.Limiter, limitPerMin int, logger logging.Logger) gin.HandlerFunc {
	if limitPerMin <= 0 {
		return func(c *gin.Context) { c.Next() }
	}
	return func(c *gin.Context) {
		nodePath := strings.TrimPrefix(c.Param("path"), "/")
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
