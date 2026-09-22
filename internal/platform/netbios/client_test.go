package netbios

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/logging"
)

// fakeServer — UDP-ответчик NBSTAT на 127.0.0.1 со случайным портом.
// handle получает каждый запрос и сам решает, что (и сколько раз) ответить.
func fakeServer(t *testing.T, handle func(pc net.PacketConn, req []byte, from net.Addr)) int {
	t.Helper()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 1024)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return // сокет закрыт в Cleanup
			}
			handle(pc, append([]byte(nil), buf[:n]...), from)
		}
	}()
	return pc.LocalAddr().(*net.UDPAddr).Port
}

// reply отвечает на запрос ответом с тем же номером транзакции.
func reply(names []Name) func(net.PacketConn, []byte, net.Addr) {
	return func(pc net.PacketConn, req []byte, from net.Addr) {
		tx := uint16(req[0])<<8 | uint16(req[1])
		_, _ = pc.WriteTo(validSpec(tx, names).bytes(), from)
	}
}

func newTestClient(port int, timeout time.Duration) *Client {
	return New(Config{Port: port, Timeout: timeout}, logging.NewNoop(), WithTransactionSeed(0x1233))
}

func TestClient_NodeStatus_FakeServer(t *testing.T) {
	t.Parallel()
	var gotReq atomic.Value
	port := fakeServer(t, func(pc net.PacketConn, req []byte, from net.Addr) {
		gotReq.Store(req)
		reply(sqlDevNames())(pc, req, from)
	})
	c := newTestClient(port, 2*time.Second)

	names, err := c.NodeStatus(context.Background(), "127.0.0.1")
	require.NoError(t, err)
	assert.Equal(t, sqlDevNames(), names)
	assert.Equal(t, BuildNodeStatusRequest(0x1234), gotReq.Load(), "первый запрос — seed+1, 50 байт golden")

	host, err := c.Hostname(context.Background(), "127.0.0.1")
	require.NoError(t, err)
	assert.Equal(t, "SRV-D-SQLDEV-HQ", host)
}

func TestClient_Hostname_NoUniqueName(t *testing.T) {
	t.Parallel()
	port := fakeServer(t, reply([]Name{{Name: "VZ78", Group: true}}))
	host, err := newTestClient(port, 2*time.Second).Hostname(context.Background(), "127.0.0.1")
	require.NoError(t, err, "ответ без уникального <00> — не ошибка, просто нет имени")
	assert.Empty(t, host)
}

func TestClient_NodeStatus_Timeout(t *testing.T) {
	t.Parallel()
	port := fakeServer(t, func(net.PacketConn, []byte, net.Addr) {}) // молчит
	c := newTestClient(port, 200*time.Millisecond)

	start := time.Now()
	_, err := c.NodeStatus(context.Background(), "127.0.0.1")
	elapsed := time.Since(start)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.GreaterOrEqual(t, elapsed, 150*time.Millisecond)
	assert.Less(t, elapsed, 2*time.Second)
}

func TestClient_NodeStatus_CtxCancel(t *testing.T) {
	t.Parallel()
	port := fakeServer(t, func(net.PacketConn, []byte, net.Addr) {})
	c := newTestClient(port, 10*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)

	start := time.Now()
	_, err := c.NodeStatus(ctx, "127.0.0.1")
	require.ErrorIs(t, err, context.Canceled)
	assert.Less(t, time.Since(start), time.Second, "отмена снимает блокировку чтения, не дожидаясь таймаута")
}

func TestClient_NodeStatus_IgnoresMismatchedTxID(t *testing.T) {
	t.Parallel()
	port := fakeServer(t, func(pc net.PacketConn, req []byte, from net.Addr) {
		tx := uint16(req[0])<<8 | uint16(req[1])
		_, _ = pc.WriteTo(validSpec(tx+100, []Name{{Name: "STALE"}}).bytes(), from)
		_, _ = pc.WriteTo(validSpec(tx, []Name{{Name: "FRESH"}}).bytes(), from)
	})
	host, err := newTestClient(port, 2*time.Second).Hostname(context.Background(), "127.0.0.1")
	require.NoError(t, err)
	assert.Equal(t, "FRESH", host, "опоздавший ответ с чужим номером пропускается")
}

func TestClient_NodeStatus_RCode(t *testing.T) {
	t.Parallel()
	port := fakeServer(t, func(pc net.PacketConn, req []byte, from net.Addr) {
		tx := uint16(req[0])<<8 | uint16(req[1])
		s := validSpec(tx, sqlDevNames())
		s.flags = 0x8403
		_, _ = pc.WriteTo(s.bytes(), from)
	})
	_, err := newTestClient(port, 2*time.Second).NodeStatus(context.Background(), "127.0.0.1")
	require.ErrorIs(t, err, ErrRCode)
}

func TestClient_NodeStatus_ClosedPort(t *testing.T) {
	t.Parallel()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	port := pc.LocalAddr().(*net.UDPAddr).Port
	require.NoError(t, pc.Close())

	// ICMP port unreachable даёт быструю ошибку чтения (Linux — ECONNREFUSED,
	// Windows — WSAECONNRESET); в худшем случае сработает таймаут. Важно одно:
	// ошибка, а не имя.
	start := time.Now()
	_, err = newTestClient(port, time.Second).NodeStatus(context.Background(), "127.0.0.1")
	require.Error(t, err)
	assert.Less(t, time.Since(start), 3*time.Second)
}
