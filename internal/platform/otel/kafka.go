package otel

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.27.0"
	"go.opentelemetry.io/otel/trace"
)

// stringMapCarrier — TextMapCarrier поверх map[string]string. Подходит для
// Kafka headers, которые наш Producer.Produce принимает как
// map[string]string и сам конвертирует в []kafka.Header.
type stringMapCarrier map[string]string

func (c stringMapCarrier) Get(key string) string  { return c[key] }
func (c stringMapCarrier) Set(key, value string)  { c[key] = value }
func (c stringMapCarrier) Keys() []string {
	out := make([]string, 0, len(c))
	for k := range c {
		out = append(out, k)
	}
	return out
}

var _ propagation.TextMapCarrier = stringMapCarrier(nil)

// InjectKafkaHeaders инжектит propagator-keys в map, который потом будет
// упакован в Kafka headers. headers НЕ должен быть nil — выделите map
// заранее.
//
// Используется в Receiver перед Producer.Produce, чтобы Sender (consumer)
// мог связать обработку с trace'ом исходного запроса.
func InjectKafkaHeaders(ctx context.Context, headers map[string]string) {
	if headers == nil {
		return
	}
	otel.GetTextMapPropagator().Inject(ctx, stringMapCarrier(headers))
}

// ExtractKafkaHeaders экстрактит propagator-keys из Kafka-headers (любой
// map-like) в context для дальнейшего использования (Tracer.Start будет
// видеть parent span'а).
func ExtractKafkaHeaders(ctx context.Context, headers map[string]string) context.Context {
	if len(headers) == 0 {
		return ctx
	}
	return otel.GetTextMapPropagator().Extract(ctx, stringMapCarrier(headers))
}

// StartKafkaProducerSpan открывает producer-span вокруг publish'а в
// Kafka-топик; при no-op TP — дешёвый no-op.
func StartKafkaProducerSpan(ctx context.Context, topic string) (context.Context, func(err error)) {
	tracer := otel.Tracer("databus/kafka.producer")
	ctx, span := tracer.Start(ctx, "kafka.publish "+topic,
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			semconv.MessagingSystemKafka,
			semconv.MessagingDestinationName(topic),
			semconv.MessagingOperationTypePublish,
		),
	)
	return ctx, func(err error) {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}
}

// StartKafkaConsumerSpan открывает consumer-span. Вызывать ПОСЛЕ
// ExtractKafkaHeaders, чтобы span'у был известен parent.
func StartKafkaConsumerSpan(ctx context.Context, topic string) (context.Context, func(err error)) {
	tracer := otel.Tracer("databus/kafka.consumer")
	ctx, span := tracer.Start(ctx, "kafka.consume "+topic,
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			semconv.MessagingSystemKafka,
			semconv.MessagingDestinationName(topic),
			semconv.MessagingOperationTypeProcess,
		),
	)
	return ctx, func(err error) {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}
}
