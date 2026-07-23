package rdns

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/logging"
)

func newTestResolver(lookup func(ctx context.Context, ip string) ([]string, error)) *Resolver {
	return New(context.Background(), nil /* redis: режим «только L1» */, Config{
		Timeout:     time.Second,
		CacheTTL:    time.Minute,
		NegativeTTL: time.Minute,
	}, logging.NewNoop(), WithLookupFunc(lookup))
}

func TestResolver_Lookup_NonIP(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	r := newTestResolver(func(context.Context, string) ([]string, error) {
		calls.Add(1)
		return []string{"x."}, nil
	})

	// Не-IP значения (rabbitmq://… §27.10, пустые, мусор) → "" и НОЛЬ DNS-вызовов.
	for _, ip := range []string{"", "rabbitmq://host:5672/vhost", "not-an-ip", "10.0.0"} {
		assert.Empty(t, r.Lookup(ip), "ip=%q", ip)
	}
	assert.Zero(t, calls.Load(), "lookupAddr не должен вызываться для не-IP")
}

func TestResolver_Lookup_ResolvesInBackground(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	r := newTestResolver(func(_ context.Context, ip string) ([]string, error) {
		calls.Add(1)
		require.Equal(t, "192.168.86.246", ip)
		return []string{"SRV-P-1C-NODE3.vz78.vozovoz.ru."}, nil
	})

	// Первый вызов — промах: "" немедленно, резолв уходит в фон.
	assert.Empty(t, r.Lookup("192.168.86.246"))

	// После завершения фонового резолва имя приходит из L1, точка PTR обрезана.
	require.Eventually(t, func() bool {
		return r.Lookup("192.168.86.246") == "SRV-P-1C-NODE3.vz78.vozovoz.ru"
	}, 2*time.Second, 10*time.Millisecond)
	assert.EqualValues(t, 1, calls.Load(), "ровно один PTR-lookup")
}

func TestResolver_Lookup_NegativeCache(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	r := newTestResolver(func(context.Context, string) ([]string, error) {
		calls.Add(1)
		return nil, errors.New("NXDOMAIN")
	})

	assert.Empty(t, r.Lookup("10.1.2.3"))
	require.Eventually(t, func() bool { return calls.Load() == 1 }, 2*time.Second, 10*time.Millisecond)

	// Негативный результат закеширован: повторные Lookup не дёргают DNS до TTL.
	for range 5 {
		assert.Empty(t, r.Lookup("10.1.2.3"))
	}
	time.Sleep(50 * time.Millisecond) // дать шанс ошибочно запущенному фону
	assert.EqualValues(t, 1, calls.Load(), "повторных PTR-lookup быть не должно")
}

func TestResolver_Lookup_ConcurrentSingleResolve(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	block := make(chan struct{})
	r := newTestResolver(func(context.Context, string) ([]string, error) {
		calls.Add(1)
		<-block // держим резолв, пока конкуренты ломятся
		return []string{"host.example."}, nil
	})

	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			assert.Empty(t, r.Lookup("172.16.0.1"))
		}()
	}
	wg.Wait()
	close(block)

	require.Eventually(t, func() bool {
		return r.Lookup("172.16.0.1") == "host.example"
	}, 2*time.Second, 10*time.Millisecond)
	assert.EqualValues(t, 1, calls.Load(), "pending+singleflight дедуплицируют конкурентный резолв")
}

func TestResolver_Lookup_EmptyPTRAnswer(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	r := newTestResolver(func(context.Context, string) ([]string, error) {
		calls.Add(1)
		return []string{}, nil // пустой успешный ответ = нет имени
	})

	assert.Empty(t, r.Lookup("10.9.9.9"))
	require.Eventually(t, func() bool { return calls.Load() == 1 }, 2*time.Second, 10*time.Millisecond)
	assert.Empty(t, r.Lookup("10.9.9.9"), "пустой ответ кешируется как негативный")
	time.Sleep(50 * time.Millisecond)
	assert.EqualValues(t, 1, calls.Load())
}
