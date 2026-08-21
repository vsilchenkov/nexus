package usecase

import (
	"context"
	"errors"
	"sync"
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
//
// Под мьютексом: фоновый цикл Run зовёт репозиторий из своей горутины, а тест
// читает счётчики из своей — без синхронизации это гонка (найдена -race).
type fakeRejectedRepo struct {
	port.RejectedRepo

	mu        sync.Mutex
	cutoffs   []time.Time
	purges    int
	trims     []int
	deleted   int
	trimmed   int
	deleteErr error
	purgeErr  error
}

func (r *fakeRejectedRepo) TrimRejectedGroups(_ context.Context, keep int) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.trims = append(r.trims, keep)
	return r.trimmed, nil
}

func (r *fakeRejectedRepo) DeleteRejectedOlderThan(_ context.Context, cutoff time.Time) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cutoffs = append(r.cutoffs, cutoff)
	if r.deleteErr != nil {
		return 0, r.deleteErr
	}
	return r.deleted, nil
}

func (r *fakeRejectedRepo) PurgeRejected(_ context.Context) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.purges++
	if r.purgeErr != nil {
		return 0, r.purgeErr
	}
	return r.deleted, nil
}

// cutoffCount/purgeCount — чтение счётчиков из горутины теста.
func (r *fakeRejectedRepo) cutoffCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.cutoffs)
}

func (r *fakeRejectedRepo) purgeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.purges
}

func (r *fakeRejectedRepo) trimKeeps() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.trims...)
}

func (r *fakeRejectedRepo) cutoffAt(i int) time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cutoffs[i]
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

	require.Equal(t, 1, repo.cutoffCount())
	assert.Equal(t, now.AddDate(0, 0, -7), repo.cutoffAt(0))
	assert.Zero(t, repo.purgeCount(), "при заданном сроке журнал не вычищается целиком")
	// Предохранитель от сканера: одного срока хранения мало — каждая попытка по
	// случайному пути создаёт НОВУЮ группу.
	assert.Equal(t, []int{domain.RejectedMaxStoredGroups}, repo.trimKeeps())
}

// TestRejectedHousekeepingCycle_SkipsTrimWhenDisabled: при выключенном журнале
// вычищается всё, и подрезать уже нечего.
func TestRejectedHousekeepingCycle_SkipsTrimWhenDisabled(t *testing.T) {
	t.Parallel()

	repo := &fakeRejectedRepo{}
	ret := NewRejectRetentionProvider()
	zero := 0
	ret.Set(&zero)

	NewRejectedHousekeeping(repo, ret, logging.NewNoop()).Cycle(context.Background())

	assert.Equal(t, 1, repo.purgeCount())
	assert.Empty(t, repo.trimKeeps())
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

	assert.Equal(t, 1, repo.purgeCount())
	assert.Zero(t, repo.cutoffCount(), "по сроку не удаляем — удалять нечего, журнал выключен")
}

// TestRejectedHousekeepingCycle_ErrorsAreLoggedNotPanicked: сбой БД не роняет
// фоновый цикл — следующая итерация повторит.
func TestRejectedHousekeepingCycle_ErrorsAreLoggedNotPanicked(t *testing.T) {
	t.Parallel()

	repo := &fakeRejectedRepo{deleteErr: errors.New("pg is down")}
	ret := NewRejectRetentionProvider()
	hk := NewRejectedHousekeeping(repo, ret, logging.NewNoop())

	assert.NotPanics(t, func() { hk.Cycle(context.Background()) })
	assert.Equal(t, 1, repo.cutoffCount())
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

	require.Eventually(t, func() bool { return repo.cutoffCount() > 0 }, time.Second, 10*time.Millisecond,
		"первый проход выполняется сразу, а не через период")

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("housekeeping did not stop on context cancel")
	}
}

// fakeRejectLock — заглушка лока цикла: считает попытки и отвечает заданным
// вердиктом. Под мьютексом — Run зовёт TryLock из своей горутины (§93.7).
type fakeRejectLock struct {
	mu    sync.Mutex
	calls int
	grant bool
	err   error
}

func (l *fakeRejectLock) TryLock(_ context.Context, _ time.Duration) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	return l.grant, l.err
}

func (l *fakeRejectLock) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls
}

// TestRejectedHousekeepingRun_SkipsCycleWhenLockTaken: плановый проход
// достаётся ОДНОЙ реплике (§93.7) — не получив лок, вторая ничего не удаляет.
func TestRejectedHousekeepingRun_SkipsCycleWhenLockTaken(t *testing.T) {
	t.Parallel()

	repo := &fakeRejectedRepo{}
	lock := &fakeRejectLock{grant: false}
	hk := NewRejectedHousekeeping(repo, NewRejectRetentionProvider(), logging.NewNoop(),
		WithRejectedHousekeepingInterval(time.Hour), WithRejectedHousekeepingLock(lock))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); hk.Run(ctx) }()

	require.Eventually(t, func() bool { return lock.count() > 0 }, time.Second, 10*time.Millisecond,
		"плановый проход обязан спросить лок")
	assert.Zero(t, repo.cutoffCount(), "без лока реплика не должна чистить")

	cancel()
	<-done
}

// TestRejectedHousekeepingCycleNow_IgnoresLock: внеочередной прогон по смене
// настройки лока НЕ берёт. Иначе TTL часового цикла отложил бы применение
// нового срока хранения до следующего часа — обещание §94.5 «уменьшение
// применяется сразу» сломалось бы ровно на второй реплике.
func TestRejectedHousekeepingCycleNow_IgnoresLock(t *testing.T) {
	t.Parallel()

	repo := &fakeRejectedRepo{}
	lock := &fakeRejectLock{grant: false} // лок занят соседом
	hk := NewRejectedHousekeeping(repo, NewRejectRetentionProvider(), logging.NewNoop(),
		WithRejectedHousekeepingInterval(time.Hour), WithRejectedHousekeepingLock(lock))

	hk.CycleNow(context.Background())

	assert.Equal(t, 1, repo.cutoffCount(), "внеочередная чистка обязана выполниться")
	assert.Zero(t, lock.count(), "и не должна спрашивать лок вовсе")
}

// TestRejectedHousekeepingRun_LockErrorSkipsCycle: ошибка Redis трактуется как
// запрет (тот же выбор, что у чистки аудита): с больным Redis дешевле
// пропустить час, чем идти вторым DELETE по растущей таблице.
func TestRejectedHousekeepingRun_LockErrorSkipsCycle(t *testing.T) {
	t.Parallel()

	repo := &fakeRejectedRepo{}
	lock := &fakeRejectLock{grant: true, err: errors.New("redis down")}
	hk := NewRejectedHousekeeping(repo, NewRejectRetentionProvider(), logging.NewNoop(),
		WithRejectedHousekeepingInterval(time.Hour), WithRejectedHousekeepingLock(lock))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); hk.Run(ctx) }()

	require.Eventually(t, func() bool { return lock.count() > 0 }, time.Second, 10*time.Millisecond)
	assert.Zero(t, repo.cutoffCount(), "ошибка лока = пропуск цикла, а не проход")

	cancel()
	<-done
}
