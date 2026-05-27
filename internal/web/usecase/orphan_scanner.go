// Package usecase: orphan_scanner — поиск ClickHouse-таблиц, у которых
// нет соответствующего узла в Postgres (§7.10 / Phase 6.7).
package usecase

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"nexus/internal/platform/config"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// OrphanScannerConnProvider — узкий read-only доступ к ClickHouse-соединению.
// Определён на стороне consumer'а (CLAUDE.md §3), чтобы OrphanScanner
// не зависел от конкретного владельца conn'а (clickhouse.Manager автоматически
// реализует его структурным совпадением).
type OrphanScannerConnProvider interface {
	Conn() chdriver.Conn
}

// OrphanTable — описывает «бесхозную» таблицу в ClickHouse.
type OrphanTable struct {
	Database   string    `json:"database"`
	Table      string    `json:"table"`
	FullName   string    `json:"full_name"` // db.table
	Engine     string    `json:"engine"`
	TotalRows  uint64    `json:"total_rows"`
	TotalBytes uint64    `json:"total_bytes"`
	CreatedAt  time.Time `json:"created_at"`
}

// OrphanScanner — usecase обнаружения и удаления orphan-таблиц (Phase 6.7).
//
// Алгоритм Scan():
//  1. SELECT clickhouse_table FROM nodes  → known-set (lowercased).
//  2. SELECT name, engine, total_rows, total_bytes, metadata_modification_time
//     FROM system.tables WHERE database = ? AND engine LIKE '%MergeTree%'
//     AND name NOT LIKE '.inner%' AND name NOT LIKE '.tmp%'.
//  3. Те, что не входят в known-set, отдаются как orphan'ы.
//
// Drop(): DROP TABLE IF EXISTS db.table; имя строго валидируется
// против isSafeTableName (защита от SQL-инъекции), действие пишется
// в audit log (action="ch_table.drop", target_type="clickhouse_table").
type OrphanScanner struct {
	ch            OrphanScannerConnProvider
	nodeRepo      port.NodeRepo
	chCfg         *config.ClickHouseSection
	audit         *AuditUsecase
	defaultTeamID string
	logger        logging.Logger
}

func NewOrphanScanner(
	ch OrphanScannerConnProvider,
	nodeRepo port.NodeRepo,
	chCfg *config.ClickHouseSection,
	audit *AuditUsecase,
	defaultTeamID string,
	logger logging.Logger,
) *OrphanScanner {
	return &OrphanScanner{
		ch:            ch,
		nodeRepo:      nodeRepo,
		chCfg:         chCfg,
		audit:         audit,
		defaultTeamID: defaultTeamID,
		logger:        logger,
	}
}

// Scan возвращает список таблиц, которых нет в Postgres.nodes.
// Сканируется только база, указанная в текущей секции ClickHouse (chCfg.Database) —
// чужие БД не трогаем.
func (s *OrphanScanner) Scan(ctx context.Context) ([]*OrphanTable, error) {
	conn := s.ch.Conn()
	if conn == nil {
		return nil, errors.New("clickhouse conn is nil")
	}
	if s.chCfg == nil || s.chCfg.Database == "" {
		return nil, errors.New("clickhouse database is empty in config")
	}

	known, err := s.knownTables(ctx)
	if err != nil {
		return nil, fmt.Errorf("known tables: %w", err)
	}

	queryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	rows, err := conn.Query(queryCtx, `
SELECT name, engine, total_rows, total_bytes, metadata_modification_time
FROM system.tables
WHERE database = ?
  AND engine LIKE '%MergeTree%'
  AND name NOT LIKE '.inner%'
  AND name NOT LIKE '.tmp%'
ORDER BY name`, s.chCfg.Database)
	if err != nil {
		return nil, fmt.Errorf("query system.tables: %w", err)
	}
	defer rows.Close()

	var out []*OrphanTable
	for rows.Next() {
		var (
			name, engine string
			totalRows    uint64
			totalBytes   uint64
			modAt        time.Time
		)
		if err := rows.Scan(&name, &engine, &totalRows, &totalBytes, &modAt); err != nil {
			return nil, fmt.Errorf("scan system.tables row: %w", err)
		}
		full := s.chCfg.Database + "." + name
		if _, ok := known[strings.ToLower(full)]; ok {
			continue
		}
		out = append(out, &OrphanTable{
			Database:   s.chCfg.Database,
			Table:      name,
			FullName:   full,
			Engine:     engine,
			TotalRows:  totalRows,
			TotalBytes: totalBytes,
			CreatedAt:  modAt,
		})
	}
	return out, nil
}

// Drop — DROP TABLE IF EXISTS для одной orphan-таблицы. Имя должно быть
// безопасным (isSafeTableNameLocal) и принадлежать текущей CH-базе из cfg.
// Не позволяем удалить таблицу узла, который есть в Postgres
// (защита от двойного клика и race-condition).
func (s *OrphanScanner) Drop(ctx context.Context, actor Actor, fullName string) error {
	if !isSafeTableNameLocal(fullName) {
		return fmt.Errorf("invalid table name: %q", fullName)
	}
	parts := strings.SplitN(fullName, ".", 2)
	if len(parts) != 2 {
		return fmt.Errorf("expected db.table, got %q", fullName)
	}
	db, tbl := parts[0], parts[1]
	if s.chCfg == nil || !strings.EqualFold(db, s.chCfg.Database) {
		return fmt.Errorf("drop allowed only in configured database %q, got %q", s.chCfg.Database, db)
	}

	// Защита от race: повторно проверяем, что эта таблица всё ещё orphan.
	known, err := s.knownTables(ctx)
	if err != nil {
		return fmt.Errorf("known tables: %w", err)
	}
	if _, used := known[strings.ToLower(fullName)]; used {
		return fmt.Errorf("table %q is in use by a node — refuse to drop", fullName)
	}

	conn := s.ch.Conn()
	if conn == nil {
		return errors.New("clickhouse conn is nil")
	}
	dropCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	stmt := fmt.Sprintf("DROP TABLE IF EXISTS `%s`.`%s`", db, tbl)
	if err := conn.Exec(dropCtx, stmt); err != nil {
		return fmt.Errorf("drop table %s: %w", fullName, err)
	}

	s.audit.Log(ctx, actor, "ch_table.drop", "clickhouse_table", fullName, map[string]any{
		"database": db,
		"table":    tbl,
	})
	s.logger.Info("orphan clickhouse table dropped",
		s.logger.Str("table", fullName),
		s.logger.Str("user", actor.UserLogin))
	return nil
}

// knownTables собирает множество lowercased "db.table" из всех узлов
// в Postgres с непустым clickhouse_table.
func (s *OrphanScanner) knownTables(ctx context.Context) (map[string]struct{}, error) {
	const pageSize = 500
	known := make(map[string]struct{}, 64)
	offset := 0
	for {
		nodes, err := s.nodeRepo.List(ctx, port.ListNodesFilter{
			TeamID: s.defaultTeamID,
			Limit:  pageSize,
			Offset: offset,
		})
		if err != nil {
			return nil, err
		}
		for _, n := range nodes {
			if n.ClickHouseTable == "" {
				continue
			}
			known[strings.ToLower(n.ClickHouseTable)] = struct{}{}
		}
		if len(nodes) < pageSize {
			break
		}
		offset += pageSize
	}
	return known, nil
}

// isSafeTableNameLocal — db.table из A-Za-z0-9_; обе части обязательны.
// Дублирует chreader.isSafeTableName, чтобы usecase не зависел от adapter.
func isSafeTableNameLocal(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	dot := -1
	for i, c := range name {
		switch {
		case c == '.':
			if dot >= 0 {
				return false
			}
			dot = i
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '_':
		default:
			return false
		}
	}
	return dot > 0 && dot < len(name)-1
}
