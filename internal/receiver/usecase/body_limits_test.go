package usecase

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
)

// До сида из app_settings действует дефолт домена, а не потолок из конфига:
// принять меньше, чем разрешил администратор, безопаснее, чем принять больше,
// пока настройки едут.
func TestBodyLimitsProvider_DefaultsBeforeSet(t *testing.T) {
	t.Parallel()

	p := NewBodyLimitsProvider(300<<20, 300<<20, logging.NewNoop())

	syncBytes, asyncBytes := p.Limits()
	assert.Equal(t, domain.BodyLimitDefaultBytes, syncBytes)
	assert.Equal(t, domain.BodyLimitDefaultBytes, asyncBytes)
}

// Потолок ниже дефолта: действует потолок — выше него шина физически не
// настроена (nginx, gRPC, Kafka, память).
func TestBodyLimitsProvider_CeilingBelowDefault(t *testing.T) {
	t.Parallel()

	p := NewBodyLimitsProvider(8, 8, logging.NewNoop())

	assert.Equal(t, 8, p.Sync())
	assert.Equal(t, 8, p.Async())
}

func TestBodyLimitsProvider_SetWithinCeiling(t *testing.T) {
	t.Parallel()

	p := NewBodyLimitsProvider(300<<20, 300<<20, logging.NewNoop())
	p.Set(150<<20, 120<<20)

	syncBytes, asyncBytes := p.Limits()
	assert.Equal(t, 150<<20, syncBytes)
	assert.Equal(t, 120<<20, asyncBytes)
}

// Значение могло быть сохранено, когда потолок был выше: применяем потолок,
// а не то, что лежит в настройках.
func TestBodyLimitsProvider_SetClampedByCeiling(t *testing.T) {
	t.Parallel()

	p := NewBodyLimitsProvider(100<<20, 50<<20, logging.NewNoop())
	p.Set(300<<20, 300<<20)

	syncBytes, asyncBytes := p.Limits()
	assert.Equal(t, 100<<20, syncBytes, "sync зажат своим потолком")
	assert.Equal(t, 50<<20, asyncBytes, "async зажат своим потолком")
}

// async выше sync означал бы, что запрос к узлу на паузе (§3.6) принимается в
// очередь крупнее, чем разрешено принять напрямую.
func TestBodyLimitsProvider_AsyncNeverExceedsSync(t *testing.T) {
	t.Parallel()

	p := NewBodyLimitsProvider(300<<20, 300<<20, logging.NewNoop())
	p.Set(40<<20, 200<<20)

	syncBytes, asyncBytes := p.Limits()
	assert.Equal(t, 40<<20, syncBytes)
	assert.Equal(t, 40<<20, asyncBytes, "async опущен до sync")
}

func TestBodyLimitsProvider_ZeroMeansDefault(t *testing.T) {
	t.Parallel()

	p := NewBodyLimitsProvider(300<<20, 300<<20, logging.NewNoop())
	p.Set(0, -1)

	syncBytes, asyncBytes := p.Limits()
	assert.Equal(t, domain.BodyLimitDefaultBytes, syncBytes)
	assert.Equal(t, domain.BodyLimitDefaultBytes, asyncBytes)
}

// Потолок не задан (конфиг без ключа) — проверяем только нижнюю границу,
// значение проходит как есть.
func TestBodyLimitsProvider_NoCeiling(t *testing.T) {
	t.Parallel()

	p := NewBodyLimitsProvider(0, 0, logging.NewNoop())
	p.Set(500<<20, 500<<20)

	syncBytes, asyncBytes := p.Limits()
	assert.Equal(t, 500<<20, syncBytes)
	assert.Equal(t, 500<<20, asyncBytes)
}

// Handler в части тестов собирается литералом, без провайдера: nil не должен
// приводить к панике на боевом пути.
func TestBodyLimitsProvider_NilSafe(t *testing.T) {
	t.Parallel()

	var p *BodyLimitsProvider
	require.NotPanics(t, func() {
		syncBytes, asyncBytes := p.Limits()
		assert.Equal(t, domain.BodyLimitDefaultBytes, syncBytes)
		assert.Equal(t, domain.BodyLimitDefaultBytes, asyncBytes)
		assert.Equal(t, domain.BodyLimitDefaultBytes, p.Sync())
		assert.Equal(t, domain.BodyLimitDefaultBytes, p.Async())
	})
}

// Пара лимитов обязана читаться согласованно: hot-reload меняет её на живом
// трафике, и половина старой пары с половиной новой нарушила бы инвариант
// async ≤ sync. Запускать с -race.
func TestBodyLimitsProvider_ConcurrentReadWrite(t *testing.T) {
	t.Parallel()

	p := NewBodyLimitsProvider(300<<20, 300<<20, logging.NewNoop())

	var wg sync.WaitGroup
	for i := range 4 {
		wg.Go(func() {
			for range 200 {
				p.Set((10+i)<<20, (5+i)<<20)
			}
		})
	}
	for range 4 {
		wg.Go(func() {
			for range 200 {
				syncBytes, asyncBytes := p.Limits()
				assert.LessOrEqual(t, asyncBytes, syncBytes, "инвариант async ≤ sync")
				assert.Positive(t, syncBytes)
			}
		})
	}
	wg.Wait()
}
