package grpcsender_test

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	"nexus/internal/domain"
	"nexus/internal/platform/config"
	"nexus/internal/platform/grpcsender"
	"nexus/internal/platform/logging"
	senderv1 "nexus/proto/sender/v1"
)

// countingServer — Sender, считающий вызовы: доказательство, что ожидание
// готовности не порождает повторов у получателя.
type countingServer struct {
	senderv1.UnimplementedSenderServiceServer
	calls atomic.Int32
}

func (s *countingServer) Send(context.Context, *senderv1.SendRequest) (*senderv1.SendResponse, error) {
	s.calls.Add(1)
	return &senderv1.SendResponse{StatusCode: 200}, nil
}

// serveOn поднимает Sender на заданном адресе — с gRPC-health в SERVING, как
// настоящий (клиент балансирует по health, §93.4). Повторный bind того же
// порта сразу после остановки прежнего сервера на Windows может ненадолго
// отказывать — поэтому несколько попыток.
func serveOn(t *testing.T, addr string, srv senderv1.SenderServiceServer) (*grpc.Server, *health.Server) {
	t.Helper()
	var lis net.Listener
	var err error
	for range 50 {
		if lis, err = net.Listen("tcp", addr); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.NoError(t, err)
	g := grpc.NewServer()
	senderv1.RegisterSenderServiceServer(g, srv)
	hs := health.NewServer()
	healthpb.RegisterHealthServer(g, hs)
	go func() { _ = g.Serve(lis) }()
	return g, hs
}

// freeAddr — адрес, на котором сейчас никто не слушает.
func freeAddr(t *testing.T) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := lis.Addr().String()
	require.NoError(t, lis.Close())
	return addr
}

// recordingObserver — счётчик исходов ожидания.
type recordingObserver struct {
	mu       sync.Mutex
	outcomes []string
}

func (o *recordingObserver) IncSenderReadyWait(outcome string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.outcomes = append(o.outcomes, outcome)
}

func (o *recordingObserver) all() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.outcomes...)
}

func newClient(t *testing.T, addr string, readyWaitMs int, obs grpcsender.ReadyWaitObserver) *grpcsender.Client {
	t.Helper()
	cl, err := grpcsender.New(&config.ReceiverSenderGRPCConfig{
		Addr: addr, PoolSize: 2, KeepaliveTimeSec: 30, KeepaliveTimeoutSec: 10, ReadyWaitMs: readyWaitMs,
	}, logging.NewNoop(), grpcsender.WithReadyWaitObserver(obs))
	require.NoError(t, err)
	t.Cleanup(func() { _ = cl.Close() })
	return cl
}

// TestSend_SurvivesSenderRestart — §109.3 (Sentry NEXUS-T/S): Sender
// перезапускается так же, как при выкате (§93.5: NOT_SERVING → дренаж →
// GracefulStop), вызовы в окне рестарта ждут возвращения Sender'а и уходят
// ровно по одному разу. Раньше они падали мгновенно: сперва «health check
// failure» на время дренажа, затем «connection refused» (Unavailable → 502).
func TestSend_SurvivesSenderRestart(t *testing.T) {
	t.Parallel()

	addr := freeAddr(t)
	first := &countingServer{}
	g1, hs1 := serveOn(t, addr, first)

	obs := &recordingObserver{}
	cl := newClient(t, addr, 10_000, obs)
	_, err := cl.Send(context.Background(), &senderv1.SendRequest{NodePath: "vika/telephony"})
	require.NoError(t, err, "первый вызов при живом Sender'е")

	hs1.Shutdown()                     // дренаж: реплика выходит из ротации
	time.Sleep(100 * time.Millisecond) // клиент успевает увидеть NOT_SERVING
	time.AfterFunc(200*time.Millisecond, g1.GracefulStop)
	second := &countingServer{}
	time.AfterFunc(1500*time.Millisecond, func() {
		g2, _ := serveOn(t, addr, second)
		t.Cleanup(g2.Stop)
	})

	start := time.Now()
	for range 3 {
		_, err = cl.Send(context.Background(), &senderv1.SendRequest{NodePath: "vika/telephony"})
		require.NoError(t, err, "вызов в окне рестарта дождался Sender'а")
	}
	assert.GreaterOrEqual(t, time.Since(start), time.Second, "вызов действительно ждал рестарта")
	assert.Equal(t, int32(1), first.calls.Load(), "дренированной реплике новых вызовов нет")
	assert.Equal(t, int32(3), second.calls.Load(), "ровно по одному вызову — без повторов")
	assert.Contains(t, obs.all(), grpcsender.ReadyWaitReady)
}

// TestSend_SenderDown_Unavailable — §109.3: Sender так и не поднялся за
// ready_wait_ms → domain.ErrSenderUnavailable (Receiver ответит 503 +
// Retry-After). С выключенным ожиданием (<0) — сразу, как прежде, но с той же
// доменной ошибкой: вызов не отправлялся ни в том, ни в другом случае.
func TestSend_SenderDown_Unavailable(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		readyWaitMs int
		minElapsed  time.Duration
		wantCode    codes.Code // gRPC-код остаётся в цепочке ошибки
		wantOutcome []string
	}{
		// Вызов отменил таймер ожидания — отсюда Canceled.
		{name: "ожидание истекло", readyWaitMs: 400, minElapsed: 400 * time.Millisecond, wantCode: codes.Canceled, wantOutcome: []string{grpcsender.ReadyWaitTimeout}},
		{name: "ожидание выключено", readyWaitMs: -1, wantCode: codes.Unavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			obs := &recordingObserver{}
			cl := newClient(t, freeAddr(t), tc.readyWaitMs, obs)

			start := time.Now()
			_, err := cl.Send(context.Background(), &senderv1.SendRequest{NodePath: "vika/telephony"})
			require.ErrorIs(t, err, domain.ErrSenderUnavailable)
			assert.Equal(t, tc.wantCode, status.Code(err))
			assert.GreaterOrEqual(t, time.Since(start), tc.minElapsed)
			assert.Less(t, time.Since(start), 5*time.Second)
			assert.Equal(t, tc.wantOutcome, obs.all())
		})
	}
}

// TestSend_CanceledWhileWaiting — §109.3/§109.4: клиент ушёл, пока вызов ждал
// Sender'а — ожидание прерывается сразу, ошибка — отмена, а не «Sender
// недоступен».
func TestSend_CanceledWhileWaiting(t *testing.T) {
	t.Parallel()

	obs := &recordingObserver{}
	cl := newClient(t, freeAddr(t), 10_000, obs)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)

	start := time.Now()
	_, err := cl.Send(ctx, &senderv1.SendRequest{NodePath: "vika/telephony"})
	require.ErrorIs(t, err, context.Canceled)
	assert.NotErrorIs(t, err, domain.ErrSenderUnavailable)
	assert.Less(t, time.Since(start), 3*time.Second)
	assert.Equal(t, []string{grpcsender.ReadyWaitCanceled}, obs.all())
}

// blockingServer — Sender, который держит вызов, пока тест не остановит
// сервер: так выглядит рестарт посреди долгого вызова.
type blockingServer struct {
	senderv1.UnimplementedSenderServiceServer
	entered chan struct{}
}

func (s *blockingServer) Send(ctx context.Context, _ *senderv1.SendRequest) (*senderv1.SendResponse, error) {
	close(s.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestSend_BreakMidCall_NotSenderUnavailable — §109.3: обрыв транспорта
// посреди вызова (соединение было готово, вызов ушёл) НЕ становится «Sender
// недоступен»: исход у получателя неизвестен, повтор клиенту не предлагается,
// ошибка остаётся прежней (урок §82.1 — такие обрывы нельзя прятать).
func TestSend_BreakMidCall_NotSenderUnavailable(t *testing.T) {
	t.Parallel()

	addr := freeAddr(t)
	srv := &blockingServer{entered: make(chan struct{})}
	g, _ := serveOn(t, addr, srv)
	cl := newClient(t, addr, 10_000, nil)

	go func() {
		<-srv.entered
		g.Stop()
	}()
	_, err := cl.Send(context.Background(), &senderv1.SendRequest{NodePath: "vika/slow"})
	require.Error(t, err)
	assert.Equal(t, codes.Unavailable, status.Code(err))
	assert.NotErrorIs(t, err, domain.ErrSenderUnavailable)
}
