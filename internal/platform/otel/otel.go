// Package otel — bootstrap OpenTelemetry distributed tracing (§16 ТЗ).
//
// Назначение: единая точка инициализации tracer-provider'а для всех трёх
// сервисов (Receiver, Sender, Web). Без OpenTelemetry (cfg.Otel.Enable=false)
// — полный no-op: глобальный provider не устанавливается, отдаётся
// noop-trace.Tracer'у. С включённым — экспорт через OTLP/HTTP в Collector
// (обычно стоит рядом и шлёт в Tempo/Jaeger/Honeycomb/Datadog).
//
// Это не замена Sentry-tracing'у (§14.3 ТЗ — Phase 5): Sentry мы оставляем
// для error-correlation, OTel — для распределённой трассировки сервис→сервис.
// Они независимы и могут работать параллельно.
package otel

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.27.0"

	"nexus/internal/platform/config"
	"nexus/internal/platform/logging"
)

// ShutdownFunc — callback для graceful shutdown'а tracer-provider'а.
// Должна вызываться из App.Stop с таймаутом, чтобы batch-spanprocessor
// успел отправить накопленные span'ы.
type ShutdownFunc func(ctx context.Context) error

// noopShutdown — что возвращаем при Enable=false. Безопасно вызывать.
func noopShutdown(_ context.Context) error { return nil }

// Init настраивает global TracerProvider под cfg.Otel.
// serviceName — имя сервиса (resource.service.name); если ServiceName в cfg
// не задан, используется этот.
//
// При cfg.Otel.Enable=false возвращает noop-shutdown и nil-error: вызывающий
// код не должен ветвиться, просто всегда defer'ит результат.
func Init(ctx context.Context, cfg *config.OtelSection, serviceName string, logger logging.Logger) (ShutdownFunc, error) {
	if cfg == nil || !cfg.Enable {
		return noopShutdown, nil
	}

	if cfg.ServiceName != "" {
		serviceName = cfg.ServiceName
	}

	endpoint := cfg.OtlpEndpoint
	if endpoint == "" {
		endpoint = "localhost:4318"
	}

	opts := []otlptracehttp.Option{otlptracehttp.WithEndpoint(endpoint)}
	if cfg.OtlpInsecure {
		opts = append(opts, otlptracehttp.WithInsecure())
	}
	exporter, err := otlptrace.New(ctx, otlptracehttp.NewClient(opts...))
	if err != nil {
		return noopShutdown, fmt.Errorf("create otlp exporter: %w", err)
	}

	attrs := []attribute.KeyValue{
		semconv.ServiceNameKey.String(serviceName),
	}
	if cfg.Environment != "" {
		attrs = append(attrs, attribute.String("deployment.environment", cfg.Environment))
	}

	res, err := resource.New(ctx, resource.WithAttributes(attrs...))
	if err != nil {
		return noopShutdown, fmt.Errorf("build otel resource: %w", err)
	}

	sampleRate := cfg.SampleRate
	if sampleRate <= 0 {
		sampleRate = 0.1
	}
	if sampleRate > 1 {
		sampleRate = 1
	}
	sampler := sdktrace.ParentBased(sdktrace.TraceIDRatioBased(sampleRate))

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithSampler(sampler),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	logger.Info("otel tracing initialized",
		logger.Str("service", serviceName),
		logger.Str("endpoint", endpoint),
		logger.Str("environment", cfg.Environment),
	)

	return func(shutdownCtx context.Context) error {
		// Маленький timeout, чтобы не блокировать shutdown больше необходимого.
		c, cancel := context.WithTimeout(shutdownCtx, 5*time.Second)
		defer cancel()
		if err := tp.Shutdown(c); err != nil {
			return fmt.Errorf("otel tracer shutdown: %w", err)
		}
		return nil
	}, nil
}
