package rdns

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
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

// newTestResolverRedis — резолвер с настоящим (in-memory) Redis: ResolveNow
// кеширует только в Redis, без него кеш в тестах не наблюдаем.
func newTestResolverRedis(t *testing.T, lookup func(context.Context, string) ([]string, error)) (*Resolver, *miniredis.Miniredis) {
	t.Helper()
	srv := miniredis.RunT(t)
	rc := goredis.NewClient(&goredis.Options{Addr: srv.Addr()})
	t.Cleanup(func() { _ = rc.Close() })
	r := New(context.Background(), rc, Config{
		Timeout: time.Second, CacheTTL: time.Minute, NegativeTTL: time.Minute,
	}, logging.NewNoop(), WithLookupFunc(lookup))
	return r, srv
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

// §106.7: публичный адрес без PTR (интернет-клиент, сканер из журнала отказов
// §94, подделанный X-Forwarded-For) NetBIOS-запросом не опрашивается —
// UDP/137 наружу из шины не уходит. Частные сети RFC 1918 — опрашиваются.
func TestResolver_NetBIOS_OnlyPrivateIPv4(t *testing.T) {
	t.Parallel()
	tests := []struct {
		ip     string
		asked  bool
		wantNB string
	}{
		{ip: "10.1.2.3", asked: true, wantNB: "HOST"},
		{ip: "172.16.0.9", asked: true, wantNB: "HOST"},
		{ip: "192.168.2.5", asked: true, wantNB: "HOST"},
		{ip: "8.8.8.8", asked: false},
		{ip: "100.64.0.1", asked: false}, // CGNAT — не RFC 1918
		{ip: "169.254.1.1", asked: false},
		{ip: "::ffff:8.8.8.8", asked: false},
	}
	for _, tc := range tests {
		t.Run(tc.ip, func(t *testing.T) {
			t.Parallel()
			var ptrCalls, nbCalls atomic.Int32
			r := newTestResolverNB(nxdomain(&ptrCalls), netbiosStub(&nbCalls, "HOST", nil), "")
			assert.Equal(t, tc.wantNB, r.ResolveNow(context.Background(), tc.ip))
			assert.EqualValues(t, 1, ptrCalls.Load(), "PTR спрашивается у любого адреса")
			if tc.asked {
				assert.EqualValues(t, 1, nbCalls.Load())
			} else {
				assert.Zero(t, nbCalls.Load(), "UDP/137 на не-частный адрес не уходит")
			}
		})
	}
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
	assert.EqualValues(t, 1, calls.Load())
}

// §106.3: ResolveNow кладёт имя в Redis, но НЕ в L1. Дозаполнение перебирает
// до тысячи исторических IP на таблицу: в L1 они вытесняли бы IP живого
// трафика (переполнение сбрасывает L1 целиком). Горячий путь берёт имя из
// Redis своим фоновым резолвом — без нового DNS-запроса.
func TestResolver_ResolveNow_WritesRedisNotL1(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	r, srv := newTestResolverRedis(t, func(context.Context, string) ([]string, error) {
		calls.Add(1)
		return []string{"srv-a.example."}, nil
	})

	assert.Equal(t, "srv-a.example", r.ResolveNow(context.Background(), "10.9.9.9"))
	r.mu.Lock()
	_, inL1 := r.l1["10.9.9.9"]
	r.mu.Unlock()
	assert.False(t, inL1, "ResolveNow не занимает L1 живого трафика")
	got, err := srv.Get(keyPrefix + "10.9.9.9")
	require.NoError(t, err)
	assert.Equal(t, "srv-a.example", got, "имя в общем кеше реплик")

	// Горячий путь: промах L1 → фоновый резолв находит имя в Redis.
	assert.Empty(t, r.Lookup("10.9.9.9"))
	require.Eventually(t, func() bool { return r.Lookup("10.9.9.9") == "srv-a.example" },
		2*time.Second, 10*time.Millisecond)
	assert.EqualValues(t, 1, calls.Load(), "повторного DNS-запроса нет — имя из Redis")
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
}

// Отмена ПОСРЕДИ цепочки: PTR вернул ошибку из-за отмены — негатив не
// кладётся в кеш, следующий резолв спрашивает DNS заново.
func TestResolver_ResolveNow_CancelledMidChain_NotCached(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	r, srv := newTestResolverRedis(t, func(lctx context.Context, _ string) ([]string, error) {
		if calls.Add(1) == 1 {
			cancel()
			<-lctx.Done()
			return nil, lctx.Err()
		}
		return []string{"late.example."}, nil
	})

	assert.Empty(t, r.ResolveNow(ctx, "10.1.2.5"))
	assert.False(t, srv.Exists(keyPrefix+"10.1.2.5"), "прерванный резолв не оставил негатив в Redis")
	assert.Equal(t, "late.example", r.ResolveNow(context.Background(), "10.1.2.5"))
	assert.EqualValues(t, 2, calls.Load())
}
