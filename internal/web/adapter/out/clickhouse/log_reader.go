// Package clickhouse — Web-сторонний адаптер к ClickHouse для чтения логов.
// Реализует port.LogReader (replay + live-tail).
package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"strings"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// chUnavailable сообщает, что err — ошибка ДОСТУПНОСТИ ClickHouse (сервер
// лежит, dial refused, таймаут, отменённый контекст), а не серверная ошибка
// запроса (битый SQL, нет таблицы — это *clickhouse.Exception). Только такие
// ошибки read-path деградирует мягко (см. domain.ErrLogsBackendUnavailable);
// серверные — пробрасывает как есть, чтобы реальные баги не маскировались.
func chUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

// classifyCHErr оборачивает ошибку CH-запроса контекстом op. Если это сбой
// доступности — дополнительно помечает её domain.ErrLogsBackendUnavailable
// (через errors.Join, чтобы errors.Is ловил и sentinel, и исходную ошибку, а
// текст лога сохранял детали).
func classifyCHErr(op string, err error) error {
	wrapped := fmt.Errorf("%s: %w", op, err)
	if chUnavailable(err) {
		return errors.Join(wrapped, domain.ErrLogsBackendUnavailable)
	}
	return wrapped
}

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

const selectCols = `ID, type, http_method, url, method, parameters, request, response,
	status, reason, date_create, date_request, date_response,
	duration, done, checksum_request, checksum_response,
	Host, IP, attempts, attempts_details, node_id`

// listCols — как selectCols, но тела (request/response) НЕ читаются с диска:
// возвращаются пустыми (”). Список логов их не показывает (§42 — тела ленивые,
// тянутся при разворачивании строки через GetByIDPreview/GetBodyChunk). Чтение
// тяжёлых body-колонок для КАЖДОЙ строки списка раздувало I/O и сеть на узлах с
// большими телами и тормозило пагинацию при скролле (§44). Порядок/число колонок
// совпадает с selectCols — используется общий scanLogRow (скан по позиции, имя
// алиаса неважно). Контентный поиск (q) по-прежнему фильтрует по реальным телам в
// WHERE (там body-колонки и читаются — только при q).
//
// ВАЖНО: алиасы НЕ называем request/response — в ClickHouse алиас SELECT затеняет
// одноимённую колонку в WHERE, и q-поиск (position(request, ?)) искал бы по пустой
// строке. С нейтральными именами WHERE фильтрует по реальным колонкам.
const listCols = `ID, type, http_method, url, method, parameters, '' AS list_req, '' AS list_resp,
	status, reason, date_create, date_request, date_response,
	duration, done, checksum_request, checksum_response,
	Host, IP, attempts, attempts_details, node_id`

// previewCols — как selectCols, но тела заменены префиксом substringUTF8(col,1,?)
// (превью), а в конец добавлены полные длины lengthUTF8(col). Не тянет тела
// целиком в Go ради дефолтного разворачивания строки лога (§42). Порядок
// колонок совпадает с selectCols, плюс две длины в хвосте.
const previewCols = `ID, type, http_method, url, method, parameters,
	substringUTF8(request, 1, ?), substringUTF8(response, 1, ?),
	status, reason, date_create, date_request, date_response,
	duration, done, checksum_request, checksum_response,
	Host, IP, attempts, attempts_details, node_id,
	lengthUTF8(request), lengthUTF8(response)`

const (
	// defaultBodyPreviewRunes — сколько рун тела отдаёт GetByIDPreview по
	// умолчанию (первое разворачивание строки в UI).
	defaultBodyPreviewRunes = 64 * 1024
	// defaultBodyChunkRunes — дефолтный размер среза GetBodyChunk (постраничная
	// подгрузка «показать весь» и потоковое скачивание).
	defaultBodyChunkRunes = 1 * 1024 * 1024
)

// bodyColumn — whitelist «which» → имя колонки тела. Пользовательский ввод
// НИКОГДА не интерполируется как идентификатор колонки напрямую (§42).
func bodyColumn(which string) (string, bool) {
	switch which {
	case "request":
		return "request", true
	case "response":
		return "response", true
	default:
		return "", false
	}
}

// nodeFilterCond — условие per-node атрибуции «(node_id = ? OR node_id = ”)» и
// его аргумент (§37). Пустой node_id у legacy-записей (до миграции) трактуем как
// принадлежащий любому co-table узлу — старые данные неразличимы, истекают по TTL;
// НОВЫЙ трафик строго per-node. nodeID == "" → фильтр не добавляется (нет узла).
func nodeFilterCond(nodeID string) (string, []any) {
	if nodeID == "" {
		return "", nil
	}
	return "(node_id = ? OR node_id = '')", []any{nodeID}
}

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
		return nil, classifyCHErr("clickhouse select log", err)
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

// GetByIDPreview — метаданные записи + превью тел (первые previewRunes рун
// request/response) + их полные длины в рунах (§42). Один запрос; substringUTF8
// и lengthUTF8 считаются в ClickHouse, тело целиком в Go не приезжает.
func (r *LogReaderCH) GetByIDPreview(ctx context.Context, table, id string, previewRunes int) (*domain.LogRecord, int64, int64, error) {
	if !isSafeTableName(table) {
		return nil, 0, 0, fmt.Errorf("invalid table name: %q", table)
	}
	if previewRunes <= 0 {
		previewRunes = defaultBodyPreviewRunes
	}
	conn, err := r.liveConn()
	if err != nil {
		return nil, 0, 0, err
	}
	rows, err := conn.Query(ctx, fmt.Sprintf(
		`SELECT %s FROM %s WHERE ID = ? ORDER BY date_request DESC LIMIT 1`, previewCols, table),
		previewRunes, previewRunes, id)
	if err != nil {
		return nil, 0, 0, classifyCHErr("clickhouse select log preview", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, 0, 0, domain.ErrNotFound
	}
	return scanLogRowPreview(rows)
}

// GetBodyChunk — срез одного тела (which) по рунам: substringUTF8(col,
// offsetRunes+1, limitRunes) + полная длина lengthUTF8(col) (§42). CH использует
// 1-based индекс, поэтому offsetRunes+1. which вне whitelist → ошибка.
func (r *LogReaderCH) GetBodyChunk(ctx context.Context, table, id, which string, offsetRunes, limitRunes int) (string, int64, error) {
	if !isSafeTableName(table) {
		return "", 0, fmt.Errorf("invalid table name: %q", table)
	}
	col, ok := bodyColumn(which)
	if !ok {
		return "", 0, fmt.Errorf("invalid body selector: %q", which)
	}
	if offsetRunes < 0 {
		offsetRunes = 0
	}
	if limitRunes <= 0 {
		limitRunes = defaultBodyChunkRunes
	}
	conn, err := r.liveConn()
	if err != nil {
		return "", 0, err
	}
	rows, err := conn.Query(ctx, fmt.Sprintf(
		`SELECT substringUTF8(%s, ?, ?), lengthUTF8(%s) FROM %s WHERE ID = ? ORDER BY date_request DESC LIMIT 1`,
		col, col, table), offsetRunes+1, limitRunes, id)
	if err != nil {
		return "", 0, classifyCHErr("clickhouse select body chunk", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return "", 0, domain.ErrNotFound
	}
	var (
		chunk string
		total uint64
	)
	if err := rows.Scan(&chunk, &total); err != nil {
		return "", 0, fmt.Errorf("scan body chunk: %w", err)
	}
	return chunk, int64(total), nil
}

// ListSince — записи узла nodeID с date_request_unix_ms > cursor; ASC, LIMIT.
func (r *LogReaderCH) ListSince(ctx context.Context, table, nodeID string, cursor int64, limit int) ([]*domain.LogRecord, error) {
	if !isSafeTableName(table) {
		return nil, fmt.Errorf("invalid table name: %q", table)
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	// §44: тела не читаем (listCols) — SSE-поток их не шлёт (toLogDTO с
	// includeBodies=false), тянутся лениво при разворачивании строки.
	conds := []string{"toUnixTimestamp64Milli(toDateTime64(date_request, 3)) > ?"}
	args := []any{cursor}
	if c, a := nodeFilterCond(nodeID); c != "" {
		conds = append([]string{c}, conds...)
		args = append(a, args...)
	}
	conn, err := r.liveConn()
	if err != nil {
		return nil, err
	}
	rows, err := conn.Query(ctx, fmt.Sprintf(
		`SELECT %s FROM %s WHERE %s ORDER BY date_request ASC LIMIT ?`,
		listCols, table, strings.Join(conds, " AND ")), append(args, limit)...)
	if err != nil {
		return nil, classifyCHErr("clickhouse list since", err)
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
	if c, a := nodeFilterCond(q.NodeID); c != "" {
		conds = append(conds, c)
		args = append(args, a...)
	}
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
		listCols, q.Table, where), append(args, limit)...)
	if err != nil {
		return nil, classifyCHErr("clickhouse search", err)
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
func (r *LogReaderCH) CountErrors(ctx context.Context, table, nodeID string, sinceMs, untilMs int64) (uint64, error) {
	if !isSafeTableName(table) {
		return 0, fmt.Errorf("invalid table name: %q", table)
	}
	var conds []string
	var args []any
	if c, a := nodeFilterCond(nodeID); c != "" {
		conds = append(conds, c)
		args = append(args, a...)
	}
	conds = append(conds, "(status >= 400 OR status = 0 OR done = 0)")
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
func (r *LogReaderCH) CountFailed(ctx context.Context, table, nodeID string, sinceMs, untilMs int64) (uint64, error) {
	if !isSafeTableName(table) {
		return 0, fmt.Errorf("invalid table name: %q", table)
	}
	conds, args := failedConds(nodeID, sinceMs, untilMs)
	conn, err := r.liveConn()
	if err != nil {
		return 0, err
	}
	var n uint64
	q := fmt.Sprintf("SELECT count() FROM %s WHERE %s", table, strings.Join(conds, " AND "))
	if err := conn.QueryRow(ctx, q, args...).Scan(&n); err != nil {
		return 0, classifyCHErr("clickhouse count failed", err)
	}
	return n, nil
}

// failedConds — условия «done=0 в окне (sinceMs, untilMs] для узла nodeID» +
// позиционные args (§35/§37). nodeID == "" → без per-node фильтра.
func failedConds(nodeID string, sinceMs, untilMs int64) ([]string, []any) {
	var conds []string
	var args []any
	if c, a := nodeFilterCond(nodeID); c != "" {
		conds = append(conds, c)
		args = append(args, a...)
	}
	conds = append(conds, "done = 0")
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
func (r *LogReaderCH) FailedIDs(ctx context.Context, table, nodeID string, sinceMs, untilMs int64, cap int) ([]string, bool, error) {
	if !isSafeTableName(table) {
		return nil, false, fmt.Errorf("invalid table name: %q", table)
	}
	if cap <= 0 {
		cap = 10000
	}
	conds, args := failedConds(nodeID, sinceMs, untilMs)
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
func (r *LogReaderCH) DeleteFailed(ctx context.Context, table, nodeID string, sinceMs, untilMs int64) (uint64, error) {
	if !isSafeTableName(table) {
		return 0, fmt.Errorf("invalid table name: %q", table)
	}
	n, err := r.CountFailed(ctx, table, nodeID, sinceMs, untilMs)
	if err != nil || n == 0 {
		return 0, err
	}
	conds, args := failedConds(nodeID, sinceMs, untilMs)
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
func (r *LogReaderCH) NodeKPI(ctx context.Context, table, nodeID string, sinceMs, untilMs int64) (port.NodeKPI, error) {
	if !isSafeTableName(table) {
		return port.NodeKPI{}, fmt.Errorf("invalid table name: %q", table)
	}
	conds := []string{"1"}
	var args []any
	if c, a := nodeFilterCond(nodeID); c != "" {
		conds = append(conds, c)
		args = append(args, a...)
	}
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
func (r *LogReaderCH) NodeChart(ctx context.Context, table, nodeID string, sinceMs, untilMs int64, buckets int) ([]port.SeriesPoint, error) {
	if !isSafeTableName(table) {
		return nil, fmt.Errorf("invalid table name: %q", table)
	}
	if buckets <= 0 {
		buckets = 48
	}
	if untilMs <= sinceMs {
		return []port.SeriesPoint{}, nil
	}
	stepSec := max((untilMs-sinceMs)/int64(buckets)/1000, 1)
	stepMs := stepSec * 1000
	conn, err := r.liveConn()
	if err != nil {
		return nil, err
	}
	conds := []string{
		"toUnixTimestamp64Milli(toDateTime64(date_request, 3)) > ?",
		"toUnixTimestamp64Milli(toDateTime64(date_request, 3)) <= ?",
	}
	args := []any{sinceMs, untilMs}
	if c, a := nodeFilterCond(nodeID); c != "" {
		conds = append([]string{c}, conds...)
		args = append(a, args...)
	}
	q := fmt.Sprintf(`SELECT
		toInt64(toUnixTimestamp(toStartOfInterval(date_request, INTERVAL %d SECOND))) AS bucket_s,
		count() AS cnt,
		countIf(done = 0) AS errs
	FROM %s
	WHERE %s
	GROUP BY bucket_s ORDER BY bucket_s`, stepSec, table, strings.Join(conds, " AND "))
	rows, err := conn.Query(ctx, q, args...)
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
		&r.ID, &typ, &r.HTTPMethod, &r.URL, &r.Method, &r.Parameters, &r.Request, &r.Response,
		&r.Status, &r.Reason, &dateCreate, &dateReq, &dateResp,
		&r.Duration, &r.Done, &r.ChecksumRequest, &r.ChecksumResponse,
		&r.Host, &r.IP, &r.Attempts, &r.AttemptsDetails, &r.NodeID,
	); err != nil {
		return nil, fmt.Errorf("scan log row: %w", err)
	}
	r.Type = domain.RootMethod(typ)
	r.DateCreate = dateCreate
	r.DateRequest = dateReq
	r.DateResponse = dateResp
	return &r, nil
}

// scanLogRowPreview — как scanLogRow, но Request/Response держат превью, а в
// хвосте идут полные длины тел (lengthUTF8, UInt64) — порядок колонок previewCols.
func scanLogRowPreview(rows chdriver.Rows) (*domain.LogRecord, int64, int64, error) {
	var (
		r          domain.LogRecord
		typ        string
		dateCreate time.Time
		dateReq    time.Time
		dateResp   time.Time
		reqLen     uint64
		respLen    uint64
	)
	if err := rows.Scan(
		&r.ID, &typ, &r.HTTPMethod, &r.URL, &r.Method, &r.Parameters, &r.Request, &r.Response,
		&r.Status, &r.Reason, &dateCreate, &dateReq, &dateResp,
		&r.Duration, &r.Done, &r.ChecksumRequest, &r.ChecksumResponse,
		&r.Host, &r.IP, &r.Attempts, &r.AttemptsDetails, &r.NodeID,
		&reqLen, &respLen,
	); err != nil {
		return nil, 0, 0, fmt.Errorf("scan log row preview: %w", err)
	}
	r.Type = domain.RootMethod(typ)
	r.DateCreate = dateCreate
	r.DateRequest = dateReq
	r.DateResponse = dateResp
	return &r, int64(reqLen), int64(respLen), nil
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
