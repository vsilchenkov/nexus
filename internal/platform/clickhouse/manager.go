package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"bus/internal/platform/config"
	"bus/internal/platform/logging"
)

// ConnProvider — read-only доступ к актуальному ClickHouse-соединению.
// Consumers (chlog.Writer, LogReaderCH, CHHousekeeping) принимают этот
// интерфейс вместо `driver.Conn`, чтобы Manager мог атомарно swap'ать
// conn при hot-reload без пересборки consumer'а.
type ConnProvider interface {
	Conn() driver.Conn
}

// Factory создаёт новый driver.Conn по конфигу. Сигнатура совпадает с
// функцией clickhouse.New, поэтому в production-коде передают её напрямую.
type Factory func(ctx context.Context, c *config.ClickHouseSection) (driver.Conn, error)

// Manager — владелец актуального ClickHouse-соединения. Реализует
// ConnProvider и подписывается на hot-reload (§8.4 / §14.5 ТЗ): при
// Reload открывает новый conn, делает Ping и атомарно подменяет старый.
//
// Старый conn закрывается с задержкой closeDelay, чтобы in-flight
// batch-insert'ы и SELECT'ы из других горутин успели завершиться.
// Это намеренный компромисс: мы не отслеживаем точно количество
// активных пользователей conn'а (это потребует ref-counting на каждый
// Conn() вызов), а полагаемся на то, что 15 секунд хватит для
// chlog.flushTable / LogReaderCH.GetByID при типовых нагрузках.
type Manager struct {
	factory    Factory
	cfg        *config.ClickHouseSection
	logger     logging.Logger
	closeDelay time.Duration

	mu      sync.RWMutex
	current driver.Conn

	stopMu  sync.Mutex
	stopped bool
}

// ManagerOption — функциональная опция конструктора NewManager.
type ManagerOption func(*Manager)

// WithCloseDelay задаёт задержку закрытия старого conn после swap.
// По умолчанию 15 секунд — достаточно для in-flight chlog-flush и
// SELECT'ов LogReaderCH. Тесты используют 0 для синхронного закрытия.
func WithCloseDelay(d time.Duration) ManagerOption {
	return func(m *Manager) { m.closeDelay = d }
}

// NewManager оборачивает уже открытый conn и фабрику для последующих
// reload'ов. cfg — указатель на живую секцию config.ClickHouseSection,
// которую перед вызовом Reload должен обновить overlay из app_settings.
func NewManager(initial driver.Conn, factory Factory, cfg *config.ClickHouseSection, logger logging.Logger, opts ...ManagerOption) *Manager {
	m := &Manager{
		factory:    factory,
		cfg:        cfg,
		logger:     logger,
		closeDelay: 15 * time.Second,
		current:    initial,
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// Conn возвращает актуальное соединение. Безопасно для concurrent-вызова.
// Возвращает nil, если Close уже отработал.
func (m *Manager) Conn() driver.Conn {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.current
}

// Reload открывает новый conn с текущим cfg, проверяет Ping и атомарно
// подменяет внутреннее соединение. Старое закрывается с задержкой
// closeDelay. При ошибке открытия / ping старое соединение НЕ затрагивается.
func (m *Manager) Reload(ctx context.Context) error {
	if m == nil || m.factory == nil {
		return errors.New("clickhouse.Manager: not initialized")
	}
	m.stopMu.Lock()
	stopped := m.stopped
	m.stopMu.Unlock()
	if stopped {
		return errors.New("clickhouse.Manager: closed")
	}

	newConn, err := m.factory(ctx, m.cfg)
	if err != nil {
		return fmt.Errorf("clickhouse reload: open: %w", err)
	}

	m.mu.Lock()
	old := m.current
	m.current = newConn
	m.mu.Unlock()

	addr := fmt.Sprintf("%s:%d", m.cfg.Host, m.cfg.Port)
	m.logger.Info("clickhouse conn swapped",
		m.logger.Str("addr", addr),
		m.logger.Str("db", m.cfg.Database))

	m.closeWithDelay(old)
	return nil
}

// Close закрывает текущее соединение и помечает Manager как закрытый —
// после этого Reload вернёт ошибку, Conn() — nil. Вызывается в App.Stop.
func (m *Manager) Close(_ context.Context) error {
	m.stopMu.Lock()
	if m.stopped {
		m.stopMu.Unlock()
		return nil
	}
	m.stopped = true
	m.stopMu.Unlock()

	m.mu.Lock()
	old := m.current
	m.current = nil
	m.mu.Unlock()

	if old != nil {
		if err := old.Close(); err != nil {
			return fmt.Errorf("clickhouse manager close: %w", err)
		}
	}
	return nil
}

func (m *Manager) closeWithDelay(c driver.Conn) {
	if c == nil {
		return
	}
	if m.closeDelay <= 0 {
		if err := c.Close(); err != nil {
			m.logger.Warn("clickhouse old conn close failed",
				m.logger.Err(err))
		}
		return
	}
	go func() {
		time.Sleep(m.closeDelay)
		if err := c.Close(); err != nil {
			m.logger.Warn("clickhouse old conn close failed",
				m.logger.Err(err))
		}
	}()
}
