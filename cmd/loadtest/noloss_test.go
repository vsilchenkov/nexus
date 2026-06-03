package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

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

	t.Run("loss detected -> fails", func(t *testing.T) {
		t.Parallel()
		r := base()
		r.NoLossChecked = true
		r.NoLoss = false
		assert.False(t, r.passed(100))
	})

	t.Run("no loss -> passes", func(t *testing.T) {
		t.Parallel()
		r := base()
		r.NoLossChecked = true
		r.NoLoss = true
		assert.True(t, r.passed(100))
	})
}
