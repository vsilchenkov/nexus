package otel

import (
	"context"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.27.0"
	"go.opentelemetry.io/otel/trace"
)

// InjectHTTPHeaders проставляет traceparent / baggage / другие propagator-keys
// в исходящий http.Header. Если global TracerProvider — no-op (Enable=false),
// ничего не вставит — это правильное поведение.
//
// Назначение: связать outbound HTTP-вызов (Sender → внешний URL, Web → Receiver
// для replay) с тем же trace'ом, в котором обрабатывается входящий запрос.
func InjectHTTPHeaders(ctx context.Context, h http.Header) {
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(h))
}

// StartHTTPClientSpan открывает client-span вокруг outbound HTTP-вызова и
// возвращает context со span'ом + finish-функцию.
//
// При Enable=false возвращает no-op span — производительность не страдает,
// finish() безопасно вызывать.
//
// Использование:
//
//	ctx, finish := otel.StartHTTPClientSpan(ctx, "POST", url)
//	defer finish(statusCode, err)
//	otel.InjectHTTPHeaders(ctx, req.Header)
//	// ... вызов ...
func StartHTTPClientSpan(ctx context.Context, method, url string) (context.Context, func(statusCode int, err error)) {
	tracer := otel.Tracer("nexus/http.client")
	ctx, span := tracer.Start(ctx, method+" "+sanitizeURL(url),
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			semconv.HTTPRequestMethodKey.String(method),
			semconv.URLFull(url),
		),
	)
	finish := func(statusCode int, err error) {
		if statusCode > 0 {
			span.SetAttributes(semconv.HTTPResponseStatusCode(statusCode))
		}
		switch {
		case err != nil:
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		case statusCode >= 500:
			span.SetStatus(codes.Error, "5xx response")
		}
		span.End()
	}
	return ctx, finish
}

// sanitizeURL — оставляет только host и path (без query), чтобы PII / токены из
// query не утекали в трейсинг-бэкенд. Если URL невалиден — возвращаем как есть
// (минимальный шанс утечки vs. потеря наблюдаемости).
func sanitizeURL(raw string) string {
	for i, r := range raw {
		if r == '?' {
			return raw[:i]
		}
	}
	return raw
}
