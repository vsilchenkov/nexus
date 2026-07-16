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

// EnsureBodySizeColumns (§42-доп) идемпотентно добавляет колонки request_size
// и response_size (истинные размеры тел в байтах, до усечения лог-копии по
// max_body_size) в существующие per-node лог-таблицы. Та же логика и
// обоснование, что у EnsureNodeIDColumn: запускается на старте и Web, и
// Sender (порядок деплоя не гарантирован), без колонок INSERT (Sender) и
// SELECT (Web) по новой схеме упадут. Без AFTER — колонки встают в конец,
// как в RequiredLogColumns. Исторические строки получают DEFAULT 0 и
// добиваются BackfillBodySizes.
func EnsureBodySizeColumns(ctx context.Context, conn driver.Conn, tables []string, logger logging.Logger) {
	ensureLogColumn(ctx, conn, tables,
		"ALTER TABLE %s ADD COLUMN IF NOT EXISTS request_size Int64 DEFAULT 0, "+
			"ADD COLUMN IF NOT EXISTS response_size Int64 DEFAULT 0",
		"ensure body size columns failed", logger)
}

// BackfillBodySizes (§42-доп) разово заполняет request_size/response_size у
// исторических строк байтовой длиной СОХРАНЁННОЙ (возможно усечённой по
// max_body_size) копии тела — истинный размер до усечения для старых записей
// не восстановим, поэтому это нижняя граница. Guard-count не даёт планировать
// мутацию на каждом старте: после успешного backfill подходящих строк не
// остаётся (у новых записей размеры пишет Sender). Мутации ALTER…UPDATE
// асинхронные, завершения не ждём; ошибка по таблице — log+continue, старт
// не валим. Вызывается после EnsureBodySizeColumns на старте Web и Sender:
// повторный/конкурентный запуск безопасен — мутации идемпотентны (пишут те
// же значения), а после первого прохода guard находит 0 строк.
func BackfillBodySizes(ctx context.Context, conn driver.Conn, tables []string, logger logging.Logger) {
	if conn == nil {
		return
	}
	seen := make(map[string]bool, len(tables))
	for _, t := range tables {
		if t == "" || seen[t] || !ensureTableNamePattern.MatchString(t) {
			continue
		}
		seen[t] = true
		var pending uint64
		row := conn.QueryRow(ctx, fmt.Sprintf(
			"SELECT count() FROM %s WHERE (request_size = 0 AND request != '') "+
				"OR (response_size = 0 AND response != '')", t))
		if err := row.Scan(&pending); err != nil {
			logger.Warn("backfill body sizes: count failed",
				logger.Str("table", t), logger.Err(err))
			continue
		}
		if pending == 0 {
			logger.Debug("backfill body sizes: nothing to do", logger.Str("table", t))
			continue
		}
		// След миграции в логах сервиса: сколько строк уйдёт в мутации.
		logger.Info("backfill body sizes: scheduling mutations",
			logger.Str("table", t), logger.Int("rows", int(pending)))
		for _, stmtFmt := range []string{
			"ALTER TABLE %s UPDATE request_size = length(request) WHERE request_size = 0 AND request != ''",
			"ALTER TABLE %s UPDATE response_size = length(response) WHERE response_size = 0 AND response != ''",
		} {
			if err := conn.Exec(ctx, fmt.Sprintf(stmtFmt, t)); err != nil {
				logger.Warn("backfill body sizes: mutation failed",
					logger.Str("table", t), logger.Err(err))
			}
		}
	}
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
