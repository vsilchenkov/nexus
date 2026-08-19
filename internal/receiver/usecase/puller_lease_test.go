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
	"nexus/internal/platform/logging"
)

// --- фейки аренды и списка узлов ---------------------------------------------

// fakeLease — управляемая аренда узлов (§93.8).
type fakeLease struct {
	mu        sync.Mutex
	granted   map[string]bool // имя ресурса → отдаём ли его нам
	def       bool            // ответ для имён, которых нет в granted
	err       error
	acquired  []string
	releases  []string
	lastTTLNs int64
}

func newFakeLease(def bool) *fakeLease {
	return &fakeLease{granted: map[string]bool{}, def: def}
}

func (f *fakeLease) Acquire(_ context.Context, name string, ttl time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acquired = append(f.acquired, name)
	f.lastTTLNs = int64(ttl)
	if f.err != nil {
		return false, f.err
	}
	if v, ok := f.granted[name]; ok {
		return v, nil
	}
	return f.def, nil
}

func (f *fakeLease) Release(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases = append(f.releases, name)
	return nil
}

func (f *fakeLease) releasedNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.releases...)
}

// fakeNodeLister отдаёт фиксированный список узлов RabbitMQAsync.
type fakeNodeLister struct {
	nodes []*domain.Node
	err   error
}

func (f *fakeNodeLister) ListRabbitMQAsync(context.Context) ([]*domain.Node, error) {
	return f.nodes, f.err
}

// failingConnector — соединение к RabbitMQ не устанавливается.
//
// Для этих тестов важен факт запуска воркера, а не его работа: воркер стартует,
// не может подключиться и уходит в свой backoff. Реальный брокер тут не нужен.
type failingConnector struct{}

func (failingConnector) Connect(context.Context, *domain.Node) (RMQConsumer, error) {
	return nil, errors.New("no broker in test")
}

func leaseTestNode(id, path string) *domain.Node {
	n := &domain.Node{
		ID:         id,
		Path:       path,
		RootMethod: domain.RootMethodRabbitMQAsync,
		TargetURL:  "https://api.partner.com/webhook",
		RMQHost:    "rmq.internal",
		RMQQueue:   "q." + path,
	}
	n.SetDefaults()
	return n
}

func newLeaseTestManager(t *testing.T, lease NodeLease, nodes ...*domain.Node) *PullerManager {
	t.Helper()
	mgr := NewPullerManager(
		&fakeNodeLister{nodes: nodes},
		failingConnector{},
		&fakeProducer{},
		"nexus.async",
		1<<20,
		15*time.Second,
		nil,
		nil,
		logging.NewNoop(),
	)
	if lease != nil {
		mgr = mgr.WithNodeLease(lease)
	}
	t.Cleanup(mgr.stopAll)
	return mgr
}

func (mgr *PullerManager) runningPaths() []string {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	out := make([]string, 0, len(mgr.running))
	for p := range mgr.running {
		out = append(out, p)
	}
	return out
}

// --- тесты -------------------------------------------------------------------

// TestReconcileStartsOnlyLeasedNodes (§93.8): воркер поднимается только на том
// узле, который ведёт эта реплика.
//
// Без аренды обе реплики Receiver'а читают одну очередь: дублей брокер не
// создаст, но темп вычитки удвоится против настроенного
// pull_batch_size × pull_interval_sec.
func TestReconcileStartsOnlyLeasedNodes(t *testing.T) {
	t.Parallel()

	mine := leaseTestNode("id-mine", "mine")
	theirs := leaseTestNode("id-theirs", "theirs")

	lease := newFakeLease(false)
	lease.granted[leaseName("id-mine")] = true

	mgr := newLeaseTestManager(t, lease, mine, theirs)
	mgr.reconcile(context.Background())

	assert.ElementsMatch(t, []string{"mine"}, mgr.runningPaths())
}

// TestReconcileStopsWorkerWhenLeaseLost (§93.8): узел, уведённый соседом,
// перестаёт обслуживаться здесь.
//
// Иначе после сетевой заминки очередь читали бы обе реплики — то есть ровно
// то состояние, которого аренда должна не допускать, и без единого сообщения
// об этом.
func TestReconcileStopsWorkerWhenLeaseLost(t *testing.T) {
	t.Parallel()

	n := leaseTestNode("id-1", "billing")
	lease := newFakeLease(true)

	mgr := newLeaseTestManager(t, lease, n)
	mgr.reconcile(context.Background())
	require.ElementsMatch(t, []string{"billing"}, mgr.runningPaths())

	// Сосед перехватил аренду.
	lease.mu.Lock()
	lease.granted[leaseName("id-1")] = false
	lease.mu.Unlock()

	mgr.reconcile(context.Background())
	assert.Empty(t, mgr.runningPaths(), "воркер уведённого узла должен быть остановлен")
}

// TestReconcileSkipsOnLeaseError (§93.8): ошибка Redis не запускает воркер.
//
// Fail-open здесь означал бы, что неисправность Redis включает удвоенную
// вычитку очередей — то самое, от чего аренда защищает. Сообщения при этом не
// теряются: они остаются в очереди RabbitMQ до восстановления.
func TestReconcileSkipsOnLeaseError(t *testing.T) {
	t.Parallel()

	lease := newFakeLease(true)
	lease.err = errors.New("redis down")

	mgr := newLeaseTestManager(t, lease, leaseTestNode("id-1", "billing"))
	mgr.reconcile(context.Background())

	assert.Empty(t, mgr.runningPaths())
}

// TestReconcileWithoutLease (§93.8): без аренды (одиночная установка) поведение
// прежнее — воркеры поднимаются на всех узлах.
func TestReconcileWithoutLease(t *testing.T) {
	t.Parallel()

	mgr := newLeaseTestManager(t, nil,
		leaseTestNode("id-1", "a"), leaseTestNode("id-2", "b"))
	mgr.reconcile(context.Background())

	assert.ElementsMatch(t, []string{"a", "b"}, mgr.runningPaths())
}

// TestLeaseRenewedForRunningWorkers (§93.8): аренда продлевается на каждом
// reconcile, отдельного таймера продления нет.
//
// Если бы продления не было, TTL истёк бы посреди работы и узел ушёл бы соседу
// при живом и здоровом ведущем.
func TestLeaseRenewedForRunningWorkers(t *testing.T) {
	t.Parallel()

	lease := newFakeLease(true)
	mgr := newLeaseTestManager(t, lease, leaseTestNode("id-1", "billing"))

	mgr.reconcile(context.Background())
	mgr.reconcile(context.Background())

	lease.mu.Lock()
	defer lease.mu.Unlock()
	assert.Equal(t, []string{leaseName("id-1"), leaseName("id-1")}, lease.acquired)
	assert.Equal(t, int64(30*time.Second), lease.lastTTLNs, "TTL — два периода reconcile")
}

// TestStopAllReleasesLeases (§93.8): при штатной остановке аренды отдаются.
//
// Без этого соседняя реплика ждала бы истечения TTL, и во время выката узлы
// RabbitMQAsync стояли бы без ведущего до двух периодов reconcile.
func TestStopAllReleasesLeases(t *testing.T) {
	t.Parallel()

	lease := newFakeLease(true)
	mgr := newLeaseTestManager(t, lease,
		leaseTestNode("id-1", "a"), leaseTestNode("id-2", "b"))
	mgr.reconcile(context.Background())
	require.Len(t, mgr.runningPaths(), 2)

	mgr.stopAll()

	assert.ElementsMatch(t,
		[]string{leaseName("id-1"), leaseName("id-2")},
		lease.releasedNames())
}
