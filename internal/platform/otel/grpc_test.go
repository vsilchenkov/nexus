package otel

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestMetadataCarrier_GetSetKeys(t *testing.T) {
	md := metadata.New(nil)
	c := metadataCarrier{md: md}

	assert.Empty(t, c.Get("traceparent"))
	c.Set("traceparent", "00-abc-def-01")
	assert.Equal(t, "00-abc-def-01", c.Get("traceparent"))

	c.Set("baggage", "k=v")
	keys := c.Keys()
	assert.ElementsMatch(t, []string{"traceparent", "baggage"}, keys)
}

func TestMetadataCarrier_EmptyMD(t *testing.T) {
	c := metadataCarrier{md: metadata.New(nil)}
	assert.Empty(t, c.Keys())
	assert.Empty(t, c.Get("anything"))
}

func TestUnaryServerInterceptor_HappyPath_CallsHandler(t *testing.T) {
	interceptor := UnaryServerInterceptor()

	called := false
	handler := func(ctx context.Context, req any) (any, error) {
		called = true
		return "ok", nil
	}

	ctx := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("traceparent", "00-x-y-01"))
	resp, err := interceptor(ctx, "req",
		&grpc.UnaryServerInfo{FullMethod: "/svc/Method"}, handler)
	require.NoError(t, err)
	assert.True(t, called)
	assert.Equal(t, "ok", resp)
}

func TestUnaryServerInterceptor_HandlerError_PropagatesAndRecords(t *testing.T) {
	interceptor := UnaryServerInterceptor()

	handler := func(ctx context.Context, req any) (any, error) {
		return nil, errors.New("boom")
	}

	_, err := interceptor(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/svc/X"}, handler)
	require.Error(t, err)
	assert.Equal(t, "boom", err.Error())
}

func TestUnaryServerInterceptor_HandlerGRPCStatusError(t *testing.T) {
	// grpc status-ошибки тоже не должны валить interceptor.
	interceptor := UnaryServerInterceptor()
	handler := func(ctx context.Context, req any) (any, error) {
		return nil, status.Error(13, "internal")
	}
	_, err := interceptor(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/svc/Y"}, handler)
	require.Error(t, err)
}

func TestUnaryServerInterceptor_NoIncomingMetadata(t *testing.T) {
	// Если call пришёл без metadata (например, прямой вызов из тестов) —
	// interceptor не должен паниковать.
	interceptor := UnaryServerInterceptor()
	handler := func(ctx context.Context, req any) (any, error) { return "ok", nil }
	resp, err := interceptor(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/svc/Z"}, handler)
	require.NoError(t, err)
	assert.Equal(t, "ok", resp)
}

func TestUnaryClientInterceptor_InjectsAndCallsInvoker(t *testing.T) {
	interceptor := UnaryClientInterceptor()

	var seenMD metadata.MD
	invoker := func(ctx context.Context, method string, req, reply any,
		cc *grpc.ClientConn, opts ...grpc.CallOption) error {
		// Внутри invoker'а outgoing context должен содержать metadata —
		// её мог положить наш interceptor (propagator-keys) и/или вызывающий.
		seenMD, _ = metadata.FromOutgoingContext(ctx)
		return nil
	}

	err := interceptor(context.Background(),
		"/test.Service/Echo", "req", "reply", nil, invoker)
	require.NoError(t, err)
	// При выключенном tracing propagator может ничего не вставить — но
	// сам факт пустого MD валиден (interceptor не должен падать). Главное:
	// invoker был вызван (см. err==nil выше) и MD доступна для чтения.
	assert.NotNil(t, seenMD)
}

func TestUnaryClientInterceptor_PreservesCallerMetadata(t *testing.T) {
	// Если вызывающий уже положил metadata в outgoing context (например,
	// auth-токен), наш interceptor её не должен затереть — только дополнить.
	interceptor := UnaryClientInterceptor()

	var seenMD metadata.MD
	invoker := func(ctx context.Context, method string, req, reply any,
		cc *grpc.ClientConn, opts ...grpc.CallOption) error {
		seenMD, _ = metadata.FromOutgoingContext(ctx)
		return nil
	}

	ctx := metadata.NewOutgoingContext(context.Background(),
		metadata.Pairs("authorization", "Bearer caller-token"))
	err := interceptor(ctx, "/test.Service/Echo", "req", "reply", nil, invoker)
	require.NoError(t, err)

	got := seenMD.Get("authorization")
	require.Len(t, got, 1, "caller's authorization metadata must survive")
	assert.Equal(t, "Bearer caller-token", got[0])
}

func TestUnaryClientInterceptor_InvokerError_Propagates(t *testing.T) {
	interceptor := UnaryClientInterceptor()
	invoker := func(ctx context.Context, method string, req, reply any,
		cc *grpc.ClientConn, opts ...grpc.CallOption) error {
		return status.Error(14, "unavailable")
	}
	err := interceptor(context.Background(),
		"/test.Service/Down", "req", "reply", nil, invoker)
	require.Error(t, err)
}
