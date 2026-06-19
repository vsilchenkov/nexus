// Package clickhouse — Web-сторонний адаптер к ClickHouse для чтения логов.
// Реализует port.LogReader (replay + live-tail).
package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// ConnProvider — узкий read-only доступ к ClickHouse-соединению.
// Определён на стороне consumer'а (CLAUDE.md §3): LogReaderCH не должен
// зависеть от конкретного владельца conn'а — clickhouse.Manager
// автоматически реализует этот интерфейс структурным совпадением.
type ConnProvider interface {
	Conn() chdriver.Conn
}

// LogReaderCH принимает ConnProvider, а не raw driver.Conn: при hot-reload
// (Phase 6.3.2.5) clickhouse.Manager swap'ает внутренний conn, и каждый
// новый запрос автоматически идёт в свежий клиент.
type LogReaderCH struct {
	conn   ConnProvider
	logger logging.Logger
}

var _ port.LogReader = (*LogReaderCH)(nil)

func NewLogReader(conn ConnProvider, logger logging.Logger) *LogReaderCH {
	return &LogReaderCH{conn: conn, logger: logger}
}

// liveConn возвращает текущее соединение из ConnProvider или ошибку, если
// Manager уже закрыт.
func (r *LogReaderCH) liveConn() (chdriver.Conn, error) {
	c := r.conn.Conn()
	if c == nil {
		return nil, fmt.Errorf("clickhouse conn is nil")
	}
	return c, nil
}

const selectCols = `ID, type, url, method, parameters, request, response,
	status, reason, date_create, date_request, date_response,
	duration, done, checksum_request, checksum_response,
	Host, IP, attempts, attempts_details`

// GetByID — одна запись по ID (UUID v4) из указанной таблицы.
//
// table — формат "db.table". Sanitized: проверяем, что состоит только из
// допустимых символов; иначе ошибка (защита от SQL-инъекции).
//
// На один ID в таблице может быть несколько строк: ClickHouse не enforce'ит
// PRIMARY KEY uniqueness (MergeTree пишет каждый INSERT отдельно), и хотя
// UUID v4-коллизии исключены, в attempts_details может оказаться replay
// того же ID или ручной повторный INSERT при file-fallback restore. Поэтому
// ORDER BY date_request DESC — берём самую свежую запись детерминированно.
func (r *LogReaderCH) GetByID(ctx context.Context, table, id string) (*domain.LogRecord, error) {
	if !isSafeTableName(table) {
		return nil, fmt.Errorf("invalid table name: %q", table)
	}
	conn, err := r.liveConn()
	if err != nil {
		return nil, err
	}
	rows, err := conn.Query(ctx,
		fmt.Sprintf(`SELECT %s FROM %s WHERE ID = ? ORDER BY date_request DESC LIMIT 1`, selectCols, table), id)
	if err != nil {
		return nil, fmt.Errorf("clickhouse select log: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, domain.ErrNotFound
	}
	rec, err := scanLogRow(rows)
	if err != nil {
		return nil, err
	}
	return rec, nil
}

// ListSince — записи с date_request_unix_ms > cursor; ASC, LIMIT.
func (r *LogReaderCH) ListSince(ctx context.Context, table string, cursor int64, limit int) ([]*domain.LogRecord, error) {
	if !isSafeTableName(table) {
		return nil, fmt.Errorf("invalid table name: %q", table)
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	conn, err := r.liveConn()
	if err != nil {
		return nil, err
	}
	rows, err := conn.Query(ctx, fmt.Sprintf(
		`SELECT %s FROM %s WHERE toUnixTimestamp64Milli(toDateTime64(date_request, 3)) > ?
		 ORDER BY date_request ASC LIMIT ?`, selectCols, table), cursor, limit)
	if err != nil {
		return nil, fmt.Errorf("clickhouse list since: %w", err)
	}
	defer rows.Close()

	var out []*domain.LogRecord
	for rows.Next() {
		rec, err := scanLogRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}

// Search — snapshot с расширенными фильтрами (§7.4 Phase 6.8). Сортировка
// по date_request DESC, LIMIT (1..500, default 100). Все фильтры опциональны;
// пустые поля q не попадают в WHERE.
func (r *LogReaderCH) Search(ctx context.Context, q port.LogQuery) ([]*domain.LogRecord, error) {
	if !isSafeTableName(q.Table) {
		return nil, fmt.Errorf("invalid table name: %q", q.Table)
	}
	limit := q.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	var (
		conds []string
		args  []any
	)
	if q.SinceMs > 0 {
		conds = append(conds, "toUnixTimestamp64Milli(toDateTime64(date_request, 3)) > ?")
		args = append(args, q.SinceMs)
	}
	if q.UntilMs > 0 {
		conds = append(conds, "toUnixTimestamp64Milli(toDateTime64(date_request, 3)) <= ?")
		args = append(args, q.UntilMs)
	}
	if q.IP != "" {
		conds = append(conds, "IP = ?")
		args = append(args, q.IP)
	}
	if q.Host != "" {
		conds = append(conds, "Host = ?")
		args = append(args, q.Host)
	}
	switch q.Status {
	case "ok":
		conds = append(conds, "status BETWEEN 200 AND 299")
	case "err":
		conds = append(conds, "(status >= 400 OR status = 0)")
	}
	switch q.Done {
	case "yes":
		conds = append(conds, "done = 1")
	case "no":
		conds = append(conds, "done = 0")
	}
	if q.Q != "" {
		conds = append(conds,
			"(positionCaseInsensitiveUTF8(url, ?) > 0 OR positionCaseInsensitiveUTF8(request, ?) > 0 OR positionCaseInsensitiveUTF8(response, ?) > 0)")
		args = append(args, q.Q, q.Q, q.Q)
	}

	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}

	conn, err := r.liveConn()
	if err != nil {
		return nil, err
	}
	rows, err := conn.Query(ctx, fmt.Sprintf(
		`SELECT %s FROM %s%s ORDER BY date_request DESC LIMIT ?`,
		selectCols, q.Table, where), append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("clickhouse search: %w", err)
	}
	defer rows.Close()

	var out []*domain.LogRecord
	for rows.Next() {
		rec, err := scanLogRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}

// CountErrors считает записи-ошибки в таблице за окно (sinceMs, untilMs]
// (§20.3). Ошибка = status>=400 OR status=0 (сетевой сбой) OR done=0.
func (r *LogReaderCH) CountErrors(ctx context.Context, table string, sinceMs, untilMs int64) (uint64, error) {
	if !isSafeTableName(table) {
		return 0, fmt.Errorf("invalid table name: %q", table)
	}
	conds := []string{"(status >= 400 OR status = 0 OR done = 0)"}
	var args []any
	if sinceMs > 0 {
		conds = append(conds, "toUnixTimestamp64Milli(toDateTime64(date_request, 3)) > ?")
		args = append(args, sinceMs)
	}
	if untilMs > 0 {
		conds = append(conds, "toUnixTimestamp64Milli(toDateTime64(date_request, 3)) <= ?")
		args = append(args, untilMs)
	}
	conn, err := r.liveConn()
	if err != nil {
		return 0, err
	}
	var n uint64
	q := fmt.Sprintf("SELECT count() FROM %s WHERE %s", table, strings.Join(conds, " AND "))
	if err := conn.QueryRow(ctx, q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("clickhouse count errors: %w", err)
	}
	return n, nil
}

// CountFailed считает НЕдоставленные записи (строго done=0) за окно
// (sinceMs, untilMs] (§35 — KPI «неудачные доставки»). Каждая такая запись —
// сообщение, ушедшее в DLQ.
func (r *LogReaderCH) CountFailed(ctx context.Context, table string, sinceMs, untilMs int64) (uint64, error) {
	if !isSafeTableName(table) {
		return 0, fmt.Errorf("invalid table name: %q", table)
	}
	conds := []string{"done = 0"}
	var args []any
	if sinceMs > 0 {
		conds = append(conds, "toUnixTimestamp64Milli(toDateTime64(date_request, 3)) > ?")
		args = append(args, sinceMs)
	}
	if untilMs > 0 {
		conds = append(conds, "toUnixTimestamp64Milli(toDateTime64(date_request, 3)) <= ?")
		args = append(args, untilMs)
	}
	conn, err := r.liveConn()
	if err != nil {
		return 0, err
	}
	var n uint64
	q := fmt.Sprintf("SELECT count() FROM %s WHERE %s", table, strings.Join(conds, " AND "))
	if err := conn.QueryRow(ctx, q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("clickhouse count failed: %w", err)
	}
	return n, nil
}

// failedConds — условия «done=0 в окне (sinceMs, untilMs]» + позиционные args.
func failedConds(sinceMs, untilMs int64) ([]string, []any) {
	conds := []string{"done = 0"}
	var args []any
	if sinceMs > 0 {
		conds = append(conds, "toUnixTimestamp64Milli(toDateTime64(date_request, 3)) > ?")
		args = append(args, sinceMs)
	}
	if untilMs > 0 {
		conds = append(conds, "toUnixTimestamp64Milli(toDateTime64(date_request, 3)) <= ?")
		args = append(args, untilMs)
	}
	return conds, args
}

// FailedIDs — уникальные ID записей done=0 за окно (sinceMs, untilMs], до cap
// (capped=true, если есть ещё). Для очистки «Неудачных доставок»: эти ID
// отменяются (qcancel), чтобы DLQ-репроцессор перестал их повторять (§34.4).
func (r *LogReaderCH) FailedIDs(ctx context.Context, table string, sinceMs, untilMs int64, cap int) ([]string, bool, error) {
	if !isSafeTableName(table) {
		return nil, false, fmt.Errorf("invalid table name: %q", table)
	}
	if cap <= 0 {
		cap = 10000
	}
	conds, args := failedConds(sinceMs, untilMs)
	conn, err := r.liveConn()
	if err != nil {
		return nil, false, err
	}
	q := fmt.Sprintf("SELECT DISTINCT ID FROM %s WHERE %s LIMIT %d", table, strings.Join(conds, " AND "), cap+1)
	rows, err := conn.Query(ctx, q, args...)
	if err != nil {
		return nil, false, fmt.Errorf("clickhouse failed ids: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, false, fmt.Errorf("scan failed id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	capped := len(ids) > cap
	if capped {
		ids = ids[:cap]
	}
	return ids, capped, nil
}

// DeleteFailed — lightweight DELETE записей done=0 за окно (sinceMs, untilMs] из
// CH-таблицы узла (очистка вида «Неудачные доставки»). Возвращает число удалённых
// (посчитано до DELETE — CH lightweight delete счётчик не отдаёт).
func (r *LogReaderCH) DeleteFailed(ctx context.Context, table string, sinceMs, untilMs int64) (uint64, error) {
	if !isSafeTableName(table) {
		return 0, fmt.Errorf("invalid table name: %q", table)
	}
	n, err := r.CountFailed(ctx, table, sinceMs, untilMs)
	if err != nil || n == 0 {
		return 0, err
	}
	conds, args := failedConds(sinceMs, untilMs)
	conn, err := r.liveConn()
	if err != nil {
		return 0, err
	}
	stmt := fmt.Sprintf("DELETE FROM %s WHERE %s", table, strings.Join(conds, " AND "))
	if err := conn.Exec(ctx, stmt, args...); err != nil {
		return 0, fmt.Errorf("clickhouse delete failed: %w", err)
	}
	return n, nil
}

// NodeKPI — ТОЧНЫЕ per-node KPI из ClickHouse-логов за окно (sinceMs, untilMs]
// (§21, вкладки «Обзор»/«Метрики» узла). В отличие от Prometheus increase() —
// мгновенные точные счётчики по уникальным запросам (ID), без rate-экстраполяции
// и без зависимости от доступности Prometheus:
//
//	Total     = countDistinct(ID)         — уникальных запросов за окно;
//	Delivered = uniqExactIf(ID, done = 1) — из них хотя бы раз доставлены (2xx);
//	Errors    = Total - Delivered         — так и не доставлены;
//	P95/P99   = перцентили длительности (мс) по всем попыткам.
func (r *LogReaderCH) NodeKPI(ctx context.Context, table string, sinceMs, untilMs int64) (port.NodeKPI, error) {
	if !isSafeTableName(table) {
		return port.NodeKPI{}, fmt.Errorf("invalid table name: %q", table)
	}
	conds := []string{"1"}
	var args []any
	if sinceMs > 0 {
		conds = append(conds, "toUnixTimestamp64Milli(toDateTime64(date_request, 3)) > ?")
		args = append(args, sinceMs)
	}
	if untilMs > 0 {
		conds = append(conds, "toUnixTimestamp64Milli(toDateTime64(date_request, 3)) <= ?")
		args = append(args, untilMs)
	}
	conn, err := r.liveConn()
	if err != nil {
		return port.NodeKPI{}, err
	}
	q := fmt.Sprintf(`SELECT
		countDistinct(ID) AS total,
		uniqExactIf(ID, done = 1) AS delivered,
		quantile(0.95)(duration) AS p95,
		quantile(0.99)(duration) AS p99
	FROM %s WHERE %s`, table, strings.Join(conds, " AND "))
	var total, delivered uint64
	var p95, p99 float64
	if err := conn.QueryRow(ctx, q, args...).Scan(&total, &delivered, &p95, &p99); err != nil {
		return port.NodeKPI{}, fmt.Errorf("clickhouse node kpi: %w", err)
	}
	if delivered > total {
		delivered = total
	}
	if math.IsNaN(p95) {
		p95 = 0
	}
	if math.IsNaN(p99) {
		p99 = 0
	}
	return port.NodeKPI{Total: total, Delivered: delivered, Errors: total - delivered, P95ms: p95, P99ms: p99}, nil
}

// NodeChart — временной ряд трафика узла за окно (sinceMs, untilMs], разбитый на
// buckets равных бакетов (count() и countIf(done=0) на бакет). Плотный ряд:
// отсутствующие бакеты — нули, ASC по времени, выравнивание бакетов как у
// toStartOfInterval (по эпохе). Источник графика «Трафик» вкладки «Обзор».
func (r *LogReaderCH) NodeChart(ctx context.Context, table string, sinceMs, untilMs int64, buckets int) ([]port.SeriesPoint, error) {
	if !isSafeTableName(table) {
		return nil, fmt.Errorf("invalid table name: %q", table)
	}
	if buckets <= 0 {
		buckets = 48
	}
	if untilMs <= sinceMs {
		return []port.SeriesPoint{}, nil
	}
	stepSec := (untilMs - sinceMs) / int64(buckets) / 1000
	if stepSec < 1 {
		stepSec = 1
	}
	stepMs := stepSec * 1000
	conn, err := r.liveConn()
	if err != nil {
		return nil, err
	}
	q := fmt.Sprintf(`SELECT
		toInt64(toUnixTimestamp(toStartOfInterval(date_request, INTERVAL %d SECOND))) AS bucket_s,
		count() AS cnt,
		countIf(done = 0) AS errs
	FROM %s
	WHERE toUnixTimestamp64Milli(toDateTime64(date_request, 3)) > ?
	  AND toUnixTimestamp64Milli(toDateTime64(date_request, 3)) <= ?
	GROUP BY bucket_s ORDER BY bucket_s`, stepSec, table)
	rows, err := conn.Query(ctx, q, sinceMs, untilMs)
	if err != nil {
		return nil, fmt.Errorf("clickhouse node chart: %w", err)
	}
	defer rows.Close()
	type bkt struct{ cnt, errs uint64 }
	got := make(map[int64]bkt)
	for rows.Next() {
		var bsec int64
		var cnt, errs uint64
		if err := rows.Scan(&bsec, &cnt, &errs); err != nil {
			return nil, fmt.Errorf("scan node chart: %w", err)
		}
		got[bsec*1000] = bkt{cnt: cnt, errs: errs}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Плотный ряд от выровненного начала окна (как toStartOfInterval по эпохе).
	startMs := (sinceMs / stepMs) * stepMs
	out := make([]port.SeriesPoint, 0, buckets+2)
	for ts := startMs; ts <= untilMs; ts += stepMs {
		b := got[ts]
		out = append(out, port.SeriesPoint{TsMs: ts, Count: b.cnt, Errors: b.errs})
	}
	return out, nil
}

func scanLogRow(rows chdriver.Rows) (*domain.LogRecord, error) {
	var (
		r          domain.LogRecord
		typ        string
		dateCreate time.Time
		dateReq    time.Time
		dateResp   time.Time
	)
	if err := rows.Scan(
		&r.ID, &typ, &r.URL, &r.Method, &r.Parameters, &r.Request, &r.Response,
		&r.Status, &r.Reason, &dateCreate, &dateReq, &dateResp,
		&r.Duration, &r.Done, &r.ChecksumRequest, &r.ChecksumResponse,
		&r.Host, &r.IP, &r.Attempts, &r.AttemptsDetails,
	); err != nil {
		return nil, fmt.Errorf("scan log row: %w", err)
	}
	r.Type = domain.RootMethod(typ)
	r.DateCreate = dateCreate
	r.DateRequest = dateReq
	r.DateResponse = dateResp
	return &r, nil
}

// isSafeTableName — db.table из A-Za-z0-9_; обе части обязательны.
func isSafeTableName(name string) bool {
	if name == "" {
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

// Unused — silence unused-import warning if package compiled standalone.
var _ = errors.New
