package usecase

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/robfig/cron/v3"
	"golang.org/x/sync/errgroup"

	"nexus/internal/domain"
	chpf "nexus/internal/platform/clickhouse"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/safego"
)

// CHTableLister — список лог-таблиц узлов (§106.3). Реализуется
// nodepg.Reader; внешние таблицы §64 исключены уже в его SQL.
type CHTableLister interface {
	ListClickHouseTables(ctx context.Context) ([]string, error)
}

// ManagedTableFilter — мягкий гейт владения §70.4: оставляет таблицы, которые
// эта нода вправе обслуживать. Реализуется clickhouse.Guard. nil — без гейта.
type ManagedTableFilter interface {
	FilterManagedTables(ctx context.Context, tables []string) []string
}

// BlockingHostResolver — синхронный резолв имени клиента полной цепочкой
// Redis → PTR → NetBIOS (§106.2). Реализуется rdns.Resolver.ResolveNow.
type BlockingHostResolver interface {
	ResolveNow(ctx context.Context, ip string) string
}

const (
	// backfillMaxIPs — потолок DISTINCT IP с пустым именем за цикл на таблицу.
	backfillMaxIPs = 1000
	// backfillParallelism — одновременных резолвов: PTR и NetBIOS ждут сеть.
	backfillParallelism = 8
	// backfillQueryTimeout — таймаут SELECT DISTINCT и постановки мутации.
	backfillQueryTimeout = 2 * time.Minute
	// backfillLockTTL — лок цикла между репликами: дольше цикла, короче
	// любого разумного интервала расписания.
	backfillLockTTL = 15 * time.Minute
)

// ClientHostBackfill дописывает имена клиентов в строки логов, записанные без
// них (§106.3): первая запись нового IP, окно негативного кеша, NetBIOS или
// PTR появились позже. Расписание — cron из «Настройки → Общие» (§106.4),
// меняется без рестарта через SetSchedule.
//
// Cron лишь сигналит в канал, а сам цикл выполняет горутина Run: контекст
// приложения никуда не сохраняется, циклы не накладываются друг на друга, а
// остановка Run означает, что цикл закончен и ClickHouse можно закрывать.
type ClientHostBackfill struct {
	ch      ConnProvider
	tables  CHTableLister
	hosts   BlockingHostResolver
	managed ManagedTableFilter
	lock    CycleLock
	logger  logging.Logger

	// trigger — тик расписания. Буфер 1: пока идёт цикл, ещё один тик ждёт,
	// остальные отбрасываются.
	trigger chan struct{}

	mu      sync.Mutex
	cron    *cron.Cron
	curExpr string
	stopped bool // Run завершён: новое расписание не взводится
}

// NewClientHostBackfill создаёт задачу без расписания: до SetSchedule циклов нет.
func NewClientHostBackfill(ch ConnProvider, tables CHTableLister, hosts BlockingHostResolver, logger logging.Logger) *ClientHostBackfill {
	return &ClientHostBackfill{
		ch: ch, tables: tables, hosts: hosts, logger: logger,
		trigger: make(chan struct{}, 1),
	}
}

// WithManagedFilter подключает гейт владения §70.4 (цепочка в wiring).
func (b *ClientHostBackfill) WithManagedFilter(f ManagedTableFilter) *ClientHostBackfill {
	b.managed = f
	return b
}

// WithCycleLock подключает лок цикла между репликами (цепочка в wiring).
func (b *ClientHostBackfill) WithCycleLock(l CycleLock) *ClientHostBackfill {
	b.lock = l
	return b
}

// Run выполняет циклы по тикам расписания и блокируется до ctx.Done. После
// выхода расписание остановлено и новое не взводится.
func (b *ClientHostBackfill) Run(ctx context.Context) {
	defer b.shutdown()
	for {
		select {
		case <-ctx.Done():
			return
		case <-b.trigger:
			b.runCycle(ctx)
		}
	}
}

// SetSchedule применяет расписание (§106.4): "" выключает задачу, то же
// выражение — no-op, новое пересобирает планировщик (у robfig/cron запись
// нельзя перенастроить, только пересоздать). Невалидное выражение возвращает
// ошибку и оставляет прежнее расписание.
func (b *ClientHostBackfill) SetSchedule(expr string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopped {
		b.logger.Debug("client_host backfill: stopped, schedule ignored", b.logger.Str("cron", expr))
		return nil
	}
	if expr == "" {
		if b.cron != nil {
			b.stopLocked()
			b.logger.Info("client_host backfill disabled")
		}
		return nil
	}
	if expr == b.curExpr && b.cron != nil {
		b.logger.Debug("client_host backfill: schedule unchanged", b.logger.Str("cron", expr))
		return nil
	}
	c := cron.New()
	if _, err := c.AddFunc(expr, b.fire); err != nil {
		return fmt.Errorf("client_host backfill: cron %q: %w", expr, err)
	}
	b.stopLocked()
	c.Start()
	b.cron, b.curExpr = c, expr
	b.logger.Info("client_host backfill armed", b.logger.Str("cron", expr))
	return nil
}

// fire — тик cron: исполняется в горутине robfig/cron, поэтому свой recover.
func (b *ClientHostBackfill) fire() {
	defer safego.Recover(b.logger, "sender.clientHostBackfill.tick")
	select {
	case b.trigger <- struct{}{}:
	default:
		// §51.9: цикл идёт дольше интервала — лишний тик отброшен.
		b.logger.Debug("client_host backfill: tick skipped, cycle still running")
	}
}

func (b *ClientHostBackfill) shutdown() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stopLocked()
	b.stopped = true
}

func (b *ClientHostBackfill) stopLocked() {
	if b.cron != nil {
		b.cron.Stop() // задачи — только fire, ждать их нечего
		b.cron = nil
	}
	b.curExpr = ""
}

// runCycle — один цикл под лок: две реплики Sender'а срабатывают в одну минуту.
func (b *ClientHostBackfill) runCycle(ctx context.Context) {
	if b.lock != nil {
		ok, err := b.lock.TryLock(ctx, backfillLockTTL)
		if err != nil {
			b.logger.Warn("client_host backfill: cycle lock check failed, skipping cycle", b.logger.Err(err))
			return
		}
		if !ok {
			b.logger.Debug("client_host backfill: cycle already taken by another replica")
			return
		}
	}
	if err := b.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
		b.logger.ErrorWithOp("client_host backfill cycle failed", err, "sender.clientHostBackfill")
	}
}

// RunOnce — один проход по всем управляемым таблицам без лока (экспортирован
// для integration-тестов). Ошибка одной таблицы — Warn и следующая таблица;
// ошибка возвращается, только если прохода не было вовсе.
func (b *ClientHostBackfill) RunOnce(ctx context.Context) error {
	tables, err := b.tables.ListClickHouseTables(ctx)
	if err != nil {
		return fmt.Errorf("list clickhouse tables: %w", err)
	}
	tables = b.validTables(tables)
	if b.managed != nil {
		tables = b.managed.FilterManagedTables(ctx, tables)
	}
	conn := b.ch.Conn()
	if conn == nil {
		return errors.New("clickhouse conn is nil")
	}
	start, filled := time.Now(), 0
	for _, t := range tables {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := b.backfillTable(ctx, conn, t)
		if err != nil {
			b.logger.Warn("client_host backfill: table failed", b.logger.Str("table", t), b.logger.Err(err))
			continue
		}
		filled += n
	}
	b.logger.Debug("client_host backfill: cycle done",
		b.logger.Int("tables", len(tables)), b.logger.Int("ips_scheduled", filled),
		b.logger.Int("duration_ms", int(time.Since(start).Milliseconds())))
	return nil
}

// validTables отбрасывает имена, которые нельзя подставить в SQL.
func (b *ClientHostBackfill) validTables(tables []string) []string {
	out := make([]string, 0, len(tables))
	for _, t := range tables {
		if !domain.IsValidCHTableName(t) {
			b.logger.Debug("client_host backfill: invalid table name skipped", b.logger.Str("table", t))
			continue
		}
		out = append(out, t)
	}
	return out
}

// backfillTable находит IP без имени, резолвит их и ставит одну мутацию на
// таблицу. Возвращает число IP, для которых мутация поставлена.
func (b *ClientHostBackfill) backfillTable(ctx context.Context, conn chdriver.Conn, table string) (int, error) {
	ips, err := b.emptyHostIPs(ctx, conn, table)
	if chpf.IsMissingColumnErr(err, "client_host") {
		// §51.9: деплой-окно — колонку §67 ещё не добавили. Не ошибка.
		b.logger.Debug("client_host backfill: no client_host column yet, table skipped", b.logger.Str("table", table))
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if len(ips) == 0 {
		b.logger.Debug("client_host backfill: nothing to fill", b.logger.Str("table", table))
		return 0, nil
	}
	hosts := b.resolveAll(ctx, ips)
	stmt, pairs, ok := buildClientHostMutation(table, hosts)
	if !ok {
		// §51.9: ни одного имени — мутацию не ставим, IP переспросят в следующем цикле.
		b.logger.Debug("client_host backfill: no names resolved",
			b.logger.Str("table", table), b.logger.Int("empty_ips", len(ips)))
		return 0, nil
	}
	qctx, cancel := context.WithTimeout(ctx, backfillQueryTimeout)
	defer cancel()
	if err := conn.Exec(qctx, stmt); err != nil {
		return 0, fmt.Errorf("schedule mutation: %w", err)
	}
	b.logger.Info("client_host backfill: mutation scheduled",
		b.logger.Str("table", table), b.logger.Int("ips", pairs), b.logger.Int("empty_ips", len(ips)))
	return pairs, nil
}

// emptyHostIPs — различные IP строк с пустым client_host (не больше backfillMaxIPs).
func (b *ClientHostBackfill) emptyHostIPs(ctx context.Context, conn chdriver.Conn, table string) ([]string, error) {
	qctx, cancel := context.WithTimeout(ctx, backfillQueryTimeout)
	defer cancel()
	// table проверен domain.IsValidCHTableName: имена таблиц CH не параметризуются.
	q := fmt.Sprintf("SELECT DISTINCT IP FROM %s WHERE client_host = '' AND IP != '' LIMIT %d", table, backfillMaxIPs)
	rows, err := conn.Query(qctx, q)
	if err != nil {
		return nil, fmt.Errorf("select empty client_host: %w", err)
	}
	defer rows.Close()
	var ips []string
	for rows.Next() {
		var ip string
		if err := rows.Scan(&ip); err != nil {
			return nil, fmt.Errorf("scan ip: %w", err)
		}
		ips = append(ips, ip)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate ips: %w", err)
	}
	return ips, nil
}

// resolveAll резолвит IP не больше backfillParallelism одновременно и
// возвращает только найденные имена.
func (b *ClientHostBackfill) resolveAll(ctx context.Context, ips []string) map[string]string {
	var mu sync.Mutex
	out := make(map[string]string, len(ips))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(backfillParallelism)
	for _, ip := range ips {
		if gctx.Err() != nil {
			break
		}
		g.Go(func() error {
			defer safego.Recover(b.logger, "sender.clientHostBackfill.resolve")
			if host := b.hosts.ResolveNow(gctx, ip); host != "" {
				mu.Lock()
				out[ip] = host
				mu.Unlock()
			}
			return nil
		})
	}
	// Горутины ошибок не возвращают: неразрешённый IP — норма, исход каждого
	// резолва уже в Debug-строке rdns.
	_ = g.Wait()
	return out
}
