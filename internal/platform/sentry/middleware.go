package sentry

import (
	"github.com/getsentry/sentry-go"
	"github.com/gin-gonic/gin"

	"nexus/internal/platform/metrics"
	"nexus/internal/platform/requestid"
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
// infraPaths — инфра-эндпоинты, которые НЕ оборачиваем в Sentry-транзакцию:
// скрейпы Prometheus (/metrics каждые ~15с × 3 сервиса) и docker-healthcheck
// (/health) иначе заваливают Sentry бесполезными транзакциями. Зеркалит
// пропуск в metrics.GinMiddleware ("чтобы не зашумлять данные").
func skipSentry(fullPath string) bool {
	switch fullPath {
	case "", "/metrics", "/health", "/ready":
		return true
	}
	return false
}

func GinMiddleware(service string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if skipSentry(c.FullPath()) {
			c.Next()
			return
		}

		hub := sentry.CurrentHub().Clone()
		ctx := sentry.SetHubOnContext(c.Request.Context(), hub)

		// request_id (§30): тег на scope hub'а ⇒ попадёт во все события этого
		// запроса (включая залогированные через logger.WithContext); тег на
		// span ниже ⇒ во все транзакции. requestid-middleware идёт первым, id
		// уже в контексте.
		reqID := requestid.FromContext(c.Request.Context())
		if reqID != "" {
			hub.Scope().SetTag("request_id", reqID)
		}

		txName := c.Request.Method + " " + routeName(c)
		span := sentry.StartTransaction(ctx, txName,
			sentry.WithOpName("http.server"),
			sentry.WithTransactionSource(sentry.SourceRoute),
		)
		defer span.Finish()

		span.SetTag("service", service)
		if reqID != "" {
			span.SetTag("request_id", reqID)
		}

		c.Request = c.Request.WithContext(span.Context())
		c.Next()

		// §78.2: теги node/root_method ставятся ПОСЛЕ обработчика. У боевого
		// трафика Receiver'а теперь один общий маршрут /api/v1/*path, поэтому
		// вывести их из имени маршрута нельзя — обработчик кладёт значения в
		// контекст, а он заполнен только после c.Next(). Span финиширует в defer
		// выше, так что теги в него попадают.
		if v1 := nodePathFromGin(c); v1 != "" {
			span.SetTag("node", v1)
		}
		if root := rootMethod(c); root != "" {
			span.SetTag("root_method", root)
		}

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

// nodePathFromGin — путь узла для тега `node`: канонический путь, положенный
// обработчиком (metrics.NodeLabelKey), иначе сырой path-параметр без ведущего
// слеша.
//
// §78.2: сырой параметр у боевых маршрутов теперь содержит и сегмент метода
// (`request/webhook/sbp-qr`), поэтому предпочтение — значению из контекста: с
// ним тег совпадает с меткой `node` в Prometheus и не меняется от формы адреса.
func nodePathFromGin(c *gin.Context) string {
	if v := c.GetString(metrics.NodeLabelKey); v != "" {
		return v
	}
	p := c.Param("path")
	if p == "" {
		return ""
	}
	if p[0] == '/' {
		return p[1:]
	}
	return p
}

// rootMethod — корневой метод узла для тега `root_method`. Источник тот же, что
// у метки method в Prometheus (§78.2): у боевого трафика имя маршрута одно на
// все формы адреса, выводить из него нечего.
func rootMethod(c *gin.Context) string {
	return c.GetString(metrics.RootMethodLabelKey)
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
