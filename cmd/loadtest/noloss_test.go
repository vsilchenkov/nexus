package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seqCounter возвращает countFn, отдающую значения из seq по порядку; после
// исчерпания повторяет последнее (имитирует «плато»).
func seqCounter(seq ...int64) countFn {
	i := 0
	return func(context.Context) (int64, error) {
		v := seq[i]
		if i < len(seq)-1 {
			i++
		}
		return v, nil
	}
}

func TestPollUntilStableReachesExpected(t *testing.T) {
	t.Parallel()
	// Растущий бэклог добирает до expected — потерь нет, ранний выход.
	rows, outcome, err := pollUntilStable(context.Background(), seqCounter(5, 10, 25),
		20, time.Millisecond, 5*time.Second)
	require.NoError(t, err)
	assert.EqualValues(t, 25, rows)
	assert.Equal(t, pollReachedExpected, outcome)
}

func TestPollUntilStablePlateauBelowExpectedIsLoss(t *testing.T) {
	t.Parallel()
	// Count замер на 8 и не растёт — после noLossStableRounds признаём плато:
	// 8 < 13 = реальная потеря (rows возвращается как есть, ok решает вызывающий).
	rows, outcome, err := pollUntilStable(context.Background(), seqCounter(8),
		13, time.Millisecond, 5*time.Second)
	require.NoError(t, err)
	assert.EqualValues(t, 8, rows)
	assert.Equal(t, pollPlateau, outcome)
}

func TestPollUntilStableHitsMaxWait(t *testing.T) {
	t.Parallel()
	// Count растёт бесконечно, expected недостижим — выходим по maxWait.
	// Растущий count = бэклог ещё дренируется → pollMaxWait (inconclusive), не плато.
	grow := int64(0)
	cnt := func(context.Context) (int64, error) { grow++; return grow, nil }
	rows, outcome, err := pollUntilStable(context.Background(), cnt,
		1<<30, time.Millisecond, 20*time.Millisecond)
	require.NoError(t, err)
	assert.Positive(t, rows)
	assert.Less(t, rows, int64(1<<30))
	assert.Equal(t, pollMaxWait, outcome)
}

func TestPollUntilStablePropagatesError(t *testing.T) {
	t.Parallel()
	want := errors.New("ch down")
	cnt := func(context.Context) (int64, error) { return 0, want }
	_, _, err := pollUntilStable(context.Background(), cnt, 10, time.Millisecond, time.Second)
	assert.ErrorIs(t, err, want)
}

func TestPollUntilStableCanceledBeforeFirstCount(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	cnt := func(context.Context) (int64, error) { called = true; return 0, nil }
	_, _, err := pollUntilStable(ctx, cnt, 10, 50*time.Millisecond, time.Second)
	assert.ErrorIs(t, err, context.Canceled)
	assert.False(t, called, "cnt не должен вызываться, если ctx отменён до первого опроса")
}

func TestCheckNoLossSkippedWithoutAddr(t *testing.T) {
	t.Parallel()
	res := checkNoLoss(context.Background(), flags{}, 5)
	assert.False(t, res.enabled)
	assert.NoError(t, res.err)
}

func TestCheckNoLossRejectsBadTable(t *testing.T) {
	t.Parallel()
	// addr задан, но имя таблицы невалидно — ошибка возвращается до коннекта.
	res := checkNoLoss(context.Background(), flags{CHAddr: "127.0.0.1:9000", CHTable: "bad; DROP"}, 0)
	assert.True(t, res.enabled)
	assert.Error(t, res.err)
}

func TestTableNameRe(t *testing.T) {
	t.Parallel()
	ok := []string{"nexus_default.loadtest", "loadtest", "db1.t_2"}
	bad := []string{"bad table", "a.b.c", "1bad", "t;DROP", ""}
	for _, s := range ok {
		assert.True(t, tableNameRe.MatchString(s), "want valid: %q", s)
	}
	for _, s := range bad {
		assert.False(t, tableNameRe.MatchString(s), "want invalid: %q", s)
	}
}

func TestReportPassedNoLoss(t *testing.T) {
	t.Parallel()
	base := func() *report {
		return &report{AchievedRPS: 100, P95Ms: 50, ErrorRate: 0}
	}

	t.Run("no-loss not checked -> passes on other criteria", func(t *testing.T) {
		t.Parallel()
		assert.True(t, base().passed(100))
	})

	t.Run("confirmed loss (plateau) -> fails", func(t *testing.T) {
		t.Parallel()
		r := base()
		r.NoLossChecked = true
		r.NoLoss = false
		assert.False(t, r.passed(100))
	})

	t.Run("inconclusive (still draining) -> passes with WARN", func(t *testing.T) {
		t.Parallel()
		// rows<expected, но count ещё рос на момент maxWait — не подтверждённая
		// потеря: прогон НЕ валим (WARN в stderr).
		r := base()
		r.NoLossChecked = true
		r.NoLoss = false
		r.NoLossInconclusive = true
		assert.True(t, r.passed(100))
	})

	t.Run("no loss -> passes", func(t *testing.T) {
		t.Parallel()
		r := base()
		r.NoLossChecked = true
		r.NoLoss = true
		assert.True(t, r.passed(100))
	})
}
