package grpcsender_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"nexus/internal/platform/config"
	"nexus/internal/platform/grpcsender"
	"nexus/internal/platform/logging"
	senderv1 "nexus/proto/sender/v1"
)

// slowServer — SenderServiceServer, отвечающий с задержкой: проверяем, что
// gRPC-слой не обрывает долгий вызов.
type slowServer struct {
	senderv1.UnimplementedSenderServiceServer
	delay time.Duration
}

func (s slowServer) Send(ctx context.Context, _ *senderv1.SendRequest) (*senderv1.SendResponse, error) {
	select {
	case <-time.After(s.delay):
		return &senderv1.SendResponse{StatusCode: 200}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func startSlowGRPC(t *testing.T, delay time.Duration) (string, func()) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer()
	senderv1.RegisterSenderServiceServer(srv, slowServer{delay: delay})
	go func() { _ = srv.Serve(lis) }()
	return lis.Addr().String(), srv.Stop
}

// TestGRPCSender_ConfigTimeoutDoesNotCapCall фиксирует контракт §4.39: клиент НЕ
// ставит дедлайн из `sender_grpc.timeout_ms` — вызов ограничен только контекстом
// вызывающего и per-node `timeout_ms` внутри Sender'а.
//
// Тест-страховка от «починки» мёртвого параметра: наивное
// `context.WithTimeout(ctx, cfg.TimeoutMs)` в Send обрежет боевые узлы с
// timeout_ms=600000 на 30-й секунде — ровно тот класс обрыва, который чинили в
// Sentry NEXUS-8. Здесь конфиг (50 мс) втрое короче ответа сервера (300 мс):
// пройдёт только реализация без дедлайна.
func TestGRPCSender_ConfigTimeoutDoesNotCapCall(t *testing.T) {
	t.Parallel()

	const serverDelay = 300 * time.Millisecond
	addr, stop := startSlowGRPC(t, serverDelay)
	defer stop()

	cl, err := grpcsender.New(&config.ReceiverSenderGRPCConfig{
		Addr:                addr,
		PoolSize:            1,
		TimeoutMs:           50, // короче ответа сервера — не должен ничего капать
		KeepaliveTimeSec:    30,
		KeepaliveTimeoutSec: 10,
	}, logging.NewNoop())
	require.NoError(t, err)
	defer func() { _ = cl.Close() }()

	start := time.Now()
	resp, err := cl.Send(context.Background(), &senderv1.SendRequest{NodePath: "partner/slow"})
	elapsed := time.Since(start)

	require.NoError(t, err, "cfg.TimeoutMs не должен обрывать вызов")
	assert.Equal(t, int32(200), resp.GetStatusCode())
	assert.GreaterOrEqual(t, elapsed, serverDelay, "вызов дождался медленного сервера")
}

// TestGRPCSender_CallerContextCancelsRPC — обратная сторона контракта: вызов
// живёт ровно столько, сколько живёт контекст вызывающего. Так боевой обрыв
// клиента (клиент закрыл HTTP-соединение → gin отменил Request.Context())
// доезжает до Sender'а и гасит исходящий запрос — в CH это `context canceled`.
func TestGRPCSender_CallerContextCancelsRPC(t *testing.T) {
	t.Parallel()

	addr, stop := startSlowGRPC(t, 5*time.Second)
	defer stop()

	cl, err := grpcsender.New(&config.ReceiverSenderGRPCConfig{
		Addr: addr, PoolSize: 1, KeepaliveTimeSec: 30, KeepaliveTimeoutSec: 10,
	}, logging.NewNoop())
	require.NoError(t, err)
	defer func() { _ = cl.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err = cl.Send(ctx, &senderv1.SendRequest{NodePath: "partner/slow"})
	require.Error(t, err)
	assert.Less(t, time.Since(start), 3*time.Second, "оборвался по контексту вызывающего, а не по серверной задержке")
}
