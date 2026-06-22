package clickhouse

import (
	"context"
	"fmt"
	"regexp"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"nexus/internal/platform/logging"
)

// ensureTableNamePattern — "<db>.<table>" из [A-Za-z0-9_]. Имя идёт в DDL
// напрямую (CH не принимает параметризованные имена), поэтому валидируется.
var ensureTableNamePattern = regexp.MustCompile(`^[A-Za-z0-9_]+\.[A-Za-z0-9_]+$`)

// EnsureNodeIDColumn (§37) идемпотентно добавляет колонку node_id в существующие
// per-node лог-таблицы: `ALTER TABLE <t> ADD COLUMN IF NOT EXISTS node_id String
// DEFAULT ”`. Дешёвая метаданные-операция, concurrent-safe (IF NOT EXISTS) —
// запускается на старте и Web, и Sender, т.к. порядок деплоя не гарантирован, а
// без колонки INSERT (Sender) и SELECT (Web) по новой схеме упадут. Новые таблицы
// получают колонку из шаблона (RequiredLogColumns). Дубли таблиц (общие у
// нескольких узлов) альтерятся один раз; ошибка по одной таблице — log+continue,
// старт не валим. Старые записи остаются с node_id=” (legacy, истекают по TTL).
func EnsureNodeIDColumn(ctx context.Context, conn driver.Conn, tables []string, logger logging.Logger) {
	ensureLogColumn(ctx, conn, tables,
		"ALTER TABLE %s ADD COLUMN IF NOT EXISTS node_id String DEFAULT ''",
		"ensure node_id column failed", logger)
}

// EnsureHTTPMethodColumn (§39) идемпотентно добавляет колонку http_method
// (HTTP-глагол) в существующие лог-таблицы: `ALTER TABLE <t> ADD COLUMN IF NOT
// EXISTS http_method String DEFAULT ” AFTER type`. Та же логика и обоснование,
// что у EnsureNodeIDColumn: запускается на старте Web и Sender (порядок деплоя
// не гарантирован), без колонки INSERT/SELECT по новой схеме упадут. AFTER type
// держит физический порядок колонок одинаковым с новыми таблицами
// (RenderCreateTable из RequiredLogColumns). Колонка `method` уже существует —
// меняется только что в неё пишут (§39: подпуть запроса вместо глагола).
func EnsureHTTPMethodColumn(ctx context.Context, conn driver.Conn, tables []string, logger logging.Logger) {
	ensureLogColumn(ctx, conn, tables,
		"ALTER TABLE %s ADD COLUMN IF NOT EXISTS http_method String DEFAULT '' AFTER type",
		"ensure http_method column failed", logger)
}

// ensureLogColumn — общий идемпотентный ALTER для добавления колонки в набор
// лог-таблиц. stmtFmt принимает имя таблицы единственным %s. Дубли таблиц
// (общие у нескольких узлов) альтерятся один раз; ошибка по одной таблице —
// log+continue, старт не валим.
func ensureLogColumn(ctx context.Context, conn driver.Conn, tables []string, stmtFmt, warnMsg string, logger logging.Logger) {
	if conn == nil {
		return
	}
	seen := make(map[string]bool, len(tables))
	for _, t := range tables {
		if t == "" || seen[t] || !ensureTableNamePattern.MatchString(t) {
			continue
		}
		seen[t] = true
		if err := conn.Exec(ctx, fmt.Sprintf(stmtFmt, t)); err != nil {
			logger.Warn(warnMsg, logger.Str("table", t), logger.Err(err))
		}
	}
}
