package usecase

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
)

// fakeCycleLock — управляемый лок цикла (§93.7).
type fakeCycleLock struct {
	granted bool
	err     error
	calls   atomic.Int64
	lastTTL atomic.Int64 // наносекунды последнего запрошенного TTL
}

func (f *fakeCycleLock) TryLock(_ context.Context, ttl time.Duration) (bool, error) {
	f.calls.Add(1)
	f.lastTTL.Store(int64(ttl))
	if f.err != nil {
		return false, f.err
	}
	return f.granted, nil
}

// countingLister считает обращения — по ним видно, дошло ли дело до уборки.
type countingLister struct {
	calls atomic.Int64
}

func (c *countingLister) ListForHousekeeping(context.Context) ([]*domain.Node, error) {
	c.calls.Add(1)
	return nil, nil
}

// newLockTestHousekeeping собирает уборщик с фейковым локом. ClickHouse не
// нужен: список узлов пуст, до соединения дело не доходит.
func newLockTestHousekeeping(lock CycleLock, lister NodeLister) *CHHousekeeping {
	h := NewCHHousekeeping(nil, lister, logging.NewNoop())
	return h.WithCycleLock(lock)
}

// TestRunCycleSkipsWhenLockTaken (§93.7): цикл второй реплики не выполняется.
//
// Без лока обе реплики Sender'а делают DROP PARTITION по одному списку: данные
// целы, но ClickHouse получает двойную работу, а в логах появляются ошибки
// «партиции нет» — там же, где потом ищут причину пропавших логов.
func TestRunCycleSkipsWhenLockTaken(t *testing.T) {
	t.Parallel()

	lister := &countingLister{}
	h := newLockTestHousekeeping(&fakeCycleLock{granted: false}, lister)

	h.runCycle(context.Background())

	assert.Equal(t, int64(0), lister.calls.Load(), "цикл без лока выполняться не должен")
}

// TestRunCycleRunsWhenLockGranted (§93.7): владелец лока убирается как обычно.
func TestRunCycleRunsWhenLockGranted(t *testing.T) {
	t.Parallel()

	lister := &countingLister{}
	h := newLockTestHousekeeping(&fakeCycleLock{granted: true}, lister)

	h.runCycle(context.Background())

	assert.Equal(t, int64(1), lister.calls.Load())
}

// TestRunCycleSkipsOnLockError (§93.7): ошибка Redis — запрет, а не разрешение.
//
// Fail-open здесь означал бы, что при недоступном Redis обе реплики убираются
// одновременно, то есть неисправность инфраструктуры включает ровно то
// поведение, от которого лок и защищает.
func TestRunCycleSkipsOnLockError(t *testing.T) {
	t.Parallel()

	lister := &countingLister{}
	h := newLockTestHousekeeping(&fakeCycleLock{err: errors.New("redis down")}, lister)

	h.runCycle(context.Background())

	assert.Equal(t, int64(0), lister.calls.Load(), "при ошибке лока цикл пропускается")
}

// TestRunCycleWithoutLock (§93.7): без лока (одиночная установка) поведение
// прежнее.
func TestRunCycleWithoutLock(t *testing.T) {
	t.Parallel()

	lister := &countingLister{}
	h := NewCHHousekeeping(nil, lister, logging.NewNoop())

	h.runCycle(context.Background())

	assert.Equal(t, int64(1), lister.calls.Load())
}

// TestCycleLockTTL (§93.7): TTL меньше периода, но не короче минуты.
//
// Больше периода — упавшая реплика заблокировала бы уборку на следующий тик
// тоже; меньше длительности цикла — вторая реплика вклинилась бы в середину.
func TestCycleLockTTL(t *testing.T) {
	t.Parallel()

	h := NewCHHousekeeping(nil, &countingLister{}, logging.NewNoop())
	require.Equal(t, 12*time.Hour, h.cycleLockTTL(), "суточный период → половина")

	h.period = 10 * time.Second
	assert.Equal(t, time.Minute, h.cycleLockTTL(), "короткий период не даёт TTL меньше минуты")
}

// TestRunCycleTTLRequested (§93.7): в лок уходит именно вычисленный TTL.
func TestRunCycleTTLRequested(t *testing.T) {
	t.Parallel()

	lock := &fakeCycleLock{granted: true}
	h := newLockTestHousekeeping(lock, &countingLister{})

	h.runCycle(context.Background())

	assert.Equal(t, int64(12*time.Hour), lock.lastTTL.Load())
}
