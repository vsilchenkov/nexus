package sentry

import (
	"github.com/getsentry/sentry-go"
	"github.com/gin-gonic/gin"
)

// GinMiddleware — оборачивает каждый запрос в Sentry-транзакцию (§14.3 ТЗ).
//
//	op   — "http.server"
//	name — "{method} {path}", напр. "POST /v1/request/{path}"
//	tags — service, node (если /v1/.../{path}), root_method
//
// Если Sentry не инициализирован (Use=false), middleware ставит пустой
// span — это no-op и не влияет на производительность.
//
// Должен быть в начале цепочки middleware, до Recovery и логгера, чтобы
// захватить весь жизненный цикл запроса (включая panic).
func GinMiddleware(service string) gin.HandlerFunc {
	return func(c *gin.Context) {
		hub := sentry.CurrentHub().Clone()
		ctx := sentry.SetHubOnContext(c.Request.Context(), hub)

		txName := c.Request.Method + " " + routeName(c)
		span := sentry.StartTransaction(ctx, txName,
			sentry.WithOpName("http.server"),
			sentry.WithTransactionSource(sentry.SourceRoute),
		)
		defer span.Finish()

		span.SetTag("service", service)
		if v1 := nodePathFromGin(c); v1 != "" {
			span.SetTag("node", v1)
		}
		if root := rootMethod(c); root != "" {
			span.SetTag("root_method", root)
		}

		c.Request = c.Request.WithContext(span.Context())
		c.Next()

		span.Status = httpStatusToSpanStatus(c.Writer.Status())
	}
}

// StartSpan создаёт child-span под текущей транзакцией. Используется
// в usecase-слое, чтобы делать вложенные спаны (например, db.query).
func StartSpan(c *gin.Context, op, description string) *sentry.Span {
	span := sentry.StartSpan(c.Request.Context(), op, sentry.WithDescription(description))
	c.Request = c.Request.WithContext(span.Context())
	return span
}

// routeName — Gin-route как шаблон ("/v1/request/*path"), а не как
// фактический URL. Это даёт сгруппированный вид в Sentry.
func routeName(c *gin.Context) string {
	if r := c.FullPath(); r != "" {
		return r
	}
	return c.Request.URL.Path
}

// nodePathFromGin — для /v1/request/*path и /v1/requestAsync/*path
// возвращает значение path-параметра без ведущего слеша.
func nodePathFromGin(c *gin.Context) string {
	p := c.Param("path")
	if p == "" {
		return ""
	}
	if p[0] == '/' {
		return p[1:]
	}
	return p
}

func rootMethod(c *gin.Context) string {
	full := c.FullPath()
	switch {
	case full == "/v1/request/*path":
		return "request"
	case full == "/v1/requestAsync/*path":
		return "requestAsync"
	}
	return ""
}

func httpStatusToSpanStatus(code int) sentry.SpanStatus {
	switch {
	case code >= 200 && code < 300:
		return sentry.SpanStatusOK
	case code == 401:
		return sentry.SpanStatusUnauthenticated
	case code == 403:
		return sentry.SpanStatusPermissionDenied
	case code == 404:
		return sentry.SpanStatusNotFound
	case code == 429:
		return sentry.SpanStatusResourceExhausted
	case code >= 400 && code < 500:
		return sentry.SpanStatusInvalidArgument
	case code >= 500:
		return sentry.SpanStatusInternalError
	}
	return sentry.SpanStatusUnknown
}
