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
// Алгоритм Scan() (multi-tenancy v2, Phase 10.D.2):
//  1. SELECT * FROM teams → allow-list БД (ch_database).
//  2. SELECT clickhouse_table FROM nodes (всех команд) → known-set.
//  3. Для каждой БД allow-list'а:
//     SELECT name, engine, total_rows, total_bytes,
//            metadata_modification_time
//     FROM system.tables
//     WHERE database = ? AND engine LIKE '%MergeTree%'
//       AND name NOT LIKE '.inner%' AND name NOT LIKE '.tmp%'
//  4. Те, что не входят в known-set, — orphan'ы.
//
// Drop(): DROP TABLE IF EXISTS db.table; имя строго валидируется
// против isSafeTableName (защита от SQL-инъекции), db обязан быть в
// allow-list teams.ch_database (защита от удаления чужих/системных БД),
// действие пишется в audit log
// (action="ch_table.drop", target_type="clickhouse_table").
type OrphanScanner struct {
	ch       OrphanScannerConnProvider
	nodeRepo port.NodeRepo
	teamRepo port.TeamRepo
	chCfg    *config.ClickHouseSection
	audit    *AuditUsecase
	logger   logging.Logger
}

func NewOrphanScanner(
	ch OrphanScannerConnProvider,
	nodeRepo port.NodeRepo,
	teamRepo port.TeamRepo,
	chCfg *config.ClickHouseSection,
	audit *AuditUsecase,
	logger logging.Logger,
) *OrphanScanner {
	return &OrphanScanner{
		ch:       ch,
		nodeRepo: nodeRepo,
		teamRepo: teamRepo,
		chCfg:    chCfg,
		audit:    audit,
		logger:   logger,
	}
}

// Scan возвращает список orphan-таблиц по всем БД allow-list'а
// (teams.ch_database). Чужие БД (system, default и т.п.) не сканируются —
// allow-list строго ограничен tenant-БД.
func (s *OrphanScanner) Scan(ctx context.Context) ([]*OrphanTable, error) {
	conn := s.ch.Conn()
	if conn == nil {
		return nil, errors.New("clickhouse conn is nil")
	}

	allowedDBs, err := s.allowedDatabases(ctx)
	if err != nil {
		return nil, fmt.Errorf("allowed databases: %w", err)
	}
	if len(allowedDBs) == 0 {
		return nil, nil
	}

	known, err := s.knownTables(ctx)
	if err != nil {
		return nil, fmt.Errorf("known tables: %w", err)
	}

	var out []*OrphanTable
	for _, db := range allowedDBs {
		dbOrphans, err := s.scanDatabase(ctx, conn, db, known)
		if err != nil {
			return nil, err
		}
		out = append(out, dbOrphans...)
	}
	return out, nil
}

func (s *OrphanScanner) scanDatabase(
	ctx context.Context,
	conn chdriver.Conn,
	db string,
	known map[string]struct{},
) ([]*OrphanTable, error) {
	queryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	rows, err := conn.Query(queryCtx, `
SELECT name, engine, total_rows, total_bytes, metadata_modification_time
FROM system.tables
WHERE database = ?
  AND engine LIKE '%MergeTree%'
  AND name NOT LIKE '.inner%'
  AND name NOT LIKE '.tmp%'
ORDER BY name`, db)
	if err != nil {
		return nil, fmt.Errorf("query system.tables for %s: %w", db, err)
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
		full := db + "." + name
		if _, ok := known[strings.ToLower(full)]; ok {
			continue
		}
		out = append(out, &OrphanTable{
			Database:   db,
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
// безопасным (isSafeTableNameLocal), db обязан быть в allow-list
// teams.ch_database (защита от удаления чужих/системных БД). Не позволяем
// удалить таблицу узла, который есть в Postgres (защита от двойного
// клика и race-condition).
func (s *OrphanScanner) Drop(ctx context.Context, actor Actor, fullName string) error {
	if !isSafeTableNameLocal(fullName) {
		return fmt.Errorf("invalid table name: %q", fullName)
	}
	parts := strings.SplitN(fullName, ".", 2)
	if len(parts) != 2 {
		return fmt.Errorf("expected db.table, got %q", fullName)
	}
	db, tbl := parts[0], parts[1]

	allowedDBs, err := s.allowedDatabases(ctx)
	if err != nil {
		return fmt.Errorf("allowed databases: %w", err)
	}
	allowed := false
	for _, d := range allowedDBs {
		if strings.EqualFold(d, db) {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Errorf("drop allowed only in tenant databases (teams.ch_database), got %q", db)
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

// knownTables собирает множество lowercased "db.table" из узлов всех
// команд (multi-tenancy v2): orphan-таблица одной команды не должна
// показываться как orphan, если её использует узел другой команды.
func (s *OrphanScanner) knownTables(ctx context.Context) (map[string]struct{}, error) {
	teams, err := s.teamRepo.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list teams: %w", err)
	}

	const pageSize = 500
	known := make(map[string]struct{}, 64)
	for _, team := range teams {
		offset := 0
		for {
			nodes, err := s.nodeRepo.List(ctx, port.ListNodesFilter{
				TeamID: team.ID,
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
	}
	return known, nil
}

// allowedDatabases — список БД, в которых разрешено сканировать orphan'ы
// и выполнять Drop. Источник: teams.ch_database.
func (s *OrphanScanner) allowedDatabases(ctx context.Context) ([]string, error) {
	teams, err := s.teamRepo.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list teams: %w", err)
	}
	out := make([]string, 0, len(teams))
	for _, t := range teams {
		if t.CHDatabase != "" {
			out = append(out, t.CHDatabase)
		}
	}
	return out, nil
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
