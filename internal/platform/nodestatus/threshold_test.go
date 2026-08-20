package nodestatus_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/nodestatus"
)

// TestDownRequiresConsecutiveFailures (§52-доп): одиночный тяжёлый отказ НЕ
// делает узел down — до порога он degraded.
//
// Это и есть исходная жалоба: узел с 42 ошибками из 489 показывал Down, потому
// что статус ставил последний ЗАВЕРШИВШИЙСЯ вызов, а им оказалась медленная
// пятисотка среди быстрых успешных ответов.
func TestDownRequiresConsecutiveFailures(t *testing.T) {
	t.Parallel()

	w, srv := newWriterWithThreshold(t, 3)
	ctx := context.Background()
	const node = "partner/orders"

	for i := 1; i < 3; i++ {
		got := w.SetLastOutcome(ctx, node, domain.NodeOutcomeDown)
		assert.Equal(t, domain.NodeOutcomeDegraded, got, "отказ %d из 3 — ещё не down", i)
	}

	got := w.SetLastOutcome(ctx, node, domain.NodeOutcomeDown)
	assert.Equal(t, domain.NodeOutcomeDown, got, "порог набран — узел down")

	stored, err := srv.Get(nodestatus.Key(node))
	require.NoError(t, err)
	assert.Equal(t, "1", stored, "в Redis лежит эффективный исход, а не сырой")
}

// TestSuccessResetsStreak (§52-доп): успешный ответ обнуляет счётчик, и отсчёт
// начинается заново. Иначе редкие ошибки за сутки накопились бы в ложный down.
func TestSuccessResetsStreak(t *testing.T) {
	t.Parallel()

	w, srv := newWriterWithThreshold(t, 3)
	ctx := context.Background()
	const node = "partner/orders"

	w.SetLastOutcome(ctx, node, domain.NodeOutcomeDown)
	w.SetLastOutcome(ctx, node, domain.NodeOutcomeDown)
	require.Equal(t, domain.NodeOutcomeOK, w.SetLastOutcome(ctx, node, domain.NodeOutcomeOK))

	assert.NotContains(t, srv.Keys(), nodestatus.StreakKey(node), "успех удаляет счётчик")

	// После успеха снова нужен полный порог.
	assert.Equal(t, domain.NodeOutcomeDegraded, w.SetLastOutcome(ctx, node, domain.NodeOutcomeDown))
	assert.Equal(t, domain.NodeOutcomeDegraded, w.SetLastOutcome(ctx, node, domain.NodeOutcomeDown))
	assert.Equal(t, domain.NodeOutcomeDown, w.SetLastOutcome(ctx, node, domain.NodeOutcomeDown))
}

// TestClientErrorDoesNotCountAsFailure (§52-доп): 4xx — ответ приёмника, а не
// его отказ. Поток клиентских ошибок не должен «ронять» живой узел.
func TestClientErrorDoesNotCountAsFailure(t *testing.T) {
	t.Parallel()

	w, srv := newWriterWithThreshold(t, 2)
	ctx := context.Background()
	const node = "partner/orders"

	for range 5 {
		assert.Equal(t, domain.NodeOutcomeDegraded,
			w.SetLastOutcome(ctx, node, domain.NodeOutcomeDegraded))
	}
	assert.NotContains(t, srv.Keys(), nodestatus.StreakKey(node), "4xx счётчик не создаёт")

	// И не мешает набрать порог настоящим отказам.
	assert.Equal(t, domain.NodeOutcomeDegraded, w.SetLastOutcome(ctx, node, domain.NodeOutcomeDown))
	assert.Equal(t, domain.NodeOutcomeDown, w.SetLastOutcome(ctx, node, domain.NodeOutcomeDown))
}

// TestStreakSharedBetweenWriters (§52-доп + §93): две реплики Sender ведут ОДИН
// счётчик. Отказы, разложенные балансировщиком по репликам, обязаны доводить
// узел до порога — иначе при двух репликах он не покраснел бы никогда.
func TestStreakSharedBetweenWriters(t *testing.T) {
	t.Parallel()

	first, srv := newWriterWithThreshold(t, 4)
	second := nodestatus.NewRedisWriter(redisClient(t, srv), 4, noopLogger())
	ctx := context.Background()
	const node = "partner/orders"

	assert.Equal(t, domain.NodeOutcomeDegraded, first.SetLastOutcome(ctx, node, domain.NodeOutcomeDown))
	assert.Equal(t, domain.NodeOutcomeDegraded, second.SetLastOutcome(ctx, node, domain.NodeOutcomeDown))
	assert.Equal(t, domain.NodeOutcomeDegraded, first.SetLastOutcome(ctx, node, domain.NodeOutcomeDown))
	assert.Equal(t, domain.NodeOutcomeDown, second.SetLastOutcome(ctx, node, domain.NodeOutcomeDown),
		"счётчик общий: четвёртый отказ подряд даёт down независимо от реплики")
}

// TestThresholdOneKeepsLegacyBehaviour (§52-доп): порог 1 = поведение до правки,
// первая же тяжёлая ошибка красит узел.
func TestThresholdOneKeepsLegacyBehaviour(t *testing.T) {
	t.Parallel()

	w, _ := newWriterWithThreshold(t, 1)
	assert.Equal(t, domain.NodeOutcomeDown,
		w.SetLastOutcome(context.Background(), "partner/orders", domain.NodeOutcomeDown))
}

// TestInvalidThresholdFallsBackToDefault (§52-доп): порог 0 означал бы «down
// всегда», поэтому заменяется дефолтом.
func TestInvalidThresholdFallsBackToDefault(t *testing.T) {
	t.Parallel()

	w, _ := newWriterWithThreshold(t, 0)
	ctx := context.Background()
	const node = "partner/orders"

	for i := 1; i < nodestatus.DefaultDownThreshold; i++ {
		require.Equal(t, domain.NodeOutcomeDegraded, w.SetLastOutcome(ctx, node, domain.NodeOutcomeDown),
			"отказ %d из %d", i, nodestatus.DefaultDownThreshold)
	}
	assert.Equal(t, domain.NodeOutcomeDown, w.SetLastOutcome(ctx, node, domain.NodeOutcomeDown))
}

// TestRedisDownReturnsRawOutcome (§52-доп): без Redis решить о пороге нечем —
// возвращаем исход как есть, доставку это не трогает.
func TestRedisDownReturnsRawOutcome(t *testing.T) {
	t.Parallel()

	w, srv := newWriterWithThreshold(t, 10)
	srv.Close()

	assert.Equal(t, domain.NodeOutcomeDown,
		w.SetLastOutcome(context.Background(), "partner/orders", domain.NodeOutcomeDown))
}
