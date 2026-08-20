package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/clock"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// fakeRejectedRepo — заглушка порта журнала отказов: запоминает вызовы чистки.
type fakeRejectedRepo struct {
	port.RejectedRepo

	cutoffs   []time.Time
	purges    int
	deleted   int
	deleteErr error
	purgeErr  error
}

func (r *fakeRejectedRepo) DeleteRejectedOlderThan(_ context.Context, cutoff time.Time) (int, error) {
	r.cutoffs = append(r.cutoffs, cutoff)
	if r.deleteErr != nil {
		return 0, r.deleteErr
	}
	return r.deleted, nil
}

func (r *fakeRejectedRepo) PurgeRejected(_ context.Context) (int, error) {
	r.purges++
	if r.purgeErr != nil {
		return 0, r.purgeErr
	}
	return r.deleted, nil
}

// TestRejectRetentionProvider: ноль — это «сбор выключен», а не «не задано».
func TestRejectRetentionProvider(t *testing.T) {
	t.Parallel()

	p := NewRejectRetentionProvider()
	assert.Equal(t, domain.RejectedRetentionDefaultDays, p.Days(), "до чтения настроек — дефолт домена")
	assert.True(t, p.Enabled())

	p.Set(nil)
	assert.Equal(t, domain.RejectedRetentionDefaultDays, p.Days(), "nil = настройка не задана")
	assert.True(t, p.Enabled())

	seven := 7
	p.Set(&seven)
	assert.Equal(t, 7, p.Days())

	zero := 0
	p.Set(&zero)
	assert.Equal(t, 0, p.Days())
	assert.False(t, p.Enabled(), "0 выключает журнал")
}

// TestRejectedHousekeepingCycle_DeletesByRetention: граница считается от
// текущего времени минус срок хранения.
func TestRejectedHousekeepingCycle_DeletesByRetention(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	repo := &fakeRejectedRepo{deleted: 3}
	ret := NewRejectRetentionProvider()
	days := 7
	ret.Set(&days)

	hk := NewRejectedHousekeeping(repo, ret, logging.NewNoop(),
		WithRejectedHousekeepingClock(clock.Func(func() time.Time { return now })))
	hk.Cycle(context.Background())

	require.Len(t, repo.cutoffs, 1)
	assert.Equal(t, now.AddDate(0, 0, -7), repo.cutoffs[0])
	assert.Zero(t, repo.purges, "при заданном сроке журнал не вычищается целиком")
}

// TestRejectedHousekeepingCycle_PurgesWhenDisabled: срок 0 удаляет накопленное
// целиком (§94.5) — иначе выключение журнала оставило бы данные лежать без
// срока годности.
func TestRejectedHousekeepingCycle_PurgesWhenDisabled(t *testing.T) {
	t.Parallel()

	repo := &fakeRejectedRepo{deleted: 12}
	ret := NewRejectRetentionProvider()
	zero := 0
	ret.Set(&zero)

	hk := NewRejectedHousekeeping(repo, ret, logging.NewNoop())
	hk.Cycle(context.Background())

	assert.Equal(t, 1, repo.purges)
	assert.Empty(t, repo.cutoffs, "по сроку не удаляем — удалять нечего, журнал выключен")
}

// TestRejectedHousekeepingCycle_ErrorsAreLoggedNotPanicked: сбой БД не роняет
// фоновый цикл — следующая итерация повторит.
func TestRejectedHousekeepingCycle_ErrorsAreLoggedNotPanicked(t *testing.T) {
	t.Parallel()

	repo := &fakeRejectedRepo{deleteErr: errors.New("pg is down")}
	ret := NewRejectRetentionProvider()
	hk := NewRejectedHousekeeping(repo, ret, logging.NewNoop())

	assert.NotPanics(t, func() { hk.Cycle(context.Background()) })
	assert.Len(t, repo.cutoffs, 1)
}

// TestRejectedHousekeepingRun_StopsOnContext: цикл делает первый проход сразу и
// завершается по отмене контекста.
func TestRejectedHousekeepingRun_StopsOnContext(t *testing.T) {
	t.Parallel()

	repo := &fakeRejectedRepo{}
	hk := NewRejectedHousekeeping(repo, NewRejectRetentionProvider(), logging.NewNoop(),
		WithRejectedHousekeepingInterval(time.Hour))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); hk.Run(ctx) }()

	require.Eventually(t, func() bool { return len(repo.cutoffs) > 0 }, time.Second, 10*time.Millisecond,
		"первый проход выполняется сразу, а не через период")

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("housekeeping did not stop on context cancel")
	}
}
