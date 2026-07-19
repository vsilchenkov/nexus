package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// SchemaInspectorCH читает схему существующей таблицы логов из system.* и
// исполняет ALTER'ы (§56). Как и TeamProvisionerCH, ходит через ConnProvider
// (hot-reload swap'ает conn) и валидирует имя таблицы жёстким regex —
// параметризации имён в ClickHouse нет.
type SchemaInspectorCH struct {
	conn   ConnProvider
	logger logging.Logger
}

var _ port.CHSchemaInspector = (*SchemaInspectorCH)(nil)

func NewSchemaInspector(conn ConnProvider, logger logging.Logger) *SchemaInspectorCH {
	return &SchemaInspectorCH{conn: conn, logger: logger}
}

// ttlDaysRe вытаскивает срок native-TTL из SHOW CREATE TABLE. Наш ALTER/CREATE
// пишет `TTL date_create + INTERVAL <n> DAY DELETE`, но ClickHouse НОРМАЛИЗУЕТ
// эту запись в `toIntervalDay(<n>)` при выводе SHOW CREATE — поэтому распознаём
// обе формы. Иначе применённый MODIFY TTL интроспекция «не видит» (HasTTL=false),
// и планировщик §56 предлагает тот же ALTER по кругу (retention «не применяется»).
var ttlDaysRe = regexp.MustCompile(`(?i)INTERVAL\s+(\d+)\s+DAY|toIntervalDay\(\s*(\d+)\s*\)`)

// ReadTableSchema собирает снимок схемы таблицы из системных таблиц ClickHouse.
func (s *SchemaInspectorCH) ReadTableSchema(ctx context.Context, table string) (domain.CurrentTableSchema, bool, error) {
	out := domain.CurrentTableSchema{Codecs: map[string]string{}}
	if !fullTableNamePattern.MatchString(table) {
		return out, false, errInvalidTableName
	}
	conn := s.conn.Conn()
	if conn == nil {
		return out, false, errors.New("clickhouse conn is nil")
	}
	db, tbl, _ := splitDBDotTable(table)

	// Существование: считаем строки, чтобы не полагаться на семантику
	// QueryRow-«нет строк» конкретного драйвера.
	var cnt uint64
	if err := conn.QueryRow(ctx,
		"SELECT count() FROM system.tables WHERE database = ? AND name = ?", db, tbl).Scan(&cnt); err != nil {
		return out, false, fmt.Errorf("check table %s: %w", table, err)
	}
	if cnt == 0 {
		return out, false, nil
	}

	// Движок / ключ сортировки / ключ партиционирования.
	var engine, sortingKey, partitionKey string
	if err := conn.QueryRow(ctx,
		"SELECT engine, sorting_key, partition_key FROM system.tables WHERE database = ? AND name = ?",
		db, tbl).Scan(&engine, &sortingKey, &partitionKey); err != nil {
		return out, false, fmt.Errorf("read system.tables %s: %w", table, err)
	}
	out.Engine = engine
	out.PartitionBy = partitionKey
	out.OrderBy = splitCSV(sortingKey)

	// CODEC по колонкам (compression_codec пуст = дефолт).
	if err := s.readCodecs(ctx, db, tbl, out.Codecs); err != nil {
		return out, false, err
	}
	// Data-skipping индексы.
	idx, err := s.readIndexes(ctx, db, tbl)
	if err != nil {
		return out, false, err
	}
	out.Indexes = idx

	// native-TTL — из SHOW CREATE (в system.tables распарсенного дня нет).
	var createStmt string
	if err := conn.QueryRow(ctx, "SHOW CREATE TABLE "+table).Scan(&createStmt); err != nil {
		// Не фатально: без TTL просто не предложим MODIFY/REMOVE TTL.
		s.logger.Debug("ch schema: SHOW CREATE failed, TTL unknown",
			s.logger.Str("table", table), s.logger.Err(err))
		return out, true, nil
	}
	if days, ok := ttlDaysFromCreate(createStmt); ok {
		out.HasTTL = true
		out.TTLDays = days
	}
	return out, true, nil
}

// ttlDaysFromCreate достаёт срок native-TTL (в днях) из текста SHOW CREATE TABLE,
// принимая обе формы записи интервала — `INTERVAL <n> DAY` и нормализованную
// ClickHouse `toIntervalDay(<n>)`. ok=false, если native-TTL по дням в DDL нет.
func ttlDaysFromCreate(createStmt string) (int32, bool) {
	m := ttlDaysRe.FindStringSubmatch(createStmt)
	if m == nil {
		return 0, false
	}
	// Группа 1 — форма INTERVAL, группа 2 — toIntervalDay; заполнена одна из них.
	day := m[1]
	if day == "" {
		day = m[2]
	}
	return atoi32(day), true
}

func (s *SchemaInspectorCH) readCodecs(ctx context.Context, db, tbl string, dst map[string]string) error {
	rows, err := s.conn.Conn().Query(ctx,
		"SELECT name, compression_codec FROM system.columns WHERE database = ? AND table = ?", db, tbl)
	if err != nil {
		return fmt.Errorf("read system.columns %s.%s: %w", db, tbl, err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, codec string
		if err := rows.Scan(&name, &codec); err != nil {
			return fmt.Errorf("scan column codec: %w", err)
		}
		if codec != "" {
			dst[name] = codec
		}
	}
	return rows.Err()
}

func (s *SchemaInspectorCH) readIndexes(ctx context.Context, db, tbl string) ([]domain.CurrentIndex, error) {
	rows, err := s.conn.Conn().Query(ctx,
		"SELECT name, type_full, expr, granularity FROM system.data_skipping_indices WHERE database = ? AND table = ?",
		db, tbl)
	if err != nil {
		return nil, fmt.Errorf("read system.data_skipping_indices %s.%s: %w", db, tbl, err)
	}
	defer rows.Close()
	var out []domain.CurrentIndex
	for rows.Next() {
		var name, typeFull, expr string
		var gran uint64
		if err := rows.Scan(&name, &typeFull, &expr, &gran); err != nil {
			return nil, fmt.Errorf("scan index: %w", err)
		}
		out = append(out, domain.CurrentIndex{
			Name: name, Type: typeFull, Expr: expr, Granularity: int32(gran),
		})
	}
	return out, rows.Err()
}

// ApplyAlter исполняет ALTER-операторы по порядку. table валидируется, сами
// операторы построены доменным планировщиком из валидированных значений.
func (s *SchemaInspectorCH) ApplyAlter(ctx context.Context, table string, statements []string) error {
	if !fullTableNamePattern.MatchString(table) {
		return errInvalidTableName
	}
	conn := s.conn.Conn()
	if conn == nil {
		return errors.New("clickhouse conn is nil")
	}
	for _, stmt := range statements {
		if err := conn.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("apply alter %q: %w", stmt, err)
		}
		s.logger.Info("ch schema: alter applied", s.logger.Str("table", table), s.logger.Str("stmt", stmt))
	}
	return nil
}

// splitCSV режет "a, b, c" в ["a","b","c"] с тримом; пустая строка → nil.
func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func atoi32(s string) int32 {
	var n int32
	for _, c := range s {
		if c < '0' || c > '9' {
			return n
		}
		n = n*10 + (c - '0')
	}
	return n
}
