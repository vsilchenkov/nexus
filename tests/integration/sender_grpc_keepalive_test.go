//go:build integration

package integration

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
	"nexus/internal/sender"
	senderv1 "nexus/proto/sender/v1"
)

// keepaliveSlowServer — приёмник, который держит unary-вызов заданное время.
// Отвечает, только если дожил; отмену контекста возвращает как ошибку, чтобы
// тест видел разницу между «сервер доработал» и «транспорт оборвали».
type keepaliveSlowServer struct {
	senderv1.UnimplementedSenderServiceServer
	hold time.Duration
}

func (s keepaliveSlowServer) Send(ctx context.Context, _ *senderv1.SendRequest) (*senderv1.SendResponse, error) {
	select {
	case <-time.After(s.hold):
		return &senderv1.SendResponse{StatusCode: 200}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// TestSender_GRPCKeepalive_LongUnaryCallSurvives — регресс §82.1: долгий
// sync-вызов не должен обрываться keepalive-политикой gRPC-сервера.
//
// Что воспроизводится. До §82 Sender собирал gRPC-сервер без
// `KeepaliveEnforcementPolicy`, поэтому действовал дефолт grpc-go: допустимый
// интервал между ping'ами клиента — 5 минут. Клиент (Receiver/Web) пингует
// каждые `keepalive_time_sec`, каждый такой ping сервер засчитывает нарушением
// и на ТРЕТЬЕМ шлёт `GOAWAY ENHANCE_YOUR_CALM / "too_many_pings"`, закрывая
// соединение вместе с идущим по нему вызовом. Потолок = 4×keepalive_time_sec, и
// `timeout_ms` узла на него не влияет: боевой узел с `timeout_ms=600000` рвался
// на 121 секунде напрямую в Receiver и на 91 через Web.
//
// Почему параметры именно такие. `keepalive_time_sec=10` — минимум, который
// принимает grpc-go (`internal.KeepaliveMinPingTime`), ниже он поднимет
// значение сам и тест перестанет что-либо проверять. Отсюда GOAWAY на старом
// коде приходит примерно на 40-й секунде (4×10), и удержание вызова в 50 секунд
// даёт запас, оставаясь самым коротким честным воспроизведением.
//
// Почему сервер поднимается через sender.GRPCServerOptions, а не своим
// grpc.NewServer: тест обязан ломаться и в том случае, если политику уберут из
// БОЕВОЙ сборки, а не только из grpcsender.
//
// Docker не нужен — весь обмен in-process.
func TestSender_GRPCKeepalive_LongUnaryCallSurvives(t *testing.T) {
	t.Parallel()

	const (
		clientPingSec = 10               // минимум grpc-go; GOAWAY на старом коде ≈ 4×10 = 40 c
		serverHold    = 50 * time.Second // заведомо дольше потолка
	)

	cfg := config.SenderSection{
		GRPCMaxConcurrentStreams: 100,
		GRPCMaxMessageBytes:      4 * 1024 * 1024,
		GRPCKeepalive: config.SenderGRPCKeepaliveConfig{
			EnforcementMinTimeSec: 5, // не строже клиентских 10 — как требует Validate
		},
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	srv := grpc.NewServer(sender.GRPCServerOptions(&cfg)...)
	senderv1.RegisterSenderServiceServer(srv, keepaliveSlowServer{hold: serverHold})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	cl, err := grpcsender.New(&config.ReceiverSenderGRPCConfig{
		Addr:                lis.Addr().String(),
		PoolSize:            1,
		KeepaliveTimeSec:    clientPingSec,
		KeepaliveTimeoutSec: 10,
	}, logging.NewNoop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = cl.Close() })

	// Контекст вызывающего заведомо длиннее удержания: единственный, кто может
	// оборвать вызов в этом тесте, — сам транспорт.
	ctx, cancel := context.WithTimeout(context.Background(), serverHold+30*time.Second)
	defer cancel()

	start := time.Now()
	resp, err := cl.Send(ctx, &senderv1.SendRequest{NodePath: "partner/slow"})
	elapsed := time.Since(start)

	require.NoError(t, err,
		"вызов оборван транспортом на %s: gRPC-сервер Sender'а обязан объявлять "+
			"EnforcementPolicy, иначе keepalive-ping'и клиента считаются нарушением "+
			"и соединение рвётся GOAWAY «too_many_pings» (§82.1)", elapsed.Round(time.Second))
	require.NotNil(t, resp)
	assert.Equal(t, int32(200), resp.GetStatusCode())
	assert.GreaterOrEqual(t, elapsed, serverHold,
		"вызов обязан дожить до ответа сервера, а не завершиться раньше")
}
