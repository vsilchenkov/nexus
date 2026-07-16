// Package chlog — ClickHouse log writer для Sender (§4.3 ТЗ).
//
// Реализация:
//   - Запись через буферизированный канал (Write — неблокирующий).
//   - Фоновый воркер копит до batch_size или ждёт flush_interval_sec —
//     потом batch INSERT в ClickHouse.
//   - Отдельный батч на каждую таблицу узла (table → buffer).
//   - При недоступности ClickHouse: метрика nexus_clickhouse_errors_total
//     и ERROR в логе (уходит в Sentry); запрос НЕ блокируется (§9.4 ТЗ).
//     Проваленный батч отправляется в durable-топик Kafka nexus.logs.retry
//     (§38, см. BatchRetrier) — отдельный consumer-group дренит его обратно
//     в CH после восстановления. Заменяет прежний локальный NDJSON-fallback.
//
// При переполнении канала запись отбрасывается с метрикой
// nexus_clickhouse_dropped_total{reason="buffer_full"}.
package chlog

import (
	"context"
	"fmt"
	"sync"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"nexus/internal/domain"
	"nexus/internal/platform/config"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/metrics"
	"nexus/internal/platform/safego"
	"nexus/internal/sender/usecase/port"
)

// ConnProvider — узкий read-only доступ к ClickHouse-соединению.
// Определён на стороне consumer'а (CLAUDE.md §3): chlog.Writer не должен
// зависеть от конкретного владельца conn'а — clickhouse.Manager
// автоматически реализует этот интерфейс структурным совпадением.
type ConnProvider interface {
	Conn() chdriver.Conn
}

// BatchRetrier отправляет проваленный (не вставленный в CH) батч в durable-
// буфер (Kafka-топик nexus.logs.retry, §38), откуда его позже дренит обратно
// в CH отдельный consumer. Определён на стороне consumer'а (CLAUDE.md §3);
// реализуется adapter/out/chlogretry. nil → проваленные батчи теряются
// (как было с пустым fallback-каталогом).
type BatchRetrier interface {
	Retry(ctx context.Context, table string, batch []*domain.LogRecord) error
}

// Writer реализует port.LogWriter.
type Writer struct {
	conn    ConnProvider
	cfg     *config.ClickHouseSection
	logger  logging.Logger
	metrics *metrics.Metrics

	ch chan job

	mu       sync.Mutex
	buffers  map[string][]*domain.LogRecord
	bufferAt map[string]time.Time

	retrier BatchRetrier // §38: durable-retry проваленных батчей через Kafka

	wg     sync.WaitGroup
	stopCh chan struct{}
}

var _ port.LogWriter = (*Writer)(nil)

type job struct {
	table string
	rec   *domain.LogRecord
}

func New(conn ConnProvider, cfg *config.ClickHouseSection, logger logging.Logger) *Writer {
	return NewWithRetrier(conn, cfg, nil, nil, logger)
}

// NewWithRetrier — вариант с durable-retry проваленных батчей через Kafka
// (§38) и опциональными Prometheus-метриками (§6 ТЗ).
// retrier = nil — retry отключён, проваленные батчи теряются (как в Phase 1).
// m = nil — метрики не публикуются (тестовый режим).
func NewWithRetrier(conn ConnProvider, cfg *config.ClickHouseSection, retrier BatchRetrier, m *metrics.Metrics, logger logging.Logger) *Writer {
	w := &Writer{
		conn:     conn,
		cfg:      cfg,
		logger:   logger,
		metrics:  m,
		ch:       make(chan job, cfg.BufferMaxSize),
		buffers:  make(map[string][]*domain.LogRecord),
		bufferAt: make(map[string]time.Time),
		retrier:  retrier,
		stopCh:   make(chan struct{}),
	}
	for i := 0; i < cfg.Workers; i++ {
		w.wg.Add(1)
		go w.run()
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
	defer safego.Recover(w.logger, "sender.chlogWriter")
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

	// §51.9: успешный INSERT раньше не оставлял следа (rows/длительность видны
	// только на ошибке) — «куда делись логи узла» было нечем диагностировать.
	insertStart := time.Now()
	if err := w.insertBatch(ctx, table, batch); err == nil {
		w.logger.Debug("clickhouse batch inserted",
			w.logger.Str("table", table),
			w.logger.Int("rows", len(batch)),
			w.logger.Int("duration_ms", int(time.Since(insertStart).Milliseconds())))
	} else {
		w.logger.ErrorWithOp("clickhouse batch insert failed", err, "chlog.flushTable",
			w.logger.Str("table", table),
			w.logger.Int("rows", len(batch)))
		if w.metrics != nil {
			w.metrics.CHErrorsTotal.WithLabelValues(table, "insert").Inc()
		}
		// §38: проваленный батч уходит в durable-топик Kafka nexus.logs.retry;
		// отдельный consumer дренит его обратно в CH после восстановления.
		if w.retrier != nil {
			if rerr := w.retrier.Retry(ctx, table, batch); rerr != nil {
				// И CH, и Kafka недоступны — батч потерян (NDJSON-fallback
				// убран по §38). ERROR → Sentry, чтобы потеря была видна.
				w.logger.ErrorWithOp("clickhouse batch retry-produce failed", rerr, "chlog.flushTable.retry",
					w.logger.Str("table", table),
					w.logger.Int("rows", len(batch)))
				if w.metrics != nil {
					w.metrics.CHErrorsTotal.WithLabelValues(table, "retry_produce").Inc()
				}
			} else {
				w.logger.Info("batch queued to kafka retry topic",
					w.logger.Str("table", table),
					w.logger.Int("rows", len(batch)))
				if w.metrics != nil {
					w.metrics.CHFallbackTotal.WithLabelValues(table, "queued").Add(float64(len(batch)))
				}
			}
		}
	}
}

const insertSQL = `INSERT INTO %s (
	ID, type, http_method, url, method, parameters, request, response,
	status, reason, date_create, date_request, date_response,
	duration, done, checksum_request, checksum_response,
	Host, IP, attempts, attempts_details, node_id,
	request_size, response_size
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
			r.ID, string(r.Type), r.HTTPMethod, r.URL, r.Method, r.Parameters, r.Request, r.Response,
			r.Status, r.Reason, r.DateCreate, r.DateRequest, r.DateResponse,
			r.Duration, r.Done, r.ChecksumRequest, r.ChecksumResponse,
			r.Host, r.IP, r.Attempts, r.AttemptsDetails, r.NodeID,
			r.RequestSize, r.ResponseSize,
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

// InsertBatch — прямой синхронный INSERT батча в CH, в обход буфера (§38).
// Используется retry-consumer'ом для дренажа nexus.logs.retry обратно в CH.
// НЕ вызывает retrier при ошибке (иначе возник бы цикл produce↔consume) —
// ошибку возвращает вызывающему, тот не коммитит offset и Kafka передоставит.
func (w *Writer) InsertBatch(ctx context.Context, table string, batch []*domain.LogRecord) error {
	return w.insertBatch(ctx, table, batch)
}

// Flush — сбрасывает все буферы; используется в graceful shutdown.
func (w *Writer) Flush(ctx context.Context) error {
	w.flushAll(ctx)
	return nil
}

// Stop останавливает воркеры и flush'ит остаток. Вызывается в App.Stop.
//
// ctx вызывающего игнорируется намеренно: к моменту финального flush его
// бюджет может быть почти исчерпан ожиданием воркеров, а терять последний
// батч из-за этого нельзя — берём свежий таймаут (при провале батч уйдёт
// в Kafka retry-топик, как обычно — §38).
func (w *Writer) Stop(_ context.Context) {
	close(w.stopCh)
	w.wg.Wait()
	// Дренаж канала: воркеры могли выйти по stopCh, не выбрав остаток
	// job'ов из w.ch (select между ветками не упорядочен) — без дренажа
	// эти записи молча теряются при shutdown.
drain:
	for {
		select {
		case j := <-w.ch:
			w.append(j.table, j.rec)
		default:
			break drain
		}
	}
	flushCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	w.flushAll(flushCtx)
}
