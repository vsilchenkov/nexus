package otel

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.27.0"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// metadataCarrier — TextMapCarrier поверх grpc/metadata.MD. Нужен для
// propagation между сервисами через gRPC (traceparent / baggage в metadata,
// не в http-заголовках). gRPC metadata-keys lowercase'ятся транспортом,
// поэтому Get/Set работают через lowercase.
type metadataCarrier struct {
	md metadata.MD
}

func (m metadataCarrier) Get(key string) string {
	vals := m.md.Get(key)
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

func (m metadataCarrier) Set(key, value string) {
	m.md.Set(key, value)
}

func (m metadataCarrier) Keys() []string {
	out := make([]string, 0, len(m.md))
	for k := range m.md {
		out = append(out, k)
	}
	return out
}

// Compile-time check.
var _ propagation.TextMapCarrier = metadataCarrier{}

// UnaryClientInterceptor создаёт client-span вокруг исходящего gRPC-вызова и
// инжектит traceparent в outgoing metadata. При Enable=false — global TP
// no-op, накладные расходы минимальны.
//
// Используется в Receiver для вызовов sender.SenderService.Send.
func UnaryClientInterceptor() grpc.UnaryClientInterceptor {
	tracer := otel.Tracer("databus/grpc.client")
	propagator := otel.GetTextMapPropagator()

	return func(
		ctx context.Context,
		method string,
		req, reply any,
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		ctx, span := tracer.Start(ctx, method,
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(
				semconv.RPCSystemKey.String("grpc"),
				semconv.RPCMethod(method),
			),
		)
		defer span.End()

		// Берём существующую outgoing metadata (если есть) и докладываем
		// propagator-keys. .Copy() важен — Pairs возвращает мутируемую MD.
		md, ok := metadata.FromOutgoingContext(ctx)
		if !ok {
			md = metadata.New(nil)
		} else {
			md = md.Copy()
		}
		propagator.Inject(ctx, metadataCarrier{md})
		ctx = metadata.NewOutgoingContext(ctx, md)

		err := invoker(ctx, method, req, reply, cc, opts...)
		if err != nil {
			span.RecordError(err)
			if st, ok := status.FromError(err); ok {
				span.SetAttributes(semconv.RPCGRPCStatusCodeKey.Int(int(st.Code())))
			}
			span.SetStatus(codes.Error, err.Error())
		}
		return err
	}
}

// UnaryServerInterceptor извлекает traceparent из incoming metadata и
// создаёт server-span. Используется в Sender для входящих gRPC-вызовов.
func UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	tracer := otel.Tracer("databus/grpc.server")
	propagator := otel.GetTextMapPropagator()

	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		ctx = propagator.Extract(ctx, metadataCarrier{md})

		ctx, span := tracer.Start(ctx, info.FullMethod,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				semconv.RPCSystemKey.String("grpc"),
				semconv.RPCMethod(info.FullMethod),
			),
		)
		defer span.End()

		resp, err := handler(ctx, req)
		if err != nil {
			span.RecordError(err)
			if st, ok := status.FromError(err); ok {
				span.SetAttributes(semconv.RPCGRPCStatusCodeKey.Int(int(st.Code())))
			}
			span.SetStatus(codes.Error, err.Error())
		}
		return resp, err
	}
}
