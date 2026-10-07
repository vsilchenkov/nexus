package clickhouse

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"nexus/internal/web/usecase/port"
)

var _ port.LogExportReader = (*LogReaderCH)(nil)

const (
	// exportKeysMinFetch — нижняя граница строк на раунд чтения ключей: при
	// малом остатке раунд всё равно берёт порцию, чтобы плотные дубли (много
	// прогонов одной записи) не превращали добор в цепочку крошечных запросов.
	exportKeysMinFetch = 1024
	// exportKeysMaxFetch — верхняя граница строк на раунд: ограничивает пару
	// «ID × время», которую ClickHouse держит в куче top-N, и память Web.
	exportKeysMaxFetch = 200_000
)

// exportKeyCols — ключ записи: ID, время прогона в миллисекундах и date_create
// прогона текстом. Алиасы не совпадают с именами колонок — не затеняют их в WHERE.
const exportKeyCols = `ID, toInt64(toUnixTimestamp64Milli(toDateTime64(date_request, 3))) AS ts,
	toString(date_create) AS key_day`

// ExportStats — оценка выгрузки (§115.5). Единица Records — запись, как у Count;
// Rows — прогоны. Размеры — по request_size/response_size (истинные байты тел,
// §42.10), урезанным до bodyCapBytes: тела с диска не читаются. Таймаут —
// countTimeout, как у счётчика «из M».
func (r *LogReaderCH) ExportStats(ctx context.Context, q port.LogQuery, bodyCapBytes int64) (port.LogExportStats, error) {
	if !isSafeTableName(q.Table) {
		return port.LogExportStats{}, fmt.Errorf("invalid table name: %q", q.Table)
	}
	q.BeforeID, q.Unresolved = "", false
	cctx, cancel := context.WithTimeout(ctx, countTimeout)
	defer cancel()

	conds, args := r.searchConds(cctx, q)
	conn, err := r.liveConn()
	if err != nil {
		return port.LogExportStats{}, err
	}
	// Аргументы проекции — перед аргументами WHERE (подстановки нумеруются по
	// порядку появления в тексте запроса).
	sql := fmt.Sprintf(`SELECT uniqExact(ID), count(),
		sum(least(request_size, ?)), sum(least(response_size, ?)) FROM %s%s`, q.Table, whereClause(conds))
	var st port.LogExportStats
	start := time.Now()
	if err := conn.QueryRow(cctx, sql, append([]any{bodyCapBytes, bodyCapBytes}, args...)...).
		Scan(&st.Records, &st.Rows, &st.ReqBytes, &st.RespBytes); err != nil {
		return port.LogExportStats{}, classifyCHErr("clickhouse export stats", err)
	}
	r.logger.Debug("logs export: stats",
		r.logger.Str("table", q.Table),
		r.logger.Any("records", st.Records),
		r.logger.Any("rows", st.Rows),
		r.logger.Any("duration_ms", time.Since(start).Milliseconds()))
	return st, nil
}

// ExportKeys — ключи записей для выгрузки в порядке списка журнала (§115.7).
//
// Читаются СТРОКИ top-N без `LIMIT 1 BY ID`: с ним ClickHouse не применяет
// top-N-сортировку и сортирует все пары «ID × время» под фильтром, а это на
// большой таблице без периода — сотни мегабайт. Дубли ID (несколько прогонов)
// отбрасываются здесь по первому вхождению — это новейший подходящий прогон,
// то есть та же строка, что выбирает `LIMIT 1 BY ID` списка. Если после
// отбрасывания ключей не хватило, чтение продолжается keyset-курсором
// (date_request, ID) — тем же, что у пагинации журнала.
func (r *LogReaderCH) ExportKeys(ctx context.Context, q port.LogQuery, limit int) ([]port.LogExportKey, error) {
	if !isSafeTableName(q.Table) {
		return nil, fmt.Errorf("invalid table name: %q", q.Table)
	}
	if limit <= 0 {
		return nil, nil
	}
	q.BeforeID, q.Unresolved, q.Limit = "", false, 0
	keys := make([]port.LogExportKey, 0, min(limit, exportKeysMaxFetch))
	seen := make(map[string]struct{}, min(limit, exportKeysMaxFetch))
	for round := 1; ; round++ {
		need := limit - len(keys)
		fetch := min(max(need+need/8, exportKeysMinFetch), exportKeysMaxFetch)
		page, err := r.exportKeyPage(ctx, q, fetch)
		if err != nil {
			return nil, err
		}
		for _, k := range page {
			if _, dup := seen[k.ID]; dup {
				continue
			}
			seen[k.ID] = struct{}{}
			keys = append(keys, k)
			if len(keys) == limit {
				break
			}
		}
		if len(keys) == limit || len(page) < fetch {
			r.logger.Debug("logs export: keys collected",
				r.logger.Str("table", q.Table),
				r.logger.Int("keys", len(keys)),
				r.logger.Int("limit", limit),
				r.logger.Int("rounds", round))
			return keys, nil
		}
		// Курсор — последняя ПРОЧИТАННАЯ строка, а не последний взятый ключ:
		// хвост страницы мог целиком состоять из дублей.
		last := page[len(page)-1]
		if last.TsMs <= 0 {
			// Нулевое время курсором не выражается (searchConds считает UntilMs=0
			// «без границы») — раунд повторил бы ту же страницу.
			r.logger.Debug("logs export: keys stopped at zero date_request",
				r.logger.Str("table", q.Table), r.logger.Int("keys", len(keys)))
			return keys, nil
		}
		q.UntilMs, q.BeforeID = last.TsMs, last.ID
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
}

// exportKeyPage — одна страница строк-ключей под фильтрами q (с keyset-курсором,
// если он задан в q.UntilMs/q.BeforeID), новые сверху.
func (r *LogReaderCH) exportKeyPage(ctx context.Context, q port.LogQuery, fetch int) ([]port.LogExportKey, error) {
	conds, args := r.searchConds(ctx, q)
	conn, err := r.liveConn()
	if err != nil {
		return nil, err
	}
	start := time.Now()
	rows, err := conn.Query(ctx, fmt.Sprintf(
		`SELECT %s FROM %s%s ORDER BY date_request DESC, ID DESC LIMIT ?`,
		exportKeyCols, q.Table, whereClause(conds)), append(args, fetch)...)
	if err != nil {
		return nil, classifyCHErr("clickhouse export keys", err)
	}
	defer rows.Close()

	page := make([]port.LogExportKey, 0, fetch)
	for rows.Next() {
		var k port.LogExportKey
		if err := rows.Scan(&k.ID, &k.TsMs, &k.DateCreate); err != nil {
			return nil, fmt.Errorf("clickhouse export keys: scan: %w", err)
		}
		page = append(page, k)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyCHErr("clickhouse export keys", err)
	}
	r.logger.Debug("logs export: key page",
		r.logger.Str("table", q.Table),
		r.logger.Int("fetch", fetch),
		r.logger.Int("read", len(page)),
		r.logger.Any("duration_ms", time.Since(start).Milliseconds()))
	return page, nil
}

// ExportRows — строки батча ключей в порядке keys (§115.7).
//
// Фильтр журнала повторяется, чтобы из нескольких прогонов записи взять тот же,
// что отобран ключом: ключ — новейший подходящий прогон во всём окне q, окно
// батча [мин, макс] лежит внутри него, значит новейший подходящий прогон в окне
// батча — тот же самый. Прогон, дописанный после чтения ключей, моложе макс и в
// окно не попадает.
//
// Узкое окно держат два условия по ключу сортировки (date_create, date_request):
//   - `date_create IN (…)` — значения из самих ключей. Отсекает партиции и
//     гранулы на ЛЮБОЙ таблице, включая внешнюю §64, где окна по date_create
//     нет (DateCreateAligned=false): без него батч читал всю таблицу — замер
//     TestLogReader_Export_RowsPrunedOnExternalTable дал 200 000 из 200 000;
//   - голый `date_request BETWEEN` — второй столбец ключа: отсекает гранулы
//     внутри дня. Обёрнутый в toDateTime64 (как в searchConds) этого не делает.
func (r *LogReaderCH) ExportRows(
	ctx context.Context, q port.LogQuery, keys []port.LogExportKey, bodyRunes int,
) ([]port.LogExportRow, error) {
	if !isSafeTableName(q.Table) {
		return nil, fmt.Errorf("invalid table name: %q", q.Table)
	}
	if len(keys) == 0 {
		return nil, nil
	}
	sql, args := r.exportRowsSQL(ctx, q, keys, bodyRunes)
	conn, err := r.liveConn()
	if err != nil {
		return nil, err
	}
	start := time.Now()
	rows, err := conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, classifyCHErr("clickhouse export rows", err)
	}
	byID, err := scanExportRows(rows, bodyRunes, len(keys))
	if err != nil {
		return nil, err
	}
	out := make([]port.LogExportRow, 0, len(keys))
	for _, k := range keys {
		if row, ok := byID[k.ID]; ok {
			out = append(out, row)
		}
	}
	if len(out) < len(keys) {
		r.logger.Debug("logs export: records vanished between reads",
			r.logger.Str("table", q.Table),
			r.logger.Int("keys", len(keys)),
			r.logger.Int("found", len(out)))
	}
	r.logger.Debug("logs export: rows batch",
		r.logger.Str("table", q.Table),
		r.logger.Int("keys", len(keys)),
		r.logger.Int("body_runes", bodyRunes),
		r.logger.Any("duration_ms", time.Since(start).Milliseconds()))
	return out, nil
}

// exportRowsSQL — запрос строк батча и его аргументы. Окно q сужается до
// [мин−1 мс, макс] ключей батча: и условия searchConds по date_request, и
// окно по date_create (§72.4) берут его оттуда.
func (r *LogReaderCH) exportRowsSQL(
	ctx context.Context, q port.LogQuery, keys []port.LogExportKey, bodyRunes int,
) (string, []any) {
	ids := make([]string, 0, len(keys))
	days := make([]string, 0, 2)
	minTs, maxTs := keys[0].TsMs, keys[0].TsMs
	for _, k := range keys {
		ids = append(ids, k.ID)
		if !slices.Contains(days, k.DateCreate) {
			days = append(days, k.DateCreate)
		}
		minTs, maxTs = min(minTs, k.TsMs), max(maxTs, k.TsMs)
	}
	q.BeforeID, q.Unresolved, q.Limit = "", false, 0
	q.SinceMs, q.UntilMs = minTs-1, maxTs

	conds, args := r.searchConds(ctx, q)
	conds = append(conds, "ID IN ?", "date_create IN ?",
		"date_request BETWEEN toDateTime(?) AND toDateTime(?)")
	args = append(args, ids, days, floorDiv(minTs, 1000), ceilDiv(maxTs, 1000))

	// Аргументы проекции (срез тел) — перед аргументами WHERE.
	cols, all := listCols, make([]any, 0, len(args)+2)
	if bodyRunes > 0 {
		cols, all = previewCols, append(all, bodyRunes, bodyRunes)
	}
	sql := fmt.Sprintf(`SELECT %s FROM %s%s ORDER BY date_request DESC, ID DESC LIMIT 1 BY ID`,
		cols, q.Table, whereClause(conds))
	return sql, append(all, args...)
}

// scanExportRows дочитывает батч целиком и закрывает курсор. Без тел колонки
// listCols, полные длины тел — нули.
func scanExportRows(rows chdriver.Rows, bodyRunes, capacity int) (map[string]port.LogExportRow, error) {
	defer rows.Close()
	byID := make(map[string]port.LogExportRow, capacity)
	for rows.Next() {
		var row port.LogExportRow
		if bodyRunes > 0 {
			rec, reqRunes, respRunes, err := scanLogRowPreview(rows)
			if err != nil {
				return nil, err
			}
			row = port.LogExportRow{Rec: *rec, ReqRunes: reqRunes, RespRunes: respRunes}
		} else {
			rec, err := scanLogRow(rows)
			if err != nil {
				return nil, err
			}
			row = port.LogExportRow{Rec: *rec}
		}
		byID[row.Rec.ID] = row
	}
	if err := rows.Err(); err != nil {
		return nil, classifyCHErr("clickhouse export rows", err)
	}
	return byID, nil
}

// whereClause — " WHERE a AND b" или пусто.
func whereClause(conds []string) string {
	if len(conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(conds, " AND ")
}

// floorDiv / ceilDiv — секунды для голого предиката по DateTime: окно не
// сужается при миллисекундах, отличных от нуля (DateTime64 внешних таблиц §64).
func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && a < 0 {
		q--
	}
	return q
}

func ceilDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && a > 0 {
		q++
	}
	return q
}
