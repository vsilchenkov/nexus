// Package recovery — кастомный gin-recovery middleware (§30 ТЗ).
//
// Заменяет встроенный gin.Recovery(): тот пишет в собственный stderr-writer и
// не интегрирован ни с нашим логгером, ни с Sentry. Наш middleware при панике
// в обработчике логирует её как error (с request_id, методом, путём и
// stacktrace), капчурит в request-scoped Sentry-hub и возвращает клиенту 500.
package recovery

import (
	"net/http"
	"runtime/debug"

	"github.com/gin-gonic/gin"

	"nexus/internal/platform/logging"
	"nexus/internal/platform/requestid"
)

// GinMiddleware перехватывает панику в нижележащих middleware и обработчике.
//
// Размещается в цепочке ПОСЛЕ sentry-middleware (чтобы паника гасилась до
// возврата управления в sentry-middleware и его span получил статус 500) и
// РАНЬШЕ обработчиков/metrics (чтобы ловить их паники).
//
// logger.WithContext(ctx) привязывает контекст запроса: sentryslog-хендлер
// достанет из него склонированный hub (выставленный sentry-middleware) и
// отправит событие с тегами service/node/request_id. Отдельный ручной capture
// не нужен — иначе получится дубль события.
func GinMiddleware(logger logging.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			r := recover()
			if r == nil {
				return
			}
			ctx := c.Request.Context()
			reqID := requestid.FromContext(ctx)
			logger.WithContext(ctx).Error("panic recovered in http handler",
				logger.Any("panic", r),
				logger.Str("request_id", reqID),
				logger.Str("method", c.Request.Method),
				logger.Str("path", c.FullPath()),
				logger.Str("stack", string(debug.Stack())),
			)
			if c.Writer.Written() {
				// Часть ответа уже ушла клиенту — статус не переписать,
				// просто прерываем цепочку.
				c.Abort()
				return
			}
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error":      "internal server error",
				"request_id": reqID,
			})
		}()
		c.Next()
	}
}
