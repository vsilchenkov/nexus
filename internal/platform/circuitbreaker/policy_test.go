package circuitbreaker_test

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"nexus/internal/domain"
	"nexus/internal/platform/circuitbreaker"
)

// §98.5: держатель глобальной политики. Пара подменяется ЦЕЛИКОМ — RecordFailure
// пишет в состояние breaker'а обе величины разом, и «половина старой пары,
// половина новой» показала бы оператору «3 из 5 · проба через 60 с» при
// сохранённых 5/30.
func TestPolicyProvider_SetAndRead(t *testing.T) {
	t.Parallel()
	p := circuitbreaker.NewPolicyProvider(domain.BreakerPolicy{Threshold: 5, Cooldown: 30 * time.Second})
	assert.Equal(t, 5, p.Policy().Threshold)
	assert.Equal(t, 30*time.Second, p.Policy().Cooldown)

	p.Set(domain.BreakerPolicy{Threshold: 2, Cooldown: 5 * time.Second})
	assert.Equal(t, 2, p.Policy().Threshold)
	assert.Equal(t, 5*time.Second, p.Policy().Cooldown)
}

// Nil-safe: breaker, собранный без провайдера, обязан работать по значениям
// узла, а не падать.
func TestPolicyProvider_NilSafe(t *testing.T) {
	t.Parallel()
	var p *circuitbreaker.PolicyProvider
	assert.Zero(t, p.Policy().Threshold)
	assert.Zero(t, p.Policy().Cooldown)
}

// Пара всегда согласована: читатель не должен увидеть порог от одной политики и
// паузу от другой. Проверяется под -race.
func TestPolicyProvider_ConcurrentSetKeepsPairConsistent(t *testing.T) {
	t.Parallel()
	p := circuitbreaker.NewPolicyProvider(domain.BreakerPolicy{Threshold: 5, Cooldown: 5 * time.Second})
	// Обе допустимые пары: (5, 5с) и (2, 2с). Любая смесь — рассинхрон.
	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Go(func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if i%2 == 0 {
				p.Set(domain.BreakerPolicy{Threshold: 5, Cooldown: 5 * time.Second})
			} else {
				p.Set(domain.BreakerPolicy{Threshold: 2, Cooldown: 2 * time.Second})
			}
		}
	})
	for range 4 {
		wg.Go(func() {
			for range 2000 {
				got := p.Policy()
				assert.Equal(t, time.Duration(got.Threshold)*time.Second, got.Cooldown,
					"порог и пауза обязаны быть из ОДНОЙ политики")
			}
		})
	}
	time.Sleep(20 * time.Millisecond)
	close(stop)
	wg.Wait()
}
