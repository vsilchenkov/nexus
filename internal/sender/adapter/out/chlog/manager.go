package chlog

import (
	"context"
	"sync"

	"nexus/internal/domain"
	"nexus/internal/platform/config"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/metrics"
	"nexus/internal/sender/usecase/port"
)

// WriterManager — обёртка над *Writer, реализующая port.LogWriter и
// поддерживающая полное пересоздание writer'а при hot-reload (§6.3.2.5
// Phase 6 / §8.4 ТЗ).
//
// Зачем нужен:
//   - смена ClickHouse.Host/User/Password обрабатывается на уровне
//     clickhouse.Manager (атомарный swap conn — Writer этого не замечает);
//   - но смена BufferMaxSize / Workers требует пересоздания канала и
//     горутин — это делает Reload().
//
// Гарантии:
//   - Write / Flush делегируются текущему writer'у под RWMutex;
//   - Reload синхронно делает Flush на старом writer'е и Stop, затем
//     поднимает новый — отсутствие потерь логов при условии, что
//     внешний caller не пишет в момент swap'а быстрее, чем flush.
//     На практике hot-reload идёт раз в минуты/часы, а flush укладывается
//     в миллисекунды, так что потери не накапливаются.
type WriterManager struct {
	provider    ConnProvider
	cfg         *config.ClickHouseSection
	fallbackDir string
	metrics     *metrics.Metrics
	logger      logging.Logger

	mu      sync.RWMutex
	current *Writer
	stopped bool
}

var _ port.LogWriter = (*WriterManager)(nil)

// NewManagerWithFallback создаёт WriterManager и сразу поднимает первый
// Writer с переданными параметрами. cfg — указатель на живую секцию
// конфига, которую Reload использует при пересоздании.
func NewManagerWithFallback(provider ConnProvider, cfg *config.ClickHouseSection, fallbackDir string, m *metrics.Metrics, logger logging.Logger) *WriterManager {
	wm := &WriterManager{
		provider:    provider,
		cfg:         cfg,
		fallbackDir: fallbackDir,
		metrics:     m,
		logger:      logger,
	}
	wm.current = NewWithFallback(provider, cfg, fallbackDir, m, logger)
	return wm
}

// Write делегирует текущему writer'у. Безопасно для concurrent-вызова.
func (m *WriterManager) Write(ctx context.Context, table string, rec *domain.LogRecord) {
	m.mu.RLock()
	w := m.current
	m.mu.RUnlock()
	if w == nil {
		return
	}
	w.Write(ctx, table, rec)
}

// Flush делегирует текущему writer'у.
func (m *WriterManager) Flush(ctx context.Context) error {
	m.mu.RLock()
	w := m.current
	m.mu.RUnlock()
	if w == nil {
		return nil
	}
	return w.Flush(ctx)
}

// Reload пересоздаёт внутренний Writer с актуальным cfg. Старый —
// flush'ит и останавливает, чтобы не оставлять висящих горутин и
// неотправленных батчей. Вызывается из bootstrap.ClickHouseReloader
// после успешного clickhouse.Manager.Reload.
func (m *WriterManager) Reload(ctx context.Context) error {
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return nil
	}
	old := m.current
	fresh := NewWithFallback(m.provider, m.cfg, m.fallbackDir, m.metrics, m.logger)
	m.current = fresh
	m.mu.Unlock()

	if old != nil {
		old.Stop(ctx)
		m.logger.Info("chlog writer recreated after hot-reload",
			m.logger.Int("workers", m.cfg.Workers),
			m.logger.Int("buffer_max_size", m.cfg.BufferMaxSize),
			m.logger.Int("batch_size", m.cfg.BatchSize))
	}
	return nil
}

// Stop останавливает текущий writer и помечает manager как закрытый.
// После Stop последующие Write — no-op, Reload возвращает nil без действий.
// Используется в App.Stop.
func (m *WriterManager) Stop(ctx context.Context) {
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return
	}
	m.stopped = true
	old := m.current
	m.current = nil
	m.mu.Unlock()

	if old != nil {
		old.Stop(ctx)
	}
}
