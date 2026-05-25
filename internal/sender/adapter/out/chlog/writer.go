// Package chlog — ClickHouse log writer для Sender (§4.3 ТЗ).
//
// Реализация:
//   - Запись через буферизированный канал (Write — неблокирующий).
//   - Фоновый воркер копит до batch_size или ждёт flush_interval_sec —
//     потом batch INSERT в ClickHouse.
//   - Отдельный батч на каждую таблицу узла (table → buffer).
//   - При недоступности ClickHouse: метрика databus_clickhouse_dropped_total
//     (TODO Phase 4) + warning в логе; запрос НЕ блокируется (§9.4 ТЗ).
//
// File-fallback при переполнении — TODO Phase 4. Сейчас при переполнении
// канала запись просто отбрасывается.
package chlog

import (
	"context"
	"fmt"
	"sync"
	"time"

	chpf "bus/internal/platform/clickhouse"
	"bus/internal/domain"
	"bus/internal/platform/config"
	"bus/internal/platform/logging"
	"bus/internal/platform/metrics"
	"bus/internal/sender/usecase/port"
)

// Writer реализует port.LogWriter.
type Writer struct {
	conn    chpf.ConnProvider
	cfg     *config.ClickHouseSection
	logger  logging.Logger
	metrics *metrics.Metrics

	ch chan job

	mu       sync.Mutex
	buffers  map[string][]*domain.LogRecord
	bufferAt map[string]time.Time

	fallback *fallbackStore // §9.4 ТЗ: NDJSON-fallback при недоступности CH

	wg     sync.WaitGroup
	stopCh chan struct{}
}

var _ port.LogWriter = (*Writer)(nil)

type job struct {
	table string
	rec   *domain.LogRecord
}

func New(conn chpf.ConnProvider, cfg *config.ClickHouseSection, logger logging.Logger) *Writer {
	return NewWithFallback(conn, cfg, "", nil, logger)
}

// NewWithFallback — вариант с явным каталогом для file-fallback (§9.4)
// и опциональными Prometheus-метриками (§6 ТЗ).
// fallbackDir = "" — fallback отключён, проваленные батчи теряются (как в Phase 1).
// m = nil — метрики не публикуются (тестовый режим).
func NewWithFallback(conn chpf.ConnProvider, cfg *config.ClickHouseSection, fallbackDir string, m *metrics.Metrics, logger logging.Logger) *Writer {
	w := &Writer{
		conn:     conn,
		cfg:      cfg,
		logger:   logger,
		metrics:  m,
		ch:       make(chan job, cfg.BufferMaxSize),
		buffers:  make(map[string][]*domain.LogRecord),
		bufferAt: make(map[string]time.Time),
		fallback: newFallbackStore(fallbackDir, 30*time.Second, logger),
		stopCh:   make(chan struct{}),
	}
	for i := 0; i < cfg.Workers; i++ {
		w.wg.Add(1)
		go w.run()
	}
	if w.fallback.Enabled() {
		go w.fallback.Run(context.Background(), func(ctx context.Context, table string, batch []*domain.LogRecord) error {
			if err := w.insertBatch(ctx, table, batch); err != nil {
				return err
			}
			if w.metrics != nil {
				w.metrics.CHFallbackTotal.WithLabelValues(table, "restored").Add(float64(len(batch)))
			}
			return nil
		})
	}
	return w
}

// Write — неблокирующая: если канал полон, лог теряется (с предупреждением
// и метрикой). Альтернатива — блокировать обработку запроса, что хуже.
func (w *Writer) Write(_ context.Context, table string, rec *domain.LogRecord) {
	if table == "" {
		w.logger.Warn("clickhouse write skipped: empty table",
			w.logger.Str("id", rec.ID))
		if w.metrics != nil {
			w.metrics.CHDroppedTotal.WithLabelValues("", "empty_table").Inc()
		}
		return
	}
	select {
	case w.ch <- job{table: table, rec: rec}:
	default:
		w.logger.Warn("clickhouse write dropped: buffer full",
			w.logger.Str("table", table), w.logger.Str("id", rec.ID))
		if w.metrics != nil {
			w.metrics.CHDroppedTotal.WithLabelValues(table, "buffer_full").Inc()
		}
	}
}

func (w *Writer) run() {
	defer w.wg.Done()
	tick := time.NewTicker(time.Duration(w.cfg.FlushIntervalSec) * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-w.stopCh:
			w.flushAll(context.Background())
			return
		case j := <-w.ch:
			w.append(j.table, j.rec)
		case <-tick.C:
			w.flushAll(context.Background())
		}
	}
}

func (w *Writer) append(table string, rec *domain.LogRecord) {
	w.mu.Lock()
	w.buffers[table] = append(w.buffers[table], rec)
	if _, ok := w.bufferAt[table]; !ok {
		w.bufferAt[table] = time.Now()
	}
	size := len(w.buffers[table])
	full := size >= w.cfg.BatchSize
	w.mu.Unlock()

	if w.metrics != nil {
		w.metrics.CHBufferSize.WithLabelValues(table).Set(float64(size))
	}
	if full {
		w.flushTable(context.Background(), table)
	}
}

func (w *Writer) flushAll(ctx context.Context) {
	w.mu.Lock()
	tables := make([]string, 0, len(w.buffers))
	for t := range w.buffers {
		tables = append(tables, t)
	}
	w.mu.Unlock()

	for _, t := range tables {
		w.flushTable(ctx, t)
	}
}

func (w *Writer) flushTable(ctx context.Context, table string) {
	w.mu.Lock()
	batch := w.buffers[table]
	w.buffers[table] = nil
	delete(w.bufferAt, table)
	w.mu.Unlock()

	if w.metrics != nil {
		w.metrics.CHBufferSize.WithLabelValues(table).Set(0)
	}
	if len(batch) == 0 {
		return
	}

	if err := w.insertBatch(ctx, table, batch); err != nil {
		w.logger.ErrorWithOp("clickhouse batch insert failed", err, "chlog.flushTable",
			w.logger.Str("table", table),
			w.logger.Int("rows", len(batch)))
		if w.metrics != nil {
			w.metrics.CHErrorsTotal.WithLabelValues(table, "insert").Inc()
		}
		if w.fallback.Enabled() {
			path, ferr := w.fallback.Save(table, batch)
			if ferr != nil {
				w.logger.ErrorWithOp("fallback save failed", ferr, "chlog.flushTable.fallback",
					w.logger.Str("table", table),
					w.logger.Int("rows", len(batch)))
				if w.metrics != nil {
					w.metrics.CHErrorsTotal.WithLabelValues(table, "fallback_save").Inc()
				}
			} else {
				w.logger.Info("batch persisted to file-fallback",
					w.logger.Str("file", path),
					w.logger.Int("rows", len(batch)))
				if w.metrics != nil {
					w.metrics.CHFallbackTotal.WithLabelValues(table, "saved").Inc()
				}
			}
		}
	}
}

const insertSQL = `INSERT INTO %s (
	ID, type, url, method, parameters, request, response,
	status, reason, date_create, date_request, date_response,
	duration, done, checksum_request, checksum_response,
	Host, IP, attempts, attempts_details
)`

func (w *Writer) insertBatch(ctx context.Context, table string, batch []*domain.LogRecord) error {
	bctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	conn := w.conn.Conn()
	if conn == nil {
		return fmt.Errorf("clickhouse conn is nil")
	}
	bt, err := conn.PrepareBatch(bctx, fmt.Sprintf(insertSQL, table))
	if err != nil {
		return fmt.Errorf("prepare batch %s: %w", table, err)
	}
	for _, r := range batch {
		err := bt.Append(
			r.ID, string(r.Type), r.URL, r.Method, r.Parameters, r.Request, r.Response,
			r.Status, r.Reason, r.DateCreate, r.DateRequest, r.DateResponse,
			r.Duration, r.Done, r.ChecksumRequest, r.ChecksumResponse,
			r.Host, r.IP, r.Attempts, r.AttemptsDetails,
		)
		if err != nil {
			return fmt.Errorf("append row: %w", err)
		}
	}
	if err := bt.Send(); err != nil {
		return fmt.Errorf("send batch %s: %w", table, err)
	}
	return nil
}

// Flush — сбрасывает все буферы; используется в graceful shutdown.
func (w *Writer) Flush(ctx context.Context) error {
	w.flushAll(ctx)
	return nil
}

// Stop останавливает воркеры и flush'ит остаток. Вызывается в App.Stop.
func (w *Writer) Stop(ctx context.Context) {
	close(w.stopCh)
	w.wg.Wait()
	w.flushAll(ctx)
	w.fallback.Stop()
}
