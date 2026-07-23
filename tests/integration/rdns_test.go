//go:build integration

package integration

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/logging"
	"nexus/internal/platform/rdns"
)

// TestRDNS_RedisCache — §67: резолвер с реальным Redis. Позитивный и негативный
// результаты кешируются с корректными TTL, «холодный» процесс (новый Resolver —
// модель рестарта/соседней реплики Sender'а) берёт имя из Redis без DNS-вызова.
func TestRDNS_RedisCache(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	redisClient, cleanup := startRedis(t, ctx)
	defer cleanup()

	logger := logging.NewNoop()
	cfg := rdns.Config{
		Timeout:     2 * time.Second,
		CacheTTL:    time.Hour,
		NegativeTTL: 10 * time.Minute,
	}

	var dnsCalls atomic.Int32
	lookup := func(_ context.Context, ip string) ([]string, error) {
		dnsCalls.Add(1)
		if ip == "192.168.86.246" {
			return []string{"SRV-P-1C-NODE3.vz78.vozovoz.ru."}, nil
		}
		return nil, errors.New("NXDOMAIN")
	}

	r1 := rdns.New(ctx, redisClient, cfg, logger, rdns.WithLookupFunc(lookup))

	// Позитивный резолв: фон кладёт значение в Redis с EX≈cache_ttl.
	assert.Empty(t, r1.Lookup("192.168.86.246"), "первый вызов — промах")
	require.Eventually(t, func() bool {
		return r1.Lookup("192.168.86.246") == "SRV-P-1C-NODE3.vz78.vozovoz.ru"
	}, 5*time.Second, 20*time.Millisecond)

	val, err := redisClient.Get(ctx, "nexus:rdns:192.168.86.246").Result()
	require.NoError(t, err)
	assert.Equal(t, "SRV-P-1C-NODE3.vz78.vozovoz.ru", val)
	ttl, err := redisClient.TTL(ctx, "nexus:rdns:192.168.86.246").Result()
	require.NoError(t, err)
	assert.InDelta(t, cfg.CacheTTL.Seconds(), ttl.Seconds(), 60, "TTL позитивного кеша ≈ cache_ttl")

	// Негативный резолв: пустой маркер с EX≈negative_ttl.
	assert.Empty(t, r1.Lookup("10.5.5.5"))
	require.Eventually(t, func() bool {
		_, gerr := redisClient.Get(ctx, "nexus:rdns:10.5.5.5").Result()
		return gerr == nil
	}, 5*time.Second, 20*time.Millisecond)
	val, err = redisClient.Get(ctx, "nexus:rdns:10.5.5.5").Result()
	require.NoError(t, err)
	assert.Empty(t, val, "негативный маркер — пустая строка")
	ttl, err = redisClient.TTL(ctx, "nexus:rdns:10.5.5.5").Result()
	require.NoError(t, err)
	assert.InDelta(t, cfg.NegativeTTL.Seconds(), ttl.Seconds(), 60, "TTL негативного кеша ≈ negative_ttl")

	callsAfterWarmup := dnsCalls.Load()

	// «Холодный» процесс: новый Resolver (пустой L1) читает Redis, DNS не дёргает.
	r2 := rdns.New(ctx, redisClient, cfg, logger, rdns.WithLookupFunc(lookup))
	assert.Empty(t, r2.Lookup("192.168.86.246"), "L1 холодный — первый вызов промах")
	require.Eventually(t, func() bool {
		return r2.Lookup("192.168.86.246") == "SRV-P-1C-NODE3.vz78.vozovoz.ru"
	}, 5*time.Second, 20*time.Millisecond)
	assert.Equal(t, callsAfterWarmup, dnsCalls.Load(),
		"холодная реплика взяла имя из Redis без нового PTR-lookup")
}
