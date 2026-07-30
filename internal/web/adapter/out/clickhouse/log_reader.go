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
	"sync"
	"time"

	chgo "github.com/ClickHouse/clickhouse-go/v2"
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
//
// §67: отсутствие колонки client_host (внешняя таблица §64, владелец ещё не
// выполнил ручной ALTER из release notes) — ТОЖЕ мягкая деградация «логи
// временно недоступны», а не 500: SELECT'ы читают колонку списком, и до ALTER
// каждый поллинг вкладки «Логи» флудил бы 500/Sentry раз в несколько секунд
// (поймано на стенде). Привязано строго к client_host — прочие missing-column
// (чужие поломки DDL) остаются серверными ошибками и не маскируются.
func classifyCHErr(op string, err error) error {
	wrapped := fmt.Errorf("%s: %w", op, err)
	if chUnavailable(err) || isMissingColumnErr(err, "client_host") {
		return errors.Join(wrapped, domain.ErrLogsBackendUnavailable)
	}
	return wrapped
}

// Коды серверных ошибок ClickHouse «нет такой колонки» (§67 деградация
// внешних таблиц §64, у которых владелец ещё не выполнил ручной ALTER).
const (
	chCodeNotFoundColumnInBlock = 10 // NOT_FOUND_COLUMN_IN_BLOCK
	chCodeNoSuchColumnInTable   = 16 // NO_SUCH_COLUMN_IN_TABLE
	chCodeUnknownIdentifier     = 47 // UNKNOWN_IDENTIFIER
)

// isMissingColumnErr сообщает, что err — серверная ошибка CH «колонка col не
// существует». Используется для мягкой деградации facet-запросов по внешним
// таблицам (§64), где новая обязательная колонка ещё не добавлена вручную:
// такие ошибки — транзитное состояние деплой-окна, а не баг (см. §67).
func isMissingColumnErr(err error, col string) bool {
	var ex *chgo.Exception
	if !errors.As(err, &ex) {
		return false
	}
	switch ex.Code {
	case chCodeNotFoundColumnInBlock, chCodeNoSuchColumnInTable, chCodeUnknownIdentifier:
		return strings.Contains(ex.Message, col)
	}
	return false
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

	// tableUsage + кеш — политика видимости записей без node_id (см.
	// nodeFilter). Опционален: nil → прежнее поведение (послабление всегда).
	tableUsage    port.NodeTableUsage
	usageMu       sync.Mutex
	usageCounts   map[string]int
	usageAt       time.Time
	usageTriedAt  time.Time
	usageInflight bool

	// ownership — гейт владения таблицей (§70.4). nil = поведение до §70.
	// Собственного кеша здесь нет: Guard кеширует вердикты сам.
	ownership Ownership
}

const (
	// usageCacheTTL — как долго живёт карта «таблица → число узлов». Read-path
	// логов спрашивает её на каждый запрос, а меняется она только при
	// создании/переносе/удалении узла, поэтому минуты хватает: цена задержки —
	// временно прежнее (более мягкое) правило видимости.
	usageCacheTTL = time.Minute
	// usageRetryInterval — пауза после НЕудачной попытки: не даёт лежащему
	// PostgreSQL получать по запросу на каждое обращение к логам.
	usageRetryInterval = 15 * time.Second
	// usageQueryTimeout — потолок на сам запрос (одна лёгкая агрегация).
	usageQueryTimeout = 3 * time.Second
)

var _ port.LogReader = (*LogReaderCH)(nil)

func NewLogReader(conn ConnProvider, logger logging.Logger) *LogReaderCH {
	return &LogReaderCH{conn: conn, logger: logger}
}

// SetTableUsage включает строгую атрибуцию записей на ОБЩИХ таблицах логов
// (см. nodeFilter). Вызывается один раз в main-wiring; nil сохраняет прежнее
// поведение.
func (r *LogReaderCH) SetTableUsage(u port.NodeTableUsage) {
	r.usageMu.Lock()
	defer r.usageMu.Unlock()
	r.tableUsage = u
	r.usageCounts, r.usageAt = nil, time.Time{}
}

// SetOwnership подключает гейт владения (§70.4): запрещает удаление записей в
// чужой таблице и включает строгую атрибуцию там же (см. nodeFilter).
// nil сохраняет прежнее поведение.
func (r *LogReaderCH) SetOwnership(o Ownership) { r.ownership = o }

// ownsTable — таблица принадлежит этой ноде. Без подключённого гейта отвечает
// true (поведение до §70). Ошибку проверки трактуем как «не наша»: у
// вызывающих это либо отказ в удалении, либо строгий фильтр — обе деградации
// безопасны.
func (r *LogReaderCH) ownsTable(ctx context.Context, table string) bool {
	if r.ownership == nil {
		return true
	}
	owns, err := r.ownership.OwnsTable(ctx, table)
	if err != nil {
		r.logger.Warn("log reader: ownership check failed, treating table as foreign",
			r.logger.Str("table", table), r.logger.Err(err))
		return false
	}
	return owns
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
	Host, IP, client_host, attempts, attempts_details, node_id, request_size, response_size`

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
	Host, IP, client_host, attempts, attempts_details, node_id, request_size, response_size`

// previewCols — как selectCols, но тела заменены префиксом substringUTF8(col,1,?)
// (превью), а в конец добавлены полные длины lengthUTF8(col). Не тянет тела
// целиком в Go ради дефолтного разворачивания строки лога (§42). Порядок
// колонок совпадает с selectCols, плюс две длины в хвосте.
const previewCols = `ID, type, http_method, url, method, parameters,
	substringUTF8(request, 1, ?), substringUTF8(response, 1, ?),
	status, reason, date_create, date_request, date_response,
	duration, done, checksum_request, checksum_response,
	Host, IP, client_host, attempts, attempts_details, node_id, request_size, response_size,
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

// nodeFilter — условие per-node атрибуции (§37) и его аргументы. nodeID == ""
// → фильтр не добавляется (нет узла).
//
// Записи с пустым node_id (legacy — писались до появления колонки либо не через
// Sender) неразличимы по владельцу, поэтому засчитываются узлу — но только на
// ЛИЧНОЙ таблице, где других владельцев и быть не может. На ОБЩЕЙ таблице такое
// послабление показывало узлу чужие записи, а после переноса узла в другую
// команду — ещё и через границу команд (поймано на стенде: у переехавшего узла
// «появились» 590 201 чужая строка). Там фильтр строгий: только свой node_id.
//
// Неизвестно, общая ли таблица (порт не подключён или запрос упал) → прежнее,
// более мягкое правило: скрыть свои логи хуже, чем показать лишние.
// §70.4: на таблице, принадлежащей другой ноде (внешняя таблица §64 в чужой
// БД), карта «таблица → число узлов» тоже бесполезна — она считается по СВОЕЙ
// PostgreSQL и покажет «личная», хотя записи туда пишет и сосед. Поэтому чужая
// таблица всегда даёт строгий фильтр.
func (r *LogReaderCH) nodeFilter(ctx context.Context, table, nodeID string) (string, []any) {
	if nodeID == "" {
		return "", nil
	}
	if r.tableIsShared(ctx, table) || !r.ownsTable(ctx, table) {
		return "node_id = ?", []any{nodeID}
	}
	return "(node_id = ? OR node_id = '')", []any{nodeID}
}

// tableIsShared — на таблицу ссылается больше одного узла. Карта считается
// одним запросом и кешируется (usageCacheTTL): read-path зовёт это на каждый
// запрос логов/метрик.
func (r *LogReaderCH) tableIsShared(ctx context.Context, table string) bool {
	if table == "" {
		return false
	}
	counts, ok := r.tableCounts(ctx)
	if !ok {
		return false
	}
	return counts[table] > 1
}

// tableCounts — карта «таблица → число узлов» из кеша, при протухании —
// одно фоновое обновление.
//
// Горячий путь (кеш свеж) — только чтение map под мьютексом, без I/O: этот
// метод зовётся на КАЖДЫЙ запрос логов/метрик, а дашборд считает KPI по всем
// узлам разом. Обновление идёт ВНЕ блокировки и только в одной горутине
// (usageInflight) — иначе 12 параллельных NodeKPI выстроились бы в очередь на
// время запроса к PostgreSQL, а при протухшем кеше ещё и ушли бы в него все
// сразу. Остальные в этот момент работают по прежней карте.
//
// Неудачная попытка тоже отмечается временем (usageTriedAt): без этого лежащий
// PostgreSQL превратил бы КАЖДЫЙ запрос логов в новый запрос к нему плюс строку
// в лог — то есть сбой БД усиливался бы кратно трафику UI.
func (r *LogReaderCH) tableCounts(ctx context.Context) (map[string]int, bool) {
	r.usageMu.Lock()
	usage := r.tableUsage
	cached, at, tried, inflight := r.usageCounts, r.usageAt, r.usageTriedAt, r.usageInflight
	fresh := cached != nil && time.Since(at) < usageCacheTTL
	switch {
	case usage == nil:
		r.usageMu.Unlock()
		return nil, false
	case fresh || inflight || time.Since(tried) < usageRetryInterval:
		r.usageMu.Unlock()
		return cached, cached != nil
	}
	r.usageInflight = true
	r.usageTriedAt = time.Now()
	r.usageMu.Unlock()

	// Запрос переживает отмену пользовательского запроса (ушёл со страницы —
	// карта всё равно обновится), но со своим потолком по времени.
	qctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), usageQueryTimeout)
	defer cancel()
	counts, err := usage.CountsByCHTable(qctx)

	r.usageMu.Lock()
	defer r.usageMu.Unlock()
	r.usageInflight = false
	if err != nil {
		r.logger.Warn("log reader: ch table usage lookup failed, keeping previous node attribution",
			r.logger.Err(err))
		// Протухшая карта лучше отсутствующей: правило останется прежним до
		// следующей успешной попытки, а не дёргается туда-сюда на каждом сбое.
		return r.usageCounts, r.usageCounts != nil
	}
	r.usageCounts, r.usageAt = counts, time.Now()
	return counts, true
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
	if c, a := r.nodeFilter(ctx, table, nodeID); c != "" {
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

// condLogOK / condLogErr — SQL-предикаты быстрых фильтров «ОК» и «Ошибки»
// вкладки логов (§72.1). Успех = запись закрыта И внешний узел ответил 2xx/3xx;
// «Ошибки» — ПОЛНОЕ ДОПОЛНЕНИЕ успеха, поэтому ok ∪ err = все записи, а
// ok ∩ err = ∅.
//
// До §72.1 предикаты были `status BETWEEN 200 AND 299` и `(status >= 400 OR
// status = 0)`: запись с 3xx не попадала НИ В ОДИН из фильтров (пропадала из
// UI), а незавершённая запись с кодом 2xx числилась успехом. Дополнительный
// мотив — ровно этот предикат применял клиентский фильтр списка логов и
// применяет красная подсветка строки, так что серверный фильтр показывает то
// же, что видел пользователь.
//
// Семантика `done` (yes/no) намеренно не тронута: на ней держатся KPI
// «неудачных доставок» §35 (CountFailed — строго done=0).
const (
	condLogOK  = "(done = 1 AND status >= 200 AND status < 400)"
	condLogErr = "NOT " + condLogOK
)

// partitionMarginDays — запас к границам по date_create, выведенным из окна по
// date_request (§72.4). Партиции месячные, поэтому лишний день ничего не стоит,
// а закрывает расхождение часовых поясов, перекос часов между инстансами
// Sender'а и записи, дошедшие до ClickHouse позже своего date_request
// (durable-retry §38).
const partitionMarginDays = 1

// dayUTC — граница по date_create (тип Date) как литерал YYYY-MM-DD.
// Считается в UTC: date_create пишется как UTC-день момента date_request, а
// DateTime рендерится в часовом поясе сервера — брать toDate(date_request) в
// SQL было бы неверно.
func dayUTC(ms int64, deltaDays int) string {
	return time.UnixMilli(ms).UTC().AddDate(0, 0, deltaDays).Format(time.DateOnly)
}

// dateCreateConds — то же временное окно, но по колонке PARTITION BY (§72.4).
//
// Без него ClickHouse читает таблицу ЦЕЛИКОМ: условие по date_request партиции
// не отсекает, хотя date_request и стоит вторым полем ключа сортировки. Замер
// на 200 000 строк (окно в сутки, TestClickHouse_DateCreatePruning_E2E): 200 000
// прочитанных строк и 3 части против 8 192 строк и 1 части; разрыв растёт с
// объёмом таблицы.
//
// Работает только когда usecase подтвердил инвариант write-path (см.
// LogQuery.DateCreateAligned): для внешних таблиц §64 сужение отключено, иначе
// оно молча теряло бы строки.
//
// Литерал через toDate(?) — константа сворачивается на анализе запроса, и
// KeyCondition использует её для отсечения партиций.
func dateCreateConds(q port.LogQuery) ([]string, []any) {
	if !q.DateCreateAligned {
		return nil, nil
	}
	var (
		conds []string
		args  []any
	)
	if q.SinceMs > 0 {
		conds = append(conds, "date_create >= toDate(?)")
		args = append(args, dayUTC(q.SinceMs, -partitionMarginDays))
	}
	if q.UntilMs > 0 {
		conds = append(conds, "date_create <= toDate(?)")
		args = append(args, dayUTC(q.UntilMs, partitionMarginDays))
	}
	return conds, args
}

// searchConds строит WHERE-условия расширенных фильтров (§48) — ЕДИНСТВЕННЫЙ
// источник для Search и Count (§67 «Всего»): один и тот же набор условий
// гарантирует, что счётчик считает ровно то, что показывает список.
func (r *LogReaderCH) searchConds(ctx context.Context, q port.LogQuery) ([]string, []any) {
	var (
		conds []string
		args  []any
	)
	if c, a := r.nodeFilter(ctx, q.Table, q.NodeID); c != "" {
		conds = append(conds, c)
		args = append(args, a...)
	}
	// §72.4: то же окно по колонке PARTITION BY — ставится ПЕРЕД условиями по
	// date_request, порядок conds и args обязан совпадать.
	if c, a := dateCreateConds(q); len(c) > 0 {
		conds = append(conds, c...)
		args = append(args, a...)
	}
	if q.SinceMs > 0 {
		conds = append(conds, "toUnixTimestamp64Milli(toDateTime64(date_request, 3)) > ?")
		args = append(args, q.SinceMs)
	}
	if q.UntilMs > 0 {
		if q.BeforeID != "" {
			// §44/45-fix: строгий keyset-курсор (date_request, ID) — перешагивает
			// «плотные» секунды (сотни строк с одинаковым date_request секундной
			// точности), где простой `<=` зацикливался на одной секунде. Монотонные
			// функции CH индексирует — гранулы по date_request прунятся.
			conds = append(conds,
				"(toUnixTimestamp64Milli(toDateTime64(date_request, 3)) < ? "+
					"OR (toUnixTimestamp64Milli(toDateTime64(date_request, 3)) = ? AND ID < ?))")
			args = append(args, q.UntilMs, q.UntilMs, q.BeforeID)
		} else {
			conds = append(conds, "toUnixTimestamp64Milli(toDateTime64(date_request, 3)) <= ?")
			args = append(args, q.UntilMs)
		}
	}
	if q.IP != "" {
		conds = append(conds, "IP = ?")
		args = append(args, q.IP)
	}
	if q.Host != "" {
		conds = append(conds, "Host = ?")
		args = append(args, q.Host)
	}
	if q.ClientHost != "" { // §67
		conds = append(conds, "client_host = ?")
		args = append(args, q.ClientHost)
	}
	switch q.Status {
	case "ok":
		conds = append(conds, condLogOK)
	case "err":
		conds = append(conds, condLogErr)
	}
	switch q.Done {
	case "yes":
		conds = append(conds, "done = 1")
	case "no":
		conds = append(conds, "done = 0")
	}
	if q.Method != "" {
		conds = append(conds, "method = ?")
		args = append(args, q.Method)
	}
	// §48: полнотекстовый фильтр строится из распарсенного AST (usecase кладёт
	// QExpr через logsearch.Parse; сырое q.Q адаптер не использует). Работает
	// поверх listCols: алиасы list_req/list_resp не затеняют реальные колонки
	// request/response — position*/match в WHERE читают их с диска (см.
	// комментарий к listCols).
	if q.QExpr != nil {
		c, a := exprConds(q.QExpr)
		conds = append(conds, c)
		args = append(args, a...)
	}
	return conds, args
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

	conds, args := r.searchConds(ctx, q)
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}

	conn, err := r.liveConn()
	if err != nil {
		return nil, err
	}
	rows, err := conn.Query(ctx, fmt.Sprintf(
		`SELECT %s FROM %s%s ORDER BY date_request DESC, ID DESC LIMIT ?`,
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

// countTimeout — серверный потолок count()-запроса (§67 «Всего»): дорогой
// полнотекстовый count по огромной таблице отваливается по таймауту, UI
// мягко деградирует до «Показано N» без «из M».
const countTimeout = 10 * time.Second

// Count — точное число записей под теми же фильтрами, что и Search (§67,
// счётчик «Показано N из M»). BeforeID (keyset-курсор пагинации) сознательно
// игнорируется: «Всего» считается по фильтрам, а не по странице. Таймаут —
// countTimeout; его превышение классифицируется как unavailable (деградация,
// не 500).
func (r *LogReaderCH) Count(ctx context.Context, q port.LogQuery) (uint64, error) {
	if !isSafeTableName(q.Table) {
		return 0, fmt.Errorf("invalid table name: %q", q.Table)
	}
	q.BeforeID = "" // курсор страницы не влияет на total
	cctx, cancel := context.WithTimeout(ctx, countTimeout)
	defer cancel()

	conds, args := r.searchConds(cctx, q)
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	conn, err := r.liveConn()
	if err != nil {
		return 0, err
	}
	var total uint64
	if err := conn.QueryRow(cctx, fmt.Sprintf("SELECT count() FROM %s%s", q.Table, where), args...).
		Scan(&total); err != nil {
		return 0, classifyCHErr("clickhouse count logs", err)
	}
	return total, nil
}

// CountErrors считает записи-ошибки в таблице за окно (sinceMs, untilMs]
// (§20.3). Ошибка = status>=400 OR status=0 (сетевой сбой) OR done=0.
func (r *LogReaderCH) CountErrors(ctx context.Context, table, nodeID string, sinceMs, untilMs int64) (uint64, error) {
	if !isSafeTableName(table) {
		return 0, fmt.Errorf("invalid table name: %q", table)
	}
	var conds []string
	var args []any
	if c, a := r.nodeFilter(ctx, table, nodeID); c != "" {
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
	conds, args := r.failedConds(ctx, table, nodeID, sinceMs, untilMs)
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
// Метод, а не свободная функция: атрибуция записей зависит от того, общая ли
// таблица (см. nodeFilter).
func (r *LogReaderCH) failedConds(ctx context.Context, table, nodeID string, sinceMs, untilMs int64) ([]string, []any) {
	var conds []string
	var args []any
	if c, a := r.nodeFilter(ctx, table, nodeID); c != "" {
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
	conds, args := r.failedConds(ctx, table, nodeID, sinceMs, untilMs)
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

// maxDistinctMethods — кап числа значений фасета Method (§48.3): дропдауну UI
// больше не нужно, а DISTINCT по всей таблице ограничивается LIMIT'ом.
const maxDistinctMethods = 200

// DistinctMethods — уникальные непустые значения колонки method узла (§48.3),
// отсортированные, до limit (кап maxDistinctMethods). method — 3-я компонента
// ORDER BY-ключа таблицы, чтение сравнительно дешёвое.
func (r *LogReaderCH) DistinctMethods(ctx context.Context, table, nodeID string, limit int) ([]string, error) {
	if !isSafeTableName(table) {
		return nil, fmt.Errorf("invalid table name: %q", table)
	}
	if limit <= 0 || limit > maxDistinctMethods {
		limit = maxDistinctMethods
	}
	conds := []string{"method != ''"}
	var args []any
	if c, a := r.nodeFilter(ctx, table, nodeID); c != "" {
		conds = append(conds, c)
		args = append(args, a...)
	}
	conn, err := r.liveConn()
	if err != nil {
		return nil, err
	}
	q := fmt.Sprintf("SELECT DISTINCT method FROM %s WHERE %s ORDER BY method LIMIT ?",
		table, strings.Join(conds, " AND "))
	rows, err := conn.Query(ctx, q, append(args, limit)...)
	if err != nil {
		return nil, classifyCHErr("clickhouse distinct methods", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, fmt.Errorf("scan method: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// maxDistinctClientHosts — кап значений фасета «Хост клиента» (§67), как
// maxDistinctMethods: дальше дропдаун с клиентской фильтрацией не имеет смысла.
const maxDistinctClientHosts = 200

// DistinctClientHosts — уникальные непустые значения колонки client_host узла
// (§67, фасет дропдауна «Хост клиента»), отсортированные, до limit. Таблица
// без колонки (внешняя §64 до ручного ALTER владельцем) — НЕ ошибка: пустой
// список + Debug (иначе каждое открытие дропдауна флудило бы 500/Sentry в
// деплой-окне).
func (r *LogReaderCH) DistinctClientHosts(ctx context.Context, table, nodeID string, limit int) ([]string, error) {
	if !isSafeTableName(table) {
		return nil, fmt.Errorf("invalid table name: %q", table)
	}
	if limit <= 0 || limit > maxDistinctClientHosts {
		limit = maxDistinctClientHosts
	}
	conds := []string{"client_host != ''"}
	var args []any
	if c, a := r.nodeFilter(ctx, table, nodeID); c != "" {
		conds = append(conds, c)
		args = append(args, a...)
	}
	conn, err := r.liveConn()
	if err != nil {
		return nil, err
	}
	q := fmt.Sprintf("SELECT DISTINCT client_host FROM %s WHERE %s ORDER BY client_host LIMIT ?",
		table, strings.Join(conds, " AND "))
	rows, err := conn.Query(ctx, q, append(args, limit)...)
	if err != nil {
		if isMissingColumnErr(err, "client_host") {
			// §51.9: тихая деградация — фиксируем причину на debug-уровне.
			r.logger.Debug("distinct client hosts: column missing (external table §64, manual ALTER pending)",
				r.logger.Str("table", table))
			return []string{}, nil
		}
		return nil, classifyCHErr("clickhouse distinct client hosts", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, fmt.Errorf("scan client host: %w", err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// DateRange — min/max date_request узла в UnixMilli (§48.3). count() в том же
// запросе служит guard'ом: min() по пустому набору в CH возвращает epoch-1970,
// а не NULL — при нуле строк отдаём (0, 0) («ограничений нет»).
func (r *LogReaderCH) DateRange(ctx context.Context, table, nodeID string) (int64, int64, error) {
	if !isSafeTableName(table) {
		return 0, 0, fmt.Errorf("invalid table name: %q", table)
	}
	where := ""
	var args []any
	if c, a := r.nodeFilter(ctx, table, nodeID); c != "" {
		where = " WHERE " + c
		args = a
	}
	conn, err := r.liveConn()
	if err != nil {
		return 0, 0, err
	}
	q := fmt.Sprintf(`SELECT
		toUnixTimestamp64Milli(toDateTime64(min(date_request), 3)),
		toUnixTimestamp64Milli(toDateTime64(max(date_request), 3)),
		count()
	FROM %s%s`, table, where)
	var minMs, maxMs int64
	var n uint64
	if err := conn.QueryRow(ctx, q, args...).Scan(&minMs, &maxMs, &n); err != nil {
		return 0, 0, classifyCHErr("clickhouse date range", err)
	}
	if n == 0 {
		return 0, 0, nil
	}
	return minMs, maxMs, nil
}

// DeleteFailed — lightweight DELETE записей done=0 за окно (sinceMs, untilMs] из
// CH-таблицы узла (очистка вида «Неудачные доставки»). Возвращает число удалённых
// (посчитано до DELETE — CH lightweight delete счётчик не отдаёт).
func (r *LogReaderCH) DeleteFailed(ctx context.Context, table, nodeID string, sinceMs, untilMs int64) (uint64, error) {
	if !isSafeTableName(table) {
		return 0, fmt.Errorf("invalid table name: %q", table)
	}
	// §70.4: единственный DML read-адаптера. На таблице другой ноды удаление
	// запрещено: фильтр по node_id защищает от чужих строк, но записи без
	// идентификатора (legacy) он не различает.
	if r.ownership != nil {
		if err := r.ownership.AssertOwnsTable(ctx, table); err != nil {
			return 0, err
		}
	}
	n, err := r.CountFailed(ctx, table, nodeID, sinceMs, untilMs)
	if err != nil || n == 0 {
		return 0, err
	}
	conds, args := r.failedConds(ctx, table, nodeID, sinceMs, untilMs)
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

// NodeKPI — per-node KPI из ClickHouse-логов за окно (sinceMs, untilMs] (§21,
// вкладки «Обзор»/«Метрики» узла; §44.A — счётчики шапки дашборда). В отличие от
// Prometheus increase() — мгновенные счётчики по уникальным запросам (ID), без
// rate-экстраполяции и без зависимости от доступности Prometheus.
//
// §44-perf: режим подсчёта уникальных управляется флагом approx (настройка
// app_settings.general.metrics_approx_counts, дефолт false = точно):
//
//   - approx=false (по умолчанию): countDistinct/uniqExactIf — ТОЧНЫЙ счёт
//     (полный хэш-сет ID). Дороже на больших объёмах.
//
//   - approx=true: uniq/uniqIf — HyperLogLog, ПРИБЛИЗИТЕЛЬНО (ошибка ~0.3%), но
//     в ~3× дешевле по CPU. Включается оператором, когда узлов/данных много и
//     точный distinct упирает ClickHouse в 100% CPU. Инвариант «шапка = Σ строк
//     таблицы» сохраняется в обоих режимах (обе стороны из одной агрегации).
//
//     Total     = countDistinct|uniq(ID)            — уникальных запросов за окно;
//     Delivered = uniqExactIf|uniqIf(ID, done = 1)  — из них хотя бы раз доставлены (2xx);
//     Errors    = Total - Delivered                 — так и не доставлены (guard delivered≤total);
//     P95/P99   = перцентили длительности (мс) по всем попыткам.
func (r *LogReaderCH) NodeKPI(ctx context.Context, table, nodeID string, sinceMs, untilMs int64, approx bool) (port.NodeKPI, error) {
	if !isSafeTableName(table) {
		return port.NodeKPI{}, fmt.Errorf("invalid table name: %q", table)
	}
	conds := []string{"1"}
	var args []any
	if c, a := r.nodeFilter(ctx, table, nodeID); c != "" {
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
	totalExpr, deliveredExpr := "countDistinct(ID)", "uniqExactIf(ID, done = 1)"
	if approx {
		totalExpr, deliveredExpr = "uniq(ID)", "uniqIf(ID, done = 1)"
	}
	q := fmt.Sprintf(`SELECT
		%s AS total,
		%s AS delivered,
		quantile(0.95)(duration) AS p95,
		quantile(0.99)(duration) AS p99
	FROM %s WHERE %s`, totalExpr, deliveredExpr, table, strings.Join(conds, " AND "))
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
	if c, a := r.nodeFilter(ctx, table, nodeID); c != "" {
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
		&r.Host, &r.IP, &r.ClientHost, &r.Attempts, &r.AttemptsDetails, &r.NodeID,
		&r.RequestSize, &r.ResponseSize,
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
		&r.Host, &r.IP, &r.ClientHost, &r.Attempts, &r.AttemptsDetails, &r.NodeID,
		&r.RequestSize, &r.ResponseSize,
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
