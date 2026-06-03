// Package requestid — сквозной идентификатор запроса (§30 ТЗ).
//
// Каждый входящий HTTP-запрос получает request_id (UUID v4): читается из
// заголовка X-Request-Id, а если его нет — генерируется. Id кладётся в
// контекст запроса, в gin.Context и в заголовок ответа, чтобы связать логи,
// Sentry-события и транзакции одного запроса между сервисами.
package requestid

import (
	"context"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// HeaderKey — имя HTTP-заголовка, по которому передаётся request_id.
const HeaderKey = "X-Request-Id"

// ginKey — ключ в gin.Context. ctxKey — типизированный ключ в context.Context
// (отдельный тип, чтобы не пересекаться с чужими ключами — §6 CLAUDE.md).
const ginKey = "request_id"

type ctxKey struct{}

// GinMiddleware читает X-Request-Id из запроса (не перезаписывает существующий),
// при отсутствии/пустом значении генерирует UUID v4, кладёт id в контекст
// запроса и gin.Context и выставляет заголовок ответа X-Request-Id.
//
// Должен стоять первым в цепочке middleware — id нужен всем нижележащим
// (otel, sentry, recovery, handler'ам).
func GinMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(HeaderKey)
		if id == "" {
			id = uuid.NewString()
		}
		c.Set(ginKey, id)
		c.Request = c.Request.WithContext(WithValue(c.Request.Context(), id))
		c.Header(HeaderKey, id)
		c.Next()
	}
}

// WithValue возвращает контекст с привязанным request_id.
func WithValue(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// FromContext извлекает request_id из контекста. Возвращает "" если не задан.
func FromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if id, ok := ctx.Value(ctxKey{}).(string); ok {
		return id
	}
	return ""
}
