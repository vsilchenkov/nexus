package rdns

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/logging"
)

// nxdomain — PTR-заглушка корпоративного DNS без обратной записи (§106.1).
func nxdomain(calls *atomic.Int32) func(context.Context, string) ([]string, error) {
	return func(context.Context, string) ([]string, error) {
		calls.Add(1)
		return nil, errors.New("NXDOMAIN")
	}
}

// netbiosStub отвечает именем name (или ошибкой err) и считает вызовы.
func netbiosStub(calls *atomic.Int32, name string, err error) NetBIOSLookupFunc {
	return func(context.Context, string) (string, error) {
		calls.Add(1)
		return name, err
	}
}

func newTestResolverNB(lookup func(context.Context, string) ([]string, error), nb NetBIOSLookupFunc, suffix string) *Resolver {
	return New(context.Background(), nil /* redis: режим «только L1» */, Config{
		Timeout:     time.Second,
		CacheTTL:    time.Minute,
		NegativeTTL: time.Minute,
	}, logging.NewNoop(), WithLookupFunc(lookup), WithNetBIOS(nb, suffix))
}

func TestResolver_NetBIOS_NotCalledWhenPTRSucceeds(t *testing.T) {
	t.Parallel()
	var nbCalls atomic.Int32
	r := newTestResolverNB(func(context.Context, string) ([]string, error) {
		return []string{"SRV-P-1C-NODE4.vz78.vozovoz.ru."}, nil
	}, netbiosStub(&nbCalls, "WRONG", nil), "vz78.vozovoz.ru")

	assert.Equal(t, "SRV-P-1C-NODE4.vz78.vozovoz.ru", r.ResolveNow(context.Background(), "192.168.86.113"))
	assert.Zero(t, nbCalls.Load(), "PTR ответил — NetBIOS не спрашиваем")
}

func TestResolver_NetBIOS_CalledWhenPTREmpty_SuffixAppended(t *testing.T) {
	t.Parallel()
	var ptrCalls, nbCalls atomic.Int32
	r := newTestResolverNB(nxdomain(&ptrCalls), netbiosStub(&nbCalls, "SRV-D-1C", nil), ".vz78.vozovoz.ru.")

	// Горячий путь: промах → "" сразу, имя из NetBIOS приходит фоном.
	assert.Empty(t, r.Lookup("192.168.2.5"))
	require.Eventually(t, func() bool {
		return r.Lookup("192.168.2.5") == "SRV-D-1C.vz78.vozovoz.ru"
	}, 2*time.Second, 10*time.Millisecond, "точки по краям суффикса обрезаны")
	assert.EqualValues(t, 1, ptrCalls.Load())
	assert.EqualValues(t, 1, nbCalls.Load())
}

func TestResolver_NetBIOS_NoSuffix_ShortName(t *testing.T) {
	t.Parallel()
	var ptrCalls, nbCalls atomic.Int32
	r := newTestResolverNB(nxdomain(&ptrCalls), netbiosStub(&nbCalls, "PC7821518", nil), "")
	assert.Equal(t, "PC7821518", r.ResolveNow(context.Background(), "192.168.3.15"))
}

func TestResolver_NetBIOS_Disabled_NotCalled(t *testing.T) {
	t.Parallel()
	var ptrCalls atomic.Int32
	r := newTestResolverNB(nxdomain(&ptrCalls), nil, "vz78.vozovoz.ru")
	assert.Empty(t, r.ResolveNow(context.Background(), "192.168.2.5"))
	assert.EqualValues(t, 1, ptrCalls.Load())
}

func TestResolver_NetBIOS_BothFail_NegativeCached(t *testing.T) {
	t.Parallel()
	var ptrCalls, nbCalls atomic.Int32
	r := newTestResolverNB(nxdomain(&ptrCalls), netbiosStub(&nbCalls, "", errors.New("i/o timeout")), "")

	assert.Empty(t, r.Lookup("192.168.2.5"))
	require.Eventually(t, func() bool { return nbCalls.Load() == 1 }, 2*time.Second, 10*time.Millisecond)
	for range 5 {
		assert.Empty(t, r.Lookup("192.168.2.5"))
		assert.Empty(t, r.ResolveNow(context.Background(), "192.168.2.5"))
	}
	time.Sleep(50 * time.Millisecond) // дать шанс ошибочно запущенному фону
	assert.EqualValues(t, 1, ptrCalls.Load(), "негатив закеширован — PTR не повторяется")
	assert.EqualValues(t, 1, nbCalls.Load(), "негатив закеширован — NetBIOS не повторяется")
}

func TestResolver_NetBIOS_SkippedForIPv6(t *testing.T) {
	t.Parallel()
	var ptrCalls, nbCalls atomic.Int32
	r := newTestResolverNB(nxdomain(&ptrCalls), netbiosStub(&nbCalls, "HOST", nil), "")
	assert.Empty(t, r.ResolveNow(context.Background(), "2001:db8::1"))
	assert.EqualValues(t, 1, ptrCalls.Load())
	assert.Zero(t, nbCalls.Load(), "NetBIOS — только IPv4")
}

func TestResolver_ResolveNow_ColdCache_Blocking(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	r := newTestResolver(func(context.Context, string) ([]string, error) {
		calls.Add(1)
		return []string{"SRV-P-1C-NODE3.vz78.vozovoz.ru."}, nil
	})

	// Холодный кеш: имя возвращается сразу, без ожидания фона.
	assert.Equal(t, "SRV-P-1C-NODE3.vz78.vozovoz.ru", r.ResolveNow(context.Background(), "192.168.86.246"))
	// И кладётся в L1: горячий путь отдаёт его без нового запроса.
	assert.Equal(t, "SRV-P-1C-NODE3.vz78.vozovoz.ru", r.Lookup("192.168.86.246"))
	assert.Equal(t, "SRV-P-1C-NODE3.vz78.vozovoz.ru", r.ResolveNow(context.Background(), "192.168.86.246"))
	assert.EqualValues(t, 1, calls.Load())
}

func TestResolver_ResolveNow_NonIP_And_Loopback(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	r := newTestResolver(func(context.Context, string) ([]string, error) {
		calls.Add(1)
		return []string{"x."}, nil
	})
	for _, ip := range []string{"", "rabbitmq://host:5672/vhost", "not-an-ip", "127.0.0.1", "::1", "0.0.0.0"} {
		assert.Empty(t, r.ResolveNow(context.Background(), ip), "ip=%q", ip)
	}
	assert.Zero(t, calls.Load())
}

func TestResolver_ResolveNow_DedupsWithBackground(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	block := make(chan struct{})
	r := newTestResolver(func(context.Context, string) ([]string, error) {
		calls.Add(1)
		<-block
		return []string{"host.example."}, nil
	})

	assert.Empty(t, r.Lookup("172.16.0.9")) // фоновый резолв стартовал и держит PTR
	require.Eventually(t, func() bool { return calls.Load() == 1 }, 2*time.Second, 10*time.Millisecond)

	got := make(chan string, 1)
	go func() { got <- r.ResolveNow(context.Background(), "172.16.0.9") }()
	time.Sleep(50 * time.Millisecond) // ResolveNow присоединяется к полёту
	close(block)

	select {
	case host := <-got:
		assert.Equal(t, "host.example", host)
	case <-time.After(2 * time.Second):
		t.Fatal("ResolveNow не вернулся")
	}
	assert.EqualValues(t, 1, calls.Load(), "singleflight общий для Lookup и ResolveNow")
}

func TestResolver_ResolveNow_CancelledCtx_NotCached(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	r := newTestResolver(func(context.Context, string) ([]string, error) {
		calls.Add(1)
		return nil, errors.New("NXDOMAIN")
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	assert.Empty(t, r.ResolveNow(ctx, "10.1.2.4"))
	assert.Zero(t, calls.Load(), "отменённый ctx — без DNS")
	r.mu.Lock()
	_, cached := r.l1["10.1.2.4"]
	r.mu.Unlock()
	assert.False(t, cached, "отмена не кешируется")
}

// Отмена ПОСРЕДИ цепочки: PTR вернул ошибку из-за отмены — негатив не
// кладётся в кеш, следующий резолв спрашивает DNS заново.
func TestResolver_ResolveNow_CancelledMidChain_NotCached(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	r := newTestResolver(func(lctx context.Context, _ string) ([]string, error) {
		if calls.Add(1) == 1 {
			cancel()
			<-lctx.Done()
			return nil, lctx.Err()
		}
		return []string{"late.example."}, nil
	})

	assert.Empty(t, r.ResolveNow(ctx, "10.1.2.5"))
	assert.Equal(t, "late.example", r.ResolveNow(context.Background(), "10.1.2.5"),
		"прерванный резолв не оставил негатив в кеше")
	assert.EqualValues(t, 2, calls.Load())
}
