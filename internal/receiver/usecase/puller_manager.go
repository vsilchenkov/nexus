package usecase

import (
	"context"
	"sync"
	"time"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/metrics"
	"nexus/internal/platform/safego"
)

// PullerManager — управляет жизненным циклом Puller-воркеров (§27.2). Один
// воркер на узел RabbitMQAsync. Периодически сверяет список узлов из PG с
// запущенными воркерами (reconcile): поднимает новые, останавливает удалённые,
// перезапускает при изменении конфига (по updated_at).
//
// Узлового pub/sub для CRUD в системе нет (Redis-инвалидация кеша — по path,
// без события), поэтому используется периодический reconcile. Задержка старта
// нового узла ограничена reconcileInterval — приемлемо для v1.
// NodeLease — аренда «этот узел ведёт эта реплика» (§93.8). Интерфейс объявлен
// на стороне консьюмера (CLAUDE.md §3); реализуется platform/redislock.Lease.
// nil = поведение до §93: воркеры поднимаются на всех репликах.
type NodeLease interface {
	Acquire(ctx context.Context, name string, ttl time.Duration) (bool, error)
	Release(ctx context.Context, name string) error
}

type PullerManager struct {
	lister          PullNodeLister
	connector       RMQConnector
	producer        AsyncProducer
	asyncTopic      string
	maxMessageBytes int
	reconcileEvery  time.Duration
	metrics         *metrics.Metrics
	sink            HealthSink
	lease           NodeLease
	logger          logging.Logger

	mu      sync.Mutex
	running map[string]*workerHandle
	wg      sync.WaitGroup
}

type workerHandle struct {
	cancel    context.CancelFunc
	updatedAt time.Time
	// nodeID — под каким именем взята аренда (§93.8). Хранится здесь, потому
	// что running индексирован по пути, а путь у узла меняется.
	nodeID string
}

func NewPullerManager(
	lister PullNodeLister,
	connector RMQConnector,
	producer AsyncProducer,
	asyncTopic string,
	maxMessageBytes int,
	reconcileEvery time.Duration,
	m *metrics.Metrics,
	sink HealthSink,
	logger logging.Logger,
) *PullerManager {
	if reconcileEvery <= 0 {
		reconcileEvery = 15 * time.Second
	}
	return &PullerManager{
		lister:          lister,
		connector:       connector,
		producer:        producer,
		asyncTopic:      asyncTopic,
		maxMessageBytes: maxMessageBytes,
		reconcileEvery:  reconcileEvery,
		metrics:         m,
		sink:            sink,
		logger:          logger,
		running:         map[string]*workerHandle{},
	}
}

// WithNodeLease подключает аренду узлов (§93.8) и возвращает тот же экземпляр
// для цепочки в wiring.
//
// Зачем это нужно именно здесь: воркер тянет сообщения из очереди RabbitMQ
// через basic.get. Две реплики Receiver'а дублей не создадут — брокер отдаёт
// сообщение одному потребителю, — но вычитка пойдёт вдвое быстрее заданного
// pull_batch_size × pull_interval_sec, а порядок доставки во внешний узел
// перемешается сильнее обычного. Узел настраивали под конкретный темп, и
// удваивать его молча, просто потому что появилась вторая реплика, нельзя.
//
// Аренда оставляет узел одной реплике; вторая подхватит его не позже TTL после
// падения соседа, а при штатной остановке — сразу (Release в stopAll).
func (mgr *PullerManager) WithNodeLease(l NodeLease) *PullerManager {
	mgr.lease = l
	return mgr
}

// leaseReleaseTimeout — бюджет на отдачу аренд при остановке. Секунды: это
// один-два Redis-запроса на узел, и затягивать остановку ими нельзя.
const leaseReleaseTimeout = 3 * time.Second

// leaseName — имя арендуемого ресурса. По ID узла, а не по пути: путь
// меняется при переименовании, и на время reconcile'а узел оказался бы
// арендован под двумя именами сразу.
func leaseName(nodeID string) string { return "puller:" + nodeID }

// leaseTTL — вдвое больше периода reconcile: одного пропущенного цикла (сборка
// мусора, заминка Redis) не хватает, чтобы отдать узел соседу, а падение
// реплики отдаёт его максимум через два периода.
func (mgr *PullerManager) leaseTTL() time.Duration { return 2 * mgr.reconcileEvery }

// holdsLease — ведёт ли эта реплика узел. Продлевает аренду, если ведёт.
//
// Ошибка Redis трактуется как «не ведём»: fail-open здесь означал бы, что
// неисправность Redis включает удвоенную вычитку очередей — то самое, от чего
// аренда защищает. Узел в этом случае временно не обслуживается ни одной
// репликой, что для pull-модели безопасно: сообщения остаются в очереди
// RabbitMQ и будут забраны, когда Redis вернётся.
func (mgr *PullerManager) holdsLease(ctx context.Context, n *domain.Node) bool {
	if mgr.lease == nil {
		return true
	}
	ok, err := mgr.lease.Acquire(ctx, leaseName(n.ID), mgr.leaseTTL())
	if err != nil {
		mgr.logger.Warn("puller: node lease check failed, worker not started",
			mgr.logger.Str("node", n.Path), mgr.logger.Err(err))
		return false
	}
	if !ok {
		mgr.logger.Debug("puller: node is led by another replica",
			mgr.logger.Str("node", n.Path))
	}
	return ok
}

// Run запускает reconcile-цикл до отмены ctx. Блокирующий — вызывается в
// отдельной горутине из app.Start. По отмене ctx останавливает все воркеры
// (graceful) и ждёт их завершения с таймаутом.
func (mgr *PullerManager) Run(ctx context.Context) {
	mgr.reconcile(ctx)
	t := time.NewTicker(mgr.reconcileEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			mgr.stopAll()
			return
		case <-t.C:
			mgr.reconcile(ctx)
		}
	}
}

// reconcile сверяет желаемое состояние (узлы из PG) с текущим (running).
func (mgr *PullerManager) reconcile(ctx context.Context) {
	nodes, err := mgr.lister.ListRabbitMQAsync(ctx)
	if err != nil {
		mgr.logger.Warn("puller reconcile: list nodes failed", mgr.logger.Err(err))
		return
	}

	desired := make(map[string]*domain.Node, len(nodes))
	for _, n := range nodes {
		desired[n.Path] = n
	}

	mgr.mu.Lock()
	defer mgr.mu.Unlock()

	// Остановить удалённые и изменённые (конфиг поменялся → перезапуск).
	for path, h := range mgr.running {
		n, ok := desired[path]
		if !ok {
			mgr.stopLocked(path, h)
			continue
		}
		if n.UpdatedAt.After(h.updatedAt) {
			mgr.logger.Info("puller: node config changed, restarting worker",
				mgr.logger.Str("node", path))
			mgr.stopLocked(path, h)
		}
	}

	// Запустить недостающие — и только те узлы, которые ведёт эта реплика.
	// Аренда продлевается здесь же для уже работающих воркеров: отдельного
	// таймера продления нет, его роль играет сам reconcile-тик.
	for path, n := range desired {
		if !mgr.holdsLease(ctx, n) {
			// Узел увели (соседняя реплика перехватила аренду после нашей
			// заминки) — воркер надо остановить, иначе очередь читают двое.
			if h, ok := mgr.running[path]; ok {
				mgr.logger.Info("puller: node lease lost, stopping worker",
					mgr.logger.Str("node", path))
				mgr.stopLocked(path, h)
			}
			continue
		}
		if _, ok := mgr.running[path]; ok {
			continue
		}
		mgr.startLocked(ctx, n)
	}
}

// startLocked поднимает воркер на узел (mu удерживается вызывающим).
func (mgr *PullerManager) startLocked(parent context.Context, n *domain.Node) {
	// context.WithoutCancel: воркер живёт по своему cancel, не по parent —
	// иначе reconcile-tick парента отменил бы его. Но завершается при Stop.
	wctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	w := NewPullerWorker(n, mgr.connector, mgr.producer, mgr.asyncTopic,
		mgr.maxMessageBytes, mgr.metrics, mgr.sink, mgr.logger)
	mgr.running[n.Path] = &workerHandle{cancel: cancel, updatedAt: n.UpdatedAt, nodeID: n.ID}
	mgr.wg.Go(func() {
		defer safego.Recover(mgr.logger, "receiver.pullerWorker")
		w.Run(wctx)
	})
	mgr.logger.Info("puller: worker started", mgr.logger.Str("node", n.Path))
}

func (mgr *PullerManager) stopLocked(path string, h *workerHandle) {
	h.cancel()
	delete(mgr.running, path)
	mgr.logger.Info("puller: worker stopped", mgr.logger.Str("node", path))
}

// stopAll останавливает все воркеры и ждёт graceful-завершения (§27.5).
func (mgr *PullerManager) stopAll() {
	mgr.mu.Lock()
	released := make([]string, 0, len(mgr.running))
	for path, h := range mgr.running {
		h.cancel()
		if h.nodeID != "" {
			released = append(released, h.nodeID)
		}
		delete(mgr.running, path)
	}
	mgr.mu.Unlock()

	mgr.releaseLeases(released)

	done := make(chan struct{})
	go func() { mgr.wg.Wait(); close(done) }()
	// Таймаут ГЛОБАЛЬНЫЙ на все воркеры сразу, не per-worker: воркеры
	// останавливаются параллельно, каждому хватает rmqBatchTimeout на
	// добивание текущего батча, +2s — запас на nack/закрытие каналов.
	select {
	case <-done:
	case <-time.After(rmqBatchTimeout + 2*time.Second):
		mgr.logger.Warn("puller: graceful stop timed out (global budget, not per-worker), some workers may not have finished",
			mgr.logger.Int("budget_sec", int((rmqBatchTimeout+2*time.Second).Seconds())))
	}
}

// releaseLeases отдаёт аренду узлов при штатной остановке реплики (§93.8).
//
// Без этого соседняя реплика ждала бы истечения TTL, и во время выката узлы
// RabbitMQAsync стояли бы без ведущего до двух периодов reconcile. Отдаём
// параллельно остановке воркеров — сообщение, которое уже взято из очереди,
// доигрывается своим чередом.
//
// Контекст здесь свой: stopAll вызывается по отмене ctx приложения, и запрос к
// Redis с отменённым контекстом не ушёл бы вовсе.
func (mgr *PullerManager) releaseLeases(nodeIDs []string) {
	if mgr.lease == nil || len(nodeIDs) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), leaseReleaseTimeout)
	defer cancel()
	for _, id := range nodeIDs {
		if err := mgr.lease.Release(ctx, leaseName(id)); err != nil {
			// Не фатально: аренда всё равно истечёт по TTL, соседняя реплика
			// подхватит узел чуть позже.
			mgr.logger.Warn("puller: node lease release failed",
				mgr.logger.Str("node_id", id), mgr.logger.Err(err))
		}
	}
}
