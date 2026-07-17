package grpcsender_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"nexus/internal/platform/config"
	"nexus/internal/platform/grpcsender"
	"nexus/internal/platform/logging"
	senderv1 "nexus/proto/sender/v1"
)

// echoServer — минимальный SenderServiceServer: тело запроса == тело ответа.
// Изолирует gRPC-ТРАНСПОРТ (без usecase) для проверки лимитов размера сообщения.
type echoServer struct {
	senderv1.UnimplementedSenderServiceServer
}

func (echoServer) Send(_ context.Context, req *senderv1.SendRequest) (*senderv1.SendResponse, error) {
	return &senderv1.SendResponse{StatusCode: 200, Body: req.GetBody()}, nil
}

// startEchoGRPC поднимает loopback gRPC-сервер с лимитом сообщения maxMsg (как в
// sender/app.go) и возвращает адрес + stop.
func startEchoGRPC(t *testing.T, maxMsg int) (string, func()) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer(
		grpc.MaxRecvMsgSize(maxMsg),
		grpc.MaxSendMsgSize(maxMsg),
	)
	senderv1.RegisterSenderServiceServer(srv, echoServer{})
	go func() { _ = srv.Serve(lis) }()
	return lis.Addr().String(), srv.Stop
}

func largeBody(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('a' + i%26)
	}
	return b
}

// TestGRPCSender_LargeBody_RoundTrip — §42: тело > дефолтного gRPC-лимита (4 МиБ)
// проходит в ОБЕ стороны (запрос и ответ), когда лимит поднят и на клиенте
// (grpcsender.New), и на сервере. Воспроизводит реальный путь Receiver↔Sender.
func TestGRPCSender_LargeBody_RoundTrip(t *testing.T) {
	t.Parallel()
	const maxMsg = 64 * 1024 * 1024
	addr, stop := startEchoGRPC(t, maxMsg)
	defer stop()

	cl, err := grpcsender.New(&config.ReceiverSenderGRPCConfig{
		Addr:            addr,
		PoolSize:        1,
		MaxMessageBytes: maxMsg,
	}, logging.NewNoop())
	require.NoError(t, err)
	defer func() { _ = cl.Close() }()

	body := largeBody(8 * 1024 * 1024) // 8 МиБ > 4 МиБ дефолт
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := cl.Send(ctx, &senderv1.SendRequest{Id: "x", Body: body})
	require.NoError(t, err, "8 МиБ запрос+ответ должны пройти с поднятым лимитом")
	require.Len(t, resp.GetBody(), len(body))
	assert.Equal(t, body, resp.GetBody())
}

// TestGRPCSender_DefaultClient_ResourceExhausted — контроль: клиент с ДЕФОЛТНЫМ
// лимитом (4 МиБ) на том же сервере падает на большом ОТВЕТЕ с ResourceExhausted
// (ровно ошибка из Sentry). Подтверждает, что поднятый лимит необходим.
func TestGRPCSender_DefaultClient_ResourceExhausted(t *testing.T) {
	t.Parallel()
	const maxMsg = 64 * 1024 * 1024
	addr, stop := startEchoGRPC(t, maxMsg)
	defer stop()

	cc, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer func() { _ = cc.Close() }()
	raw := senderv1.NewSenderServiceClient(cc)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = raw.Send(ctx, &senderv1.SendRequest{Id: "x", Body: largeBody(8 * 1024 * 1024)})
	require.Error(t, err)
	assert.Equal(t, codes.ResourceExhausted, status.Code(err))
}
