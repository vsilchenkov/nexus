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
type PullerManager struct {
	lister          PullNodeLister
	connector       RMQConnector
	producer        AsyncProducer
	asyncTopic      string
	maxMessageBytes int
	reconcileEvery  time.Duration
	metrics         *metrics.Metrics
	sink            HealthSink
	logger          logging.Logger

	mu      sync.Mutex
	running map[string]*workerHandle
	wg      sync.WaitGroup
}

type workerHandle struct {
	cancel    context.CancelFunc
	updatedAt time.Time
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

	// Запустить недостающие.
	for path, n := range desired {
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
	mgr.running[n.Path] = &workerHandle{cancel: cancel, updatedAt: n.UpdatedAt}
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
	for path, h := range mgr.running {
		h.cancel()
		delete(mgr.running, path)
	}
	mgr.mu.Unlock()

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
