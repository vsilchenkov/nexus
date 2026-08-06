package circuitbreaker_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/circuitbreaker"
)

// Unit-тесты операторских операций (§81.4) поверх miniredis. Проверяют контракт
// «оператор видит правду и может снять блокировку»: снимок без побочных
// эффектов, сброс идемпотентен и возвращает состояние ДО сброса.
func newAdmin(t *testing.T, threshold int, cooldown time.Duration) (*circuitbreaker.Breaker, *circuitbreaker.Admin, *miniredis.Miniredis) {
	t.Helper()

	srv := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: srv.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	return circuitbreaker.New(client, threshold, cooldown), circuitbreaker.NewAdmin(client), srv
}

// Ключ собирают пять разных методов Breaker'а плюс Admin в другом процессе.
// Тест держит их на одном формате: разойдись они — Web показывал бы «закрыт»
// над открытым breaker'ом, и никакой другой тест этого не заметил бы.
func TestKey_MatchesWhatBreakerWrites(t *testing.T) {
	t.Parallel()

	b, _, srv := newAdmin(t, 2, time.Minute)
	require.NoError(t, b.RecordFailure(context.Background(), "node/a", domain.BreakerPolicy{}))

	assert.Equal(t, "circuit:node/a", circuitbreaker.Key("node/a"))
	assert.True(t, srv.Exists(circuitbreaker.Key("node/a")), "Breaker пишет ровно в Key()")
}

func TestSnapshot_UnknownKey(t *testing.T) {
	t.Parallel()

	_, adm, _ := newAdmin(t, 3, time.Minute)

	snap, err := adm.Snapshot(context.Background(), "node/never-seen")
	require.NoError(t, err)
	assert.False(t, snap.Exists, "ключа нет — breaker никогда не открывался")
	assert.Equal(t, circuitbreaker.StateClosed, snap.State)
	assert.Zero(t, snap.Failures)
	assert.Zero(t, snap.RetryAfter)
}

func TestSnapshot_OpenCarriesPolicyAndCountdown(t *testing.T) {
	t.Parallel()

	const (
		threshold = 3
		cooldown  = 30 * time.Second
	)
	b, adm, _ := newAdmin(t, threshold, cooldown)
	ctx := context.Background()
	for range threshold {
		require.NoError(t, b.RecordFailure(ctx, "node/a", domain.BreakerPolicy{}))
	}

	snap, err := adm.Snapshot(ctx, "node/a")
	require.NoError(t, err)
	assert.True(t, snap.Exists)
	assert.Equal(t, circuitbreaker.StateOpen, snap.State)
	assert.Equal(t, threshold, snap.Failures)
	// Политика приезжает из hash'а: у читающей стороны своей копии нет.
	assert.Equal(t, threshold, snap.Threshold)
	assert.Equal(t, cooldown, snap.Cooldown)
	assert.False(t, snap.OpenedAt.IsZero())
	assert.Greater(t, snap.RetryAfter, time.Duration(0))
	assert.LessOrEqual(t, snap.RetryAfter, cooldown)
}

// Ключ, созданный версией до §81.3, полей политики не содержит. Снимок обязан
// оставаться читаемым: UI покажет счётчик без знаменателя, а не упадёт.
func TestSnapshot_LegacyKeyWithoutPolicy(t *testing.T) {
	t.Parallel()

	_, adm, srv := newAdmin(t, 5, time.Minute)
	k := circuitbreaker.Key("node/legacy")
	srv.HSet(k, "state", "open", "failures", "5", "opened_at", "1")

	snap, err := adm.Snapshot(context.Background(), "node/legacy")
	require.NoError(t, err)
	assert.True(t, snap.Exists)
	assert.Equal(t, circuitbreaker.StateOpen, snap.State)
	assert.Equal(t, 5, snap.Failures)
	assert.Zero(t, snap.Threshold, "политика неизвестна")
	assert.Zero(t, snap.Cooldown)
	assert.Zero(t, snap.RetryAfter, "без cooldown обратный отсчёт не выдумываем")
}

func TestReset_ReturnsPreviousStateAndClears(t *testing.T) {
	t.Parallel()

	b, adm, srv := newAdmin(t, 2, time.Minute)
	ctx := context.Background()
	for range 2 {
		require.NoError(t, b.RecordFailure(ctx, "node/a", domain.BreakerPolicy{}))
	}

	before, err := adm.Reset(ctx, "node/a")
	require.NoError(t, err)
	assert.True(t, before.Exists, "снимок — состояние ДО сброса")
	assert.Equal(t, circuitbreaker.StateOpen, before.State)
	assert.Equal(t, 2, before.Failures)

	assert.False(t, srv.Exists(circuitbreaker.Key("node/a")), "состояние удалено")
}

func TestReset_IsIdempotent(t *testing.T) {
	t.Parallel()

	_, adm, _ := newAdmin(t, 2, time.Minute)
	ctx := context.Background()

	snap, err := adm.Reset(ctx, "node/never-seen")
	require.NoError(t, err, "сброс закрытого breaker'а — не ошибка")
	assert.False(t, snap.Exists, "оператор видит, что сбрасывать было нечего")
}

// Смысл кнопки: доставка возобновляется немедленно, а не после cooldown.
func TestReset_AllowsTrafficImmediately(t *testing.T) {
	t.Parallel()

	b, adm, _ := newAdmin(t, 2, time.Hour) // cooldown заведомо не истечёт сам
	ctx := context.Background()
	for range 2 {
		require.NoError(t, b.RecordFailure(ctx, "node/a", domain.BreakerPolicy{}))
	}
	ok, err := b.Allow(ctx, "node/a")
	require.NoError(t, err)
	require.False(t, ok, "предусловие: breaker открыт")

	_, err = adm.Reset(ctx, "node/a")
	require.NoError(t, err)

	ok, err = b.Allow(ctx, "node/a")
	require.NoError(t, err)
	assert.True(t, ok, "после сброса запросы идут сразу")
}

// Проба, упавшая уже после сброса, не должна воскрешать open: счёт начинается
// с нуля, узел получает полный бюджет попыток.
func TestReset_FailureAfterResetStartsFromScratch(t *testing.T) {
	t.Parallel()

	b, adm, _ := newAdmin(t, 3, time.Minute)
	ctx := context.Background()
	for range 3 {
		require.NoError(t, b.RecordFailure(ctx, "node/a", domain.BreakerPolicy{}))
	}
	_, err := adm.Reset(ctx, "node/a")
	require.NoError(t, err)

	require.NoError(t, b.RecordFailure(ctx, "node/a", domain.BreakerPolicy{}))

	snap, err := adm.Snapshot(ctx, "node/a")
	require.NoError(t, err)
	assert.Equal(t, 1, snap.Failures)
	assert.NotEqual(t, circuitbreaker.StateOpen, snap.State)
}

// Снимок не должен расходовать half-open-пробу: иначе открытая вкладка UI
// съедала бы пробы, которые предназначены боевому трафику.
func TestSnapshot_DoesNotConsumeProbe(t *testing.T) {
	t.Parallel()

	b, adm, srv := newAdmin(t, 2, time.Minute)
	ctx := context.Background()
	for range 2 {
		require.NoError(t, b.RecordFailure(ctx, "node/a", domain.BreakerPolicy{}))
	}
	// cooldown истёк — следующий Allow обязан выдать ровно одну пробу.
	srv.HSet(circuitbreaker.Key("node/a"), "opened_at", "0")

	for range 3 {
		_, err := adm.Snapshot(ctx, "node/a")
		require.NoError(t, err)
	}

	ok, err := b.Allow(ctx, "node/a")
	require.NoError(t, err)
	assert.True(t, ok, "проба не израсходована снимками")
}

// §81.3: политика узла перекрывает глобальную. Узел с порогом 2 обязан
// открыться на второй ошибке, хотя в конфигурации стоит 10.
func TestRecordFailure_NodePolicyOverridesGlobal(t *testing.T) {
	t.Parallel()

	b, adm, _ := newAdmin(t, 10, time.Hour) // глобальная политика — «терпеливая»
	ctx := context.Background()
	nodePolicy := domain.BreakerPolicy{Threshold: 2, Cooldown: 5 * time.Second}

	require.NoError(t, b.RecordFailure(ctx, "node/strict", nodePolicy))
	snap, err := adm.Snapshot(ctx, "node/strict")
	require.NoError(t, err)
	require.NotEqual(t, circuitbreaker.StateOpen, snap.State, "одной ошибки мало")

	require.NoError(t, b.RecordFailure(ctx, "node/strict", nodePolicy))
	snap, err = adm.Snapshot(ctx, "node/strict")
	require.NoError(t, err)
	assert.Equal(t, circuitbreaker.StateOpen, snap.State, "порог узла — 2, а не глобальные 10")
	assert.Equal(t, 2, snap.Threshold, "в снимок уходит применённая политика")
	assert.Equal(t, 5*time.Second, snap.Cooldown)
}

// Нулевые поля политики означают «как в конфигурации» — узел без собственных
// настроек ведёт себя ровно как до §81.3.
func TestRecordFailure_ZeroPolicyFallsBackToGlobal(t *testing.T) {
	t.Parallel()

	b, adm, _ := newAdmin(t, 3, 42*time.Second)
	ctx := context.Background()
	for range 3 {
		require.NoError(t, b.RecordFailure(ctx, "node/default", domain.BreakerPolicy{}))
	}

	snap, err := adm.Snapshot(ctx, "node/default")
	require.NoError(t, err)
	assert.Equal(t, circuitbreaker.StateOpen, snap.State)
	assert.Equal(t, 3, snap.Threshold)
	assert.Equal(t, 42*time.Second, snap.Cooldown)
}

// Cooldown узла обязан управлять и выдачей половинчато-открытой пробы: иначе
// настройка была бы косметической — состояние показывает одно, Allow ждёт другое.
func TestAllow_UsesNodeCooldownFromState(t *testing.T) {
	t.Parallel()

	// Глобальный cooldown — час; у узла 50 мс.
	b, _, srv := newAdmin(t, 1, time.Hour)
	ctx := context.Background()
	require.NoError(t, b.RecordFailure(ctx, "node/fast", domain.BreakerPolicy{Threshold: 1, Cooldown: 50 * time.Millisecond}))

	ok, err := b.Allow(ctx, "node/fast")
	require.NoError(t, err)
	require.False(t, ok, "предусловие: breaker открыт")

	// Сдвигаем момент открытия на 100 мс назад — cooldown узла истёк, глобальный нет.
	srv.HSet(circuitbreaker.Key("node/fast"), "opened_at",
		strconv.FormatInt(time.Now().Add(-100*time.Millisecond).UnixNano(), 10))

	ok, err = b.Allow(ctx, "node/fast")
	require.NoError(t, err)
	assert.True(t, ok, "проба выдаётся по cooldown узла, а не по глобальному")
}

// IsOpen (им пользуется репроцессор DLQ) обязан считать по тому же cooldown.
func TestIsOpen_UsesNodeCooldownFromState(t *testing.T) {
	t.Parallel()

	b, _, srv := newAdmin(t, 1, time.Hour)
	ctx := context.Background()
	require.NoError(t, b.RecordFailure(ctx, "node/fast", domain.BreakerPolicy{Threshold: 1, Cooldown: 50 * time.Millisecond}))

	open, err := b.IsOpen(ctx, "node/fast")
	require.NoError(t, err)
	require.True(t, open)

	srv.HSet(circuitbreaker.Key("node/fast"), "opened_at",
		strconv.FormatInt(time.Now().Add(-100*time.Millisecond).UnixNano(), 10))

	open, err = b.IsOpen(ctx, "node/fast")
	require.NoError(t, err)
	assert.False(t, open, "cooldown узла истёк — репроцессор снова пробует")
}

// Ревизия §81.9: до достижения порога состояние тоже обязано быть видно.
// Прежде RecordFailure писал `state` только при открытии, и ключ с 4 отказами
// из 5 выглядел для читающей стороны как несуществующий — оператор не видел
// «сколько осталось», хотя политика в hash легла именно ради этого (§81.3.1).
func TestSnapshot_PartialFailures_AreVisible(t *testing.T) {
	t.Parallel()

	b, adm, _ := newAdmin(t, 5, time.Minute)
	ctx := context.Background()
	for range 3 {
		require.NoError(t, b.RecordFailure(ctx, "node/partial", domain.BreakerPolicy{}))
	}

	snap, err := adm.Snapshot(ctx, "node/partial")
	require.NoError(t, err)
	assert.True(t, snap.Exists, "счёт идёт — состояние существует")
	assert.Equal(t, circuitbreaker.StateClosed, snap.State, "но защита ещё не сработала")
	assert.Equal(t, 3, snap.Failures)
	assert.Equal(t, 5, snap.Threshold, "знаменатель для «3 из 5»")
	assert.Zero(t, snap.RetryAfter, "закрытому breaker'у обратный отсчёт не нужен")
}
