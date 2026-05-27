package otel

import (
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.27.0"
	"go.opentelemetry.io/otel/trace"
)

// GinMiddleware — минималистичный аналог otelgin.Middleware.
//
// Зачем своя реализация: official otelgin тащит большой граф зависимостей
// через opentelemetry-go-contrib, который у нас на Windows валит линкер
// при go build ./... (CLAUDE.md «грабли» #1). Наш handler делает ровно то,
// что нужно для §16 ТЗ:
//
//  1. Извлекает traceparent / baggage из header'ов (TextMapPropagator).
//  2. Стартует server-span с http.* атрибутами по semconv.
//  3. Пишет статус в span (codes.Error для 5xx).
//  4. Делает .End() в defer'е — span закроется даже на panic'е.
//
// При выключенном tracing (Enable=false) глобальный TracerProvider —
// no-op, и весь middleware превращается в пару дешёвых allocation'ов
// без сетевой нагрузки.
func GinMiddleware(serviceName string) gin.HandlerFunc {
	tracer := otel.Tracer("nexus/" + serviceName)
	propagator := otel.GetTextMapPropagator()

	return func(c *gin.Context) {
		ctx := propagator.Extract(c.Request.Context(), propagation.HeaderCarrier(c.Request.Header))

		opts := []trace.SpanStartOption{
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				semconv.HTTPRequestMethodKey.String(c.Request.Method),
				semconv.URLPath(c.Request.URL.Path),
				attribute.String("http.route", c.FullPath()),
				semconv.UserAgentOriginal(c.Request.UserAgent()),
				attribute.String("http.client_ip", c.ClientIP()),
			),
		}
		ctx, span := tracer.Start(ctx, c.FullPath(), opts...)
		defer span.End()

		c.Request = c.Request.WithContext(ctx)
		c.Next()

		status := c.Writer.Status()
		span.SetAttributes(semconv.HTTPResponseStatusCode(status))
		if status >= 500 {
			span.SetStatus(codes.Error, "5xx response")
		}
	}
}
