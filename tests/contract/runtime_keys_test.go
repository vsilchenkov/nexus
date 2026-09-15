package contract

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/circuitbreaker"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/nodestatus"
	"nexus/internal/receiver/adapter/out/rabbitmq"
	webredis "nexus/internal/web/adapter/out/redis"
)

// Рантайм-состояние узла пишет один процесс, а читает другой (§101.6): исход
// последнего вызова и circuit breaker — Sender → Web, здоровье pull-узла —
// Receiver → Web. Писатели и читатели сверяются здесь, на одном Redis: разойдись
// формат ключа — Web молча показывал бы «нет данных» над живым состоянием, а
// тесты каждого сервиса по отдельности этого не видят.
//
// Узлы — боевой случай §101.1: путь telegram в командах messengers и webhook.

const (
	runtimeIDMessengers = "f6693f87-0000-4000-8000-000000000001"
	runtimeIDWebhook    = "d3aae718-0000-4000-8000-000000000002"
)

func newRuntimeRedis(t *testing.T) *goredis.Client {
	t.Helper()
	srv := miniredis.RunT(t)
	c := goredis.NewClient(&goredis.Options{Addr: srv.Addr()})
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestRuntimeKeys_NodeOutcomeSplitsSameNamePaths — «Down» узла одной команды не
// красит одноимённый узел другой.
func TestRuntimeKeys_NodeOutcomeSplitsSameNamePaths(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := newRuntimeRedis(t)
	log := logging.NewNoop()

	w := nodestatus.NewRedisWriter(c, 1, log)
	w.SetLastOutcome(ctx, runtimeIDMessengers, domain.NodeOutcomeDown)
	w.SetLastOutcome(ctx, runtimeIDWebhook, domain.NodeOutcomeOK)

	got, err := webredis.NewNodeStatusReaderRedis(c, log).GetLastOutcomes(ctx,
		[]string{runtimeIDMessengers, runtimeIDWebhook})
	require.NoError(t, err)
	assert.Equal(t, map[string]domain.NodeOutcome{
		runtimeIDMessengers: domain.NodeOutcomeDown,
		runtimeIDWebhook:    domain.NodeOutcomeOK,
	}, got)
}

// TestRuntimeKeys_BreakerSplitsSameNamePaths — отказы одного узла не закрывают
// доставку одноимённому, а ручной сброс защиты одного не открывает другой.
func TestRuntimeKeys_BreakerSplitsSameNamePaths(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := newRuntimeRedis(t)

	cb := circuitbreaker.New(c, 2, time.Minute)
	for range 2 {
		require.NoError(t, cb.RecordFailure(ctx,
			circuitbreaker.NodeKey(runtimeIDMessengers), domain.BreakerPolicy{}))
	}

	web := webredis.NewNodeBreakerRedis(c, logging.NewNoop())
	open, err := web.State(ctx, runtimeIDMessengers)
	require.NoError(t, err)
	assert.Equal(t, domain.BreakerStateOpen, open.State, "Web видит breaker, открытый Sender'ом")
	other, err := web.State(ctx, runtimeIDWebhook)
	require.NoError(t, err)
	assert.False(t, other.Exists, "breaker одноимённого узла другой команды не тронут")

	_, err = web.Reset(ctx, runtimeIDWebhook)
	require.NoError(t, err)
	allowed, err := cb.Allow(ctx, circuitbreaker.NodeKey(runtimeIDMessengers))
	require.NoError(t, err)
	assert.False(t, allowed, "сброс защиты чужого узла не открывает доставку этому")
}

// TestRuntimeKeys_RMQHealthSplitsSameNamePaths — снимок здоровья одного
// pull-узла не перезаписывает снимок одноимённого.
func TestRuntimeKeys_RMQHealthSplitsSameNamePaths(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := newRuntimeRedis(t)

	sink := rabbitmq.NewHealthSink(c)
	require.NoError(t, sink.Publish(ctx, domain.RMQHealth{
		NodeID: runtimeIDMessengers, NodePath: "telegram", Degraded: true, QueueDepth: 5,
	}))
	require.NoError(t, sink.Publish(ctx, domain.RMQHealth{
		NodeID: runtimeIDWebhook, NodePath: "telegram", QueueDepth: 7,
	}))
	require.Error(t, sink.Publish(ctx, domain.RMQHealth{NodePath: "telegram"}),
		"снимок без id узла некуда положить")

	r := webredis.NewRMQHealthReaderRedis(c)
	a, err := r.Get(ctx, runtimeIDMessengers)
	require.NoError(t, err)
	require.NotNil(t, a)
	assert.True(t, a.Degraded)
	assert.EqualValues(t, 5, a.QueueDepth)

	b, err := r.Get(ctx, runtimeIDWebhook)
	require.NoError(t, err)
	require.NotNil(t, b)
	assert.False(t, b.Degraded)
	assert.EqualValues(t, 7, b.QueueDepth)
}
