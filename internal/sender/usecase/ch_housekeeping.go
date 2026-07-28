package usecase

import (
	"context"
	"fmt"
	"strings"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
)

// NodeLister — минимальный интерфейс для housekeeping: получить узлы
// со включённой ClickHouse-таблицей и их retention в днях.
type NodeLister interface {
	ListForHousekeeping(ctx context.Context) ([]*domain.Node, error)
}

// ConnProvider — узкий read-only доступ к ClickHouse-соединению.
// Определён на стороне consumer'а (CLAUDE.md §3): CHHousekeeping не
// должен зависеть от конкретного владельца conn'а — clickhouse.Manager
// автоматически реализует этот интерфейс структурным совпадением.
type ConnProvider interface {
	Conn() chdriver.Conn
}

// TableOwnership — гейт владения таблицей (§70.4). Определён на стороне
// консьюмера; реализуется clickhouse.Guard. nil = поведение до §70.
//
// DROP PARTITION удаляет партицию ЦЕЛИКОМ, без разбора по node_id, поэтому
// здесь используется строгая проверка: чужая таблица, таблица без маркера и
// неопределённый ответ ClickHouse одинаково означают «не трогаем».
type TableOwnership interface {
	OwnsTable(ctx context.Context, table string) (bool, error)
}

// CHHousekeeping — раз в сутки удаляет старые ClickHouse-партиции по
// retention каждого узла (§4.3 ТЗ).
//
// Партиционирование таблиц логов: PARTITION BY toYYYYMM(date_create), т.е.
// имена партиций — "YYYYMM". Дроп выполняется через
//
//	ALTER TABLE <db.table> DROP PARTITION '<YYYYMM>'
//
// для всех партиций со столбца system.parts, чья дата старше cutoff.
type CHHousekeeping struct {
	ch        ConnProvider
	nodes     NodeLister
	period    time.Duration
	ownership TableOwnership
	logger    logging.Logger
}

func NewCHHousekeeping(ch ConnProvider, nodes NodeLister, logger logging.Logger) *CHHousekeeping {
	return &CHHousekeeping{ch: ch, nodes: nodes, period: 24 * time.Hour, logger: logger}
}

// WithOwnership подключает гейт владения (§70.4) и возвращает тот же экземпляр
// для цепочки в wiring. nil сохраняет прежнее поведение.
func (h *CHHousekeeping) WithOwnership(o TableOwnership) *CHHousekeeping {
	h.ownership = o
	return h
}

// mayDrop — разрешено ли удалять партиции этой таблицы. Ошибку проверки
// трактуем как запрет: пропущенная уборка стоит места на диске, ошибочная —
// суток чужих логов.
func (h *CHHousekeeping) mayDrop(ctx context.Context, table string) bool {
	if h.ownership == nil {
		return true
	}
	owns, err := h.ownership.OwnsTable(ctx, table)
	if err != nil {
		h.logger.Warn("housekeeping: ownership check failed, skipping table",
			h.logger.Str("table", table), h.logger.Err(err))
		return false
	}
	if !owns {
		h.logger.Warn("housekeeping: table belongs to another nexus instance, skipped",
			h.logger.Str("table", table))
	}
	return owns
}

// Run — блокирующий цикл. Первый прогон делается сразу, далее раз в period.
// Завершается при отмене ctx.
func (h *CHHousekeeping) Run(ctx context.Context) {
	tick := time.NewTicker(h.period)
	defer tick.Stop()

	if err := h.runOnce(ctx); err != nil {
		h.logger.ErrorWithOp("ch housekeeping iteration failed", err, "ch.housekeeping")
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if err := h.runOnce(ctx); err != nil {
				h.logger.ErrorWithOp("ch housekeeping iteration failed", err, "ch.housekeeping")
			}
		}
	}
}

func (h *CHHousekeeping) runOnce(ctx context.Context) error {
	nodes, err := h.nodes.ListForHousekeeping(ctx)
	if err != nil {
		return fmt.Errorf("list nodes: %w", err)
	}
	for _, n := range nodes {
		if n.ClickHouseTable == "" || n.ClickHouseRetentionDays <= 0 {
			continue
		}
		// §70.4: DROP PARTITION сносит партицию целиком — на чужой таблице это
		// удалило бы логи соседней ноды по НАШЕМУ retention.
		if !h.mayDrop(ctx, n.ClickHouseTable) {
			continue
		}
		dropped, err := h.dropPartitionsOlderThan(ctx, n.ClickHouseTable, int(n.ClickHouseRetentionDays))
		if err != nil {
			h.logger.Warn("drop partitions failed",
				h.logger.Str("table", n.ClickHouseTable),
				h.logger.Err(err))
			continue
		}
		if dropped > 0 {
			h.logger.Info("partitions dropped",
				h.logger.Str("table", n.ClickHouseTable),
				h.logger.Int("count", dropped),
				h.logger.Int("retention_days", int(n.ClickHouseRetentionDays)))
		}
	}
	return nil
}

// dropPartitionsOlderThan находит все активные партиции таблицы, чей
// max_date < cutoff, и удаляет их. Возвращает число дропнутых партиций.
func (h *CHHousekeeping) dropPartitionsOlderThan(ctx context.Context, table string, retentionDays int) (int, error) {
	db, tbl, ok := splitDBTable(table)
	if !ok {
		return 0, fmt.Errorf("invalid table name %q", table)
	}
	cutoff := time.Now().AddDate(0, 0, -retentionDays)

	conn := h.ch.Conn()
	if conn == nil {
		return 0, fmt.Errorf("clickhouse conn is nil")
	}
	rows, err := conn.Query(ctx, `
SELECT DISTINCT partition
FROM system.parts
WHERE database = ? AND table = ? AND active = 1 AND max_date < ?
`, db, tbl, cutoff)
	if err != nil {
		return 0, fmt.Errorf("query system.parts: %w", err)
	}
	defer rows.Close()

	var partitions []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return 0, fmt.Errorf("scan partition: %w", err)
		}
		partitions = append(partitions, p)
	}

	for _, p := range partitions {
		// CH-сервер не принимает параметризованное имя партиции, поэтому
		// собираем строку вручную, но строго после фильтра isSafePartition.
		if !isSafePartition(p) {
			h.logger.Warn("skip unsafe partition name",
				h.logger.Str("table", table),
				h.logger.Str("partition", p))
			continue
		}
		stmt := fmt.Sprintf("ALTER TABLE %s DROP PARTITION '%s'", table, p)
		if err := conn.Exec(ctx, stmt); err != nil {
			return 0, fmt.Errorf("drop partition %s/%s: %w", table, p, err)
		}
	}
	return len(partitions), nil
}

func splitDBTable(name string) (db, table string, ok bool) {
	i := strings.IndexByte(name, '.')
	if i <= 0 || i >= len(name)-1 {
		return "", "", false
	}
	return name[:i], name[i+1:], true
}

// isSafePartition — partition по toYYYYMM это 6 цифр; на всякий случай
// допускаем и буквы/цифры/'_'/'-'.
func isSafePartition(p string) bool {
	if p == "" || len(p) > 64 {
		return false
	}
	for _, c := range p {
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c == '_' || c == '-':
		default:
			return false
		}
	}
	return true
}
