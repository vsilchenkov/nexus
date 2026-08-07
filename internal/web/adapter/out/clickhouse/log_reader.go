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
	usageFacts    *tableFacts
	usageAt       time.Time
	usageTriedAt  time.Time
	usageInflight bool

	// ownership — гейт владения таблицей (§70.4). nil = поведение до §70.
	// Собственного кеша здесь нет: Guard кеширует вердикты сам.
	ownership Ownership
}

// tableFacts — снимок фактов о таблицах логов из PostgreSQL, на которых стоит
// правило видимости записей без node_id (nodeFilter).
//
// Оба факта живут в ОДНОМ снимке и обновляются одним циклом намеренно: правило
// читает их вместе, и полуснимок (свежие счётчики + протухшее множество внешних
// таблиц) означал бы молча спрятанные логи. Ошибка любого из двух запросов
// отменяет обновление целиком — лучше согласованные старые факты, чем
// несогласованные свежие.
type tableFacts struct {
	// counts — «полное имя таблицы → сколько узлов на неё ссылается» (§61).
	counts map[string]int
	// external — таблицы, помеченные external_table хотя бы одним узлом (§64).
	external map[string]struct{}
}

const (
	// usageCacheTTL — как долго живёт снимок фактов о таблицах (tableFacts).
	// Read-path логов спрашивает его на каждый запрос, а меняется он только при
	// создании/переносе/удалении узла и смене флага external_table, поэтому
	// минуты хватает: цена задержки — временно прежнее правило видимости.
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
	r.usageFacts, r.usageAt = nil, time.Time{}
}

// SetOwnership подключает гейт владения (§70.4): запрещает удаление записей в
// чужой таблице. nil сохраняет прежнее поведение.
//
// На атрибуцию чтения гейт больше не влияет: правило видимости определяется
// только тем, помечена ли таблица внешней (§64), — см. nodeFilter.
func (r *LogReaderCH) SetOwnership(o Ownership) { r.ownership = o }

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
// По умолчанию фильтр СТРОГИЙ: узлу принадлежат записи с его node_id, и только
// они. Послабление «плюс записи с пустым node_id» осталось ровно в одном месте —
// на ОДИНОЧНОЙ внешней таблице §64.
//
// Почему §64 — исключение: её наполняет посторонний сервис, а node_id проставляет
// только Sender, поэтому там пустой node_id не аномалия, а КАЖДАЯ строка. Строгий
// фильтр прячет единственное содержимое таблицы — ровно то, ради чего узел заведён
// (бой: 10 222 523 записи в gate_logs.PDT_PDTExchange стали невидимы после §70).
// Общая внешняя таблица (на неё ссылается ≥2 узла) снова строгая: там послабление
// показало бы записи соседа, в том числе из другой команды.
//
// Почему послабление убрано с обычных таблиц: оно вводилось под legacy-записи,
// писавшиеся до появления колонки (§37, 2026-06-19). Своей истории они узлу не
// добавляют — это просто «чьи-то» строки, — а на общей таблице показывали чужое.
// Скорость роли не играла: замер на 5 млн строк дал одинаковые read_rows и
// read_bytes у обоих вариантов (node_id не входит в ORDER BY, гранулы им не
// отсекаются), см. §61.2.
//
// Владение таблицей (§70.4) на выбор фильтра больше НЕ влияет: правило и так
// строгое везде, кроме §64, а для внешней таблицы «чужая» — нормальное
// состояние, а не признак соседней ноды. Гейт владения остался там, где он и
// нужен, — на разрушающих операциях (DeleteFailedRows).
//
// Деградация: фактов о таблице нет (порт не подключён или запрос к PostgreSQL
// упал) → таблица считается внешней, то есть действует послабление. Инвариант
// прежний — СТРОГОСТЬ ВКЛЮЧАЮТ ТОЛЬКО ДОКАЗАННЫЕ ФАКТЫ: иначе недоступность
// PostgreSQL прячет всё содержимое внешних таблиц, а это и был боевой инцидент.
func (r *LogReaderCH) nodeFilter(ctx context.Context, table, nodeID string) (string, []any) {
	if nodeID == "" {
		return "", nil
	}
	if !r.tableIsShared(ctx, table) && r.tableIsExternal(ctx, table) {
		return "(node_id = ? OR node_id = '')", []any{nodeID}
	}
	return "node_id = ?", []any{nodeID}
}

// tableIsShared — на таблицу ссылается больше одного узла. Карта считается
// одним запросом и кешируется (usageCacheTTL): read-path зовёт это на каждый
// запрос логов/метрик.
func (r *LogReaderCH) tableIsShared(ctx context.Context, table string) bool {
	if table == "" {
		return false
	}
	facts, ok := r.facts(ctx)
	if !ok {
		return false
	}
	return facts.counts[table] > 1
}

// tableIsExternal — таблицу ведёт посторонний сервис (§64), то есть пустой
// node_id в ней штатен для каждой строки. Единственное основание для послабления
// в nodeFilter.
//
// Снимка фактов нет (порт не подключён или запрос упал) → true: послабление
// действует. Иначе недоступность PostgreSQL прячет ВСЁ содержимое внешних
// таблиц — это и был боевой инцидент. Симметрично tableIsShared, которая в той
// же ситуации тоже уходит в мягкую ветку; оба факта обязаны деградировать в одну
// сторону, иначе получается правило, строгое наполовину.
func (r *LogReaderCH) tableIsExternal(ctx context.Context, table string) bool {
	if table == "" {
		return false
	}
	facts, ok := r.facts(ctx)
	if !ok {
		r.logger.Debug("log reader: no table facts, treating table as external (lenient node attribution)",
			r.logger.Str("table", table))
		return true
	}
	_, external := facts.external[table]
	if external {
		// Тихое решение: на такой таблице узлу видны и записи с пустым node_id,
		// хотя везде остальное правило строгое. Без строки в логе «почему тут
		// видно чужое» не разобрать (ТЗ §51.9). Сообщение говорит про ФАКТ, а не
		// про решение вызывающего: смягчает атрибуцию nodeFilter, и только он.
		r.logger.Debug("log reader: table is marked external (§64), empty node_id counts toward the node",
			r.logger.Str("table", table))
	}
	return external
}

// facts — снимок фактов о таблицах из кеша, при протухании — одно фоновое
// обновление.
//
// Горячий путь (кеш свеж) — только чтение указателя под мьютексом, без I/O:
// этот метод зовётся на КАЖДЫЙ запрос логов/метрик, а дашборд считает KPI по
// всем узлам разом. Обновление идёт ВНЕ блокировки и только в одной горутине
// (usageInflight) — иначе 12 параллельных NodeKPI выстроились бы в очередь на
// время запроса к PostgreSQL, а при протухшем кеше ещё и ушли бы в него все
// сразу. Остальные в этот момент работают по прежнему снимку.
//
// Неудачная попытка тоже отмечается временем (usageTriedAt): без этого лежащий
// PostgreSQL превратил бы КАЖДЫЙ запрос логов в новый запрос к нему плюс строку
// в лог — то есть сбой БД усиливался бы кратно трафику UI.
//
// Возвращённый снимок неизменяем: обновление кладёт НОВЫЙ *tableFacts, а не
// правит карты на месте — читатели работают со своей копией без блокировки.
func (r *LogReaderCH) facts(ctx context.Context) (*tableFacts, bool) {
	r.usageMu.Lock()
	usage := r.tableUsage
	cached, at, tried, inflight := r.usageFacts, r.usageAt, r.usageTriedAt, r.usageInflight
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
	// снимок всё равно обновится), но со своим потолком по времени. Потолок один
	// на оба запроса: это верхняя граница ЗАДЕРЖКИ обновления, а не бюджет на
	// каждый — иначе при медленной БД цикл растягивался бы вдвое.
	qctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), usageQueryTimeout)
	defer cancel()
	next, err := loadTableFacts(qctx, usage)

	r.usageMu.Lock()
	defer r.usageMu.Unlock()
	r.usageInflight = false
	if err != nil {
		r.logger.Warn("log reader: ch table facts lookup failed, keeping previous node attribution",
			r.logger.Err(err))
		// Протухший снимок лучше отсутствующего: правило останется прежним до
		// следующей успешной попытки, а не дёргается туда-сюда на каждом сбое.
		return r.usageFacts, r.usageFacts != nil
	}
	r.usageFacts, r.usageAt = next, time.Now()
	return next, true
}

// loadTableFacts — оба факта одним циклом. Ошибка любого запроса отменяет
// обновление целиком: правило видимости читает счётчики и признак внешности
// вместе, и снимок, собранный наполовину из свежих, наполовину из старых
// данных, может спрятать логи узла (см. tableFacts).
func loadTableFacts(ctx context.Context, usage port.NodeTableUsage) (*tableFacts, error) {
	counts, err := usage.CountsByCHTable(ctx)
	if err != nil {
		return nil, fmt.Errorf("counts by ch table: %w", err)
	}
	external, err := usage.ExternalCHTables(ctx)
	if err != nil {
		return nil, fmt.Errorf("external ch tables: %w", err)
	}
	return &tableFacts{counts: counts, external: external}, nil
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
//
// §78.6: дедупа по ID здесь намеренно НЕТ (в отличие от Search). Это поток
// событий live-tail: повтор ID означает новый прогон доставки уже показанной
// записи — событие, которое оператор и должен увидеть, а не дубль страницы.
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
	// §79.1: Unresolved даёт здесь только КАНДИДАТОВ (строки done=0). Отсев
	// записей, у которых в окне есть прогон done=1, невыразим одним WHERE и
	// делается вторым запросом (см. deliveredAmong) — в FailedIDs и
	// searchUnresolved. Счётчики (CountFailed/Count) идут другим путём —
	// разностью агрегатов, и флаг перед вызовом снимают.
	if q.Unresolved {
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
//
// §78.6: единица выдачи — ЗАПИСЬ, а не строка таблицы. На один ID приходится
// столько строк, сколько раз запускался прогон доставки (redelivery Kafka,
// DLQ-репроцессор §36, ttl_expired, replay, дренаж retry-топика §38);
// внутренние ретраи одного прогона схлопнуты в attempts/attempts_details.
// `LIMIT 1 BY ID` оставляет свежайшую попытку каждой записи — ту же строку,
// что отдаёт GetByID при разворачивании. Без него страница из limit строк
// схлопывалась дедупом фронта в горстку записей, а Count считал другую
// единицу — боевое «Показано 17 из 40».
//
// Keyset-курсор (UntilMs+BeforeID) строится по свежайшей попытке последней
// записи страницы, поэтому СТАРЫЕ попытки уже показанной записи лежат ниже
// курсора и могут прийти повторно на следующей странице — их отсеивает дедуп
// фронта (useInfiniteLogs). Контракт конца истории (len < limit, §72.2) это не
// нарушает: пока остаются непрочитанные строки, страница набирается полностью.
func (r *LogReaderCH) Search(ctx context.Context, q port.LogQuery) ([]*domain.LogRecord, error) {
	if !isSafeTableName(q.Table) {
		return nil, fmt.Errorf("invalid table name: %q", q.Table)
	}
	if q.Unresolved {
		return r.searchUnresolved(ctx, q)
	}
	return r.searchPage(ctx, q)
}

// unresolvedSearchRounds — сколько страниц кандидатов адаптер готов прочитать,
// добирая страницу до limit после отсева доставленных (§79.1). Отсев редок
// (доставленная запись выпадает из вида навсегда после первой же очистки),
// поэтому трёх раундов хватает с запасом; предел нужен, чтобы патологический
// случай не превратил один запрос списка в неограниченный обход таблицы.
const unresolvedSearchRounds = 3

// searchUnresolved — страница списка «Неудачных доставок» (§79.1).
//
// Кандидаты (строки done=0) читаются обычной keyset-страницей, затем одним
// запросом отсеиваются записи, у которых в окне есть успешный прогон. Отсев
// уменьшает страницу, а фронт считает неполную страницу концом истории (§72.2),
// поэтому страница добирается следующими раундами по тому же курсору.
func (r *LogReaderCH) searchUnresolved(ctx context.Context, q port.LogQuery) ([]*domain.LogRecord, error) {
	limit := q.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	out := make([]*domain.LogRecord, 0, limit)
	cur := q
	for round := 0; round < unresolvedSearchRounds && len(out) < limit; round++ {
		page, err := r.searchPage(ctx, cur)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		ids := make([]string, 0, len(page))
		for _, rec := range page {
			ids = append(ids, rec.ID)
		}
		delivered, err := r.deliveredAmong(ctx, cur, ids)
		if err != nil {
			return nil, err
		}
		skip := make(map[string]struct{}, len(delivered))
		for _, id := range delivered {
			skip[id] = struct{}{}
		}
		for _, rec := range page {
			if _, ok := skip[rec.ID]; ok {
				continue
			}
			if len(out) == limit {
				break
			}
			out = append(out, rec)
		}
		if len(page) < limit {
			break // кандидаты кончились — честный конец истории
		}
		// Курсор следующего раунда — свежайшая попытка последней записи страницы
		// (та же логика, что у обычной keyset-пагинации выше).
		last := page[len(page)-1]
		cur.UntilMs = last.DateRequest.UnixMilli()
		cur.BeforeID = last.ID
		cur.Limit = limit
	}
	if len(out) < limit {
		r.logger.Debug("unresolved page short after filtering",
			r.logger.Str("table", q.Table),
			r.logger.Int("returned", len(out)),
			r.logger.Int("limit", limit))
	}
	return out, nil
}

// searchPage — одна keyset-страница под фильтрами q (без §79.1-отсева).
func (r *LogReaderCH) searchPage(ctx context.Context, q port.LogQuery) ([]*domain.LogRecord, error) {
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
		`SELECT %s FROM %s%s ORDER BY date_request DESC, ID DESC LIMIT 1 BY ID LIMIT ?`,
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
//
// §78.6: считаются ЗАПИСИ (uniqExact по ID), а не строки таблицы — та же
// единица, что у Search и NodeKPI §44. `count()` считал прогоны доставки, и
// счётчик расходился со списком («Показано 17 из 40»). uniqExact дороже
// count(): страховка — countTimeout выше (мягкая деградация до «Показано N»).
func (r *LogReaderCH) Count(ctx context.Context, q port.LogQuery) (uint64, error) {
	if !isSafeTableName(q.Table) {
		return 0, fmt.Errorf("invalid table name: %q", q.Table)
	}
	q.BeforeID = "" // курсор страницы не влияет на total
	cctx, cancel := context.WithTimeout(ctx, countTimeout)
	defer cancel()

	// §79.1: «Всего» под фильтром «Неудачные доставки» обязано считать ту же
	// единицу, что список и KPI, — записи без единого успешного прогона.
	if q.Unresolved {
		return r.countUnresolved(cctx, q, false)
	}

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
	if err := conn.QueryRow(cctx, fmt.Sprintf("SELECT uniqExact(ID) FROM %s%s", q.Table, where), args...).
		Scan(&total); err != nil {
		return 0, classifyCHErr("clickhouse count logs", err)
	}
	return total, nil
}

// CountErrors считает ошибки в таблице за окно (sinceMs, untilMs] (§20.3,
// Telegram-уведомления). Ошибка = status>=400 OR status=0 (сетевой сбой) OR done=0.
//
// §78.6: единица здесь намеренно оставлена СТРОКОЙ — это счётчик неудачных
// прогонов доставки для порога уведомления, а не «сколько записей показать».
// Ни к какому списку в UI он не стоит парой, поэтому переводить его на записи
// незачем (это изменило бы смысл порога у всех настроенных уведомлений).
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

// uniqueExprs — выражения «уникальных записей» и «из них доставленных».
// ЕДИНСТВЕННЫЙ источник этой пары: на ней держатся NodeKPI (§44), CountFailed и
// Count при Unresolved (§79.1). Разъехавшись, они дали бы ровно тот дефект,
// который чинит §79.1, — разные ответы на один вопрос на одном экране.
//
// approx=true — приблизительный режим (HLL, app_settings.general.
// metrics_approx_counts): в ~3× дешевле по CPU, ошибка ~0.3%.
func uniqueExprs(approx bool) (total, delivered string) {
	if approx {
		return "uniq(ID)", "uniqIf(ID, done = 1)"
	}
	return "countDistinct(ID)", "uniqExactIf(ID, done = 1)"
}

// subUnsigned — вычитание с клампом. В приблизительном режиме delivered
// считается по HLL независимо от total и может оказаться БОЛЬШЕ него; голое
// total-delivered на uint64 дало бы 1.8e19 на экране.
func subUnsigned(total, delivered uint64) uint64 {
	if delivered >= total {
		return 0
	}
	return total - delivered
}

// countUnresolved — число записей без единого прогона done=1 под фильтрами q
// (§79.1). Ровно выражение Errors из NodeKPI, поэтому KPI узла и KPI вкладки
// «Очередь» тождественны по построению, а не по совпадению.
//
// Предикат выражается АГРЕГАТАМИ, а не WHERE: строки done=0 и done=1 одной
// записи обязаны попасть в один и тот же запрос, иначе успешный прогон не
// вычтет неудачный. Поэтому Unresolved/Done снимаются с копии q.
func (r *LogReaderCH) countUnresolved(ctx context.Context, q port.LogQuery, approx bool) (uint64, error) {
	q.BeforeID, q.Limit = "", 0
	q.Unresolved, q.Done = false, ""
	conds, args := r.searchConds(ctx, q)
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	conn, err := r.liveConn()
	if err != nil {
		return 0, err
	}
	totalExpr, deliveredExpr := uniqueExprs(approx)
	var total, delivered uint64
	sql := fmt.Sprintf("SELECT %s AS total, %s AS delivered FROM %s%s",
		totalExpr, deliveredExpr, q.Table, where)
	if err := conn.QueryRow(ctx, sql, args...).Scan(&total, &delivered); err != nil {
		return 0, classifyCHErr("clickhouse count failed", err)
	}
	return subUnsigned(total, delivered), nil
}

// CountFailed — KPI «неудачные доставки» вкладки «Очередь» (§35).
// Единица — ЗАПИСЬ без единого успешного прогона (§79.1).
func (r *LogReaderCH) CountFailed(ctx context.Context, q port.LogQuery, approx bool) (uint64, error) {
	if !isSafeTableName(q.Table) {
		return 0, fmt.Errorf("invalid table name: %q", q.Table)
	}
	return r.countUnresolved(ctx, q, approx)
}

// failedIDsCap — потолок набора ID по умолчанию (когда вызывающий не задал свой).
const failedIDsCap = 10000

// FailedIDs — ID недоставленных записей под фильтрами q (§79.1), до cap.
//
// Два шага вместо одного запроса — вопрос стоимости, а не удобства. Кандидатов
// (строк done=0) мало, доставленных записей на порядки больше, поэтому
// множество строится по МАЛОЙ стороне: сначала кандидаты, затем проба
// «кто из них уже доставлен». Вариант `ID NOT IN (SELECT … done=1)` строил бы
// хэш-сет по всем доставленным записям окна на каждый вызов (список поллится
// раз в 15 с), `GROUP BY ID HAVING max(done)=0` — по всем различным ID окна.
func (r *LogReaderCH) FailedIDs(ctx context.Context, q port.LogQuery, cap int) ([]string, bool, error) {
	if !isSafeTableName(q.Table) {
		return nil, false, fmt.Errorf("invalid table name: %q", q.Table)
	}
	if cap <= 0 {
		cap = failedIDsCap
	}
	q.BeforeID, q.Limit = "", 0
	q.Unresolved, q.Done = false, "no" // кандидаты: строки с неудачным прогоном

	conds, args := r.searchConds(ctx, q)
	conn, err := r.liveConn()
	if err != nil {
		return nil, false, err
	}
	sql := fmt.Sprintf("SELECT DISTINCT ID FROM %s WHERE %s LIMIT %d",
		q.Table, strings.Join(conds, " AND "), cap+1)
	candidates, err := scanIDs(ctx, conn, sql, args, "clickhouse failed ids")
	if err != nil {
		return nil, false, err
	}
	capped := len(candidates) > cap
	if capped {
		candidates = candidates[:cap]
	}
	if len(candidates) == 0 {
		return nil, false, nil
	}

	// §81.5: отсев по маркеру причины — на уровне ЗАПИСИ, а не строки. Условие в
	// WHERE убрало бы только строки с маркером, и запись с двумя прогонами
	// («connection refused», затем «client_canceled») осталась бы кандидатом через
	// первую строку — то есть попала бы в массовый повтор, ради предотвращения
	// которого отсев и вводился.
	if len(q.ExcludeReasonPrefixes) > 0 {
		marked, mErr := r.markedAmong(ctx, q, candidates, q.ExcludeReasonPrefixes)
		if mErr != nil {
			return nil, false, mErr
		}
		candidates = diffIDs(candidates, marked)
		if len(candidates) == 0 {
			return nil, capped, nil
		}
	}

	delivered, err := r.deliveredAmong(ctx, q, candidates)
	if err != nil {
		return nil, false, err
	}
	ids := diffIDs(candidates, delivered)
	r.logger.Debug("failed ids resolved",
		r.logger.Str("table", q.Table),
		r.logger.Int("candidates", len(candidates)),
		r.logger.Int("delivered", len(delivered)),
		r.logger.Int("unresolved", len(ids)))
	return ids, capped, nil
}

// deliveredAmong — какие из ids имеют в окне хотя бы один прогон done=1.
// Список ID передаётся параметром: clickhouse-go рендерит []string как массив
// (`ID IN ['a','b']`), поэтому конкатенации в SQL не требуется.
func (r *LogReaderCH) deliveredAmong(ctx context.Context, q port.LogQuery, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	q.BeforeID, q.Limit = "", 0
	q.Unresolved, q.Done = false, "yes"
	conds, args := r.searchConds(ctx, q)
	conds = append(conds, "ID IN ?")
	args = append(args, ids)
	conn, err := r.liveConn()
	if err != nil {
		return nil, err
	}
	sql := fmt.Sprintf("SELECT DISTINCT ID FROM %s WHERE %s", q.Table, strings.Join(conds, " AND "))
	return scanIDs(ctx, conn, sql, args, "clickhouse delivered ids")
}

// markedAmong возвращает те из ids, у которых есть ХОТЯ БЫ ОДИН прогон с
// причиной из prefixes (§81.5). Зеркало deliveredAmong: единица «неудачной
// доставки» — запись, поэтому и признак ищется по всем её прогонам.
//
// startsWith, а не LIKE: маркер стоит в начале reason по построению, и так не
// нужно экранировать спецсимволы шаблона. Историю условие не покрывает — записи
// до §81.2 несут сырой текст ошибки (переписывать журнал нельзя).
func (r *LogReaderCH) markedAmong(ctx context.Context, q port.LogQuery, ids, prefixes []string) ([]string, error) {
	if len(ids) == 0 || len(prefixes) == 0 {
		return nil, nil
	}
	q.BeforeID, q.Limit = "", 0
	q.Unresolved, q.Done = false, ""
	q.ExcludeReasonPrefixes = nil
	conds, args := r.searchConds(ctx, q)

	var marks []string
	for _, pfx := range prefixes {
		if pfx == "" {
			continue
		}
		marks = append(marks, "startsWith(reason, ?)")
		args = append(args, pfx)
	}
	if len(marks) == 0 {
		return nil, nil
	}
	conds = append(conds, "("+strings.Join(marks, " OR ")+")")
	conds = append(conds, "ID IN ?")
	args = append(args, ids)

	conn, err := r.liveConn()
	if err != nil {
		return nil, err
	}
	sql := fmt.Sprintf("SELECT DISTINCT ID FROM %s WHERE %s", q.Table, strings.Join(conds, " AND "))
	return scanIDs(ctx, conn, sql, args, "clickhouse marked ids")
}

// scanIDs — общий сбор колонки ID.
func scanIDs(ctx context.Context, conn chgo.Conn, sql string, args []any, op string) ([]string, error) {
	rows, err := conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, classifyCHErr(op, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("%s: scan: %w", op, err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyCHErr(op, err)
	}
	return out, nil
}

// diffIDs — candidates минус delivered, порядок candidates сохраняется.
func diffIDs(candidates, delivered []string) []string {
	if len(delivered) == 0 {
		return candidates
	}
	skip := make(map[string]struct{}, len(delivered))
	for _, id := range delivered {
		skip[id] = struct{}{}
	}
	out := make([]string, 0, len(candidates))
	for _, id := range candidates {
		if _, ok := skip[id]; ok {
			continue
		}
		out = append(out, id)
	}
	return out
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

// DeleteFailedRows — lightweight DELETE строк done=0 записей ids (§79.2, очистка
// вида «Неудачные доставки» и уборка оригиналов после массового повтора).
// Возвращает число ЗАПИСЕЙ; строк уходит больше — у записи столько строк, сколько
// было прогонов доставки. Строки done=1 тех же записей остаются: история
// успешной доставки из журнала не пропадает.
//
// Один стейтмент на весь набор: lightweight delete в ClickHouse порождает
// мутацию, и 500 удалений по одному ID подряд создали бы мутационную нагрузку на
// ровном месте.
func (r *LogReaderCH) DeleteFailedRows(ctx context.Context, q port.LogQuery, ids []string) (uint64, error) {
	if !isSafeTableName(q.Table) {
		return 0, fmt.Errorf("invalid table name: %q", q.Table)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	// §70.4: единственный DML read-адаптера. На таблице другой ноды удаление
	// запрещено: фильтр по node_id защищает от чужих строк, но записи без
	// идентификатора (legacy) он не различает.
	if r.ownership != nil {
		if err := r.ownership.AssertOwnsTable(ctx, q.Table); err != nil {
			return 0, err
		}
	}
	conds, args := r.deleteFailedConds(ctx, q, ids)
	conn, err := r.liveConn()
	if err != nil {
		return 0, err
	}
	stmt := fmt.Sprintf("DELETE FROM %s WHERE %s", q.Table, strings.Join(conds, " AND "))
	if err := conn.Exec(ctx, stmt, args...); err != nil {
		return 0, fmt.Errorf("clickhouse delete failed rows: %w", err)
	}
	r.logger.Debug("failed rows deleted",
		r.logger.Str("table", q.Table), r.logger.Int("records", len(ids)))
	return uint64(len(ids)), nil
}

// deleteFailedConds — условия разрушающей операции: строки done=0 указанных
// записей в окне.
//
// Node-фильтр здесь ТОТ ЖЕ, что при чтении (nodeFilter): очистка обязана убирать
// ровно то, что узел ВИДИТ в «Неудачных доставках». Строгий `node_id = ?` в
// разрушающей операции выглядит безопаснее, но ломает два штатных случая —
// записи без идентификатора (legacy до §37) и внешнюю таблицу §64, где пустой
// node_id нормален для каждой строки: пользователь видел бы записи в списке, а
// «Очистить» молча оставляла бы их на месте (поймано integration-тестом
// TestAsyncQueue_PurgeFailed_E2E).
//
// Данные постороннего писателя защищает не фильтр, а гейт §70.4 (на чужой БД DML
// запрещён целиком) и точный список ids — он получен тем же чтением.
//
// Окно избыточно при заданных ids, но оставлено намеренно: date_create-условия
// §72.4 отсекают партиции, а без них DELETE сканирует таблицу целиком.
func (r *LogReaderCH) deleteFailedConds(ctx context.Context, q port.LogQuery, ids []string) ([]string, []any) {
	var conds []string
	var args []any
	if c, a := r.nodeFilter(ctx, q.Table, q.NodeID); c != "" {
		conds = append(conds, c)
		args = append(args, a...)
	}
	if c, a := dateCreateConds(q); len(c) > 0 {
		conds = append(conds, c...)
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
	conds = append(conds, "done = 0", "ID IN ?")
	args = append(args, ids)
	return conds, args
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
//
// §79.4: фильтры журнала (полнотекст, метод, хост клиента, статус) приходят в
// том же LogQuery и применяются тем же searchConds — KPI обязан описывать ровно
// то множество, что показывает список логов под теми же фильтрами.
//
// §79.5: перцентили считаются по ПОПЫТКАМ (строкам), а не по записям, — это
// характеристика внешнего вызова. Единица у них другая осознанно.
func (r *LogReaderCH) NodeKPI(ctx context.Context, q port.LogQuery, approx bool) (port.NodeKPI, error) {
	if !isSafeTableName(q.Table) {
		return port.NodeKPI{}, fmt.Errorf("invalid table name: %q", q.Table)
	}
	q.BeforeID, q.Limit = "", 0
	conds, args := r.searchConds(ctx, q)
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	conn, err := r.liveConn()
	if err != nil {
		return port.NodeKPI{}, err
	}
	totalExpr, deliveredExpr := uniqueExprs(approx)
	// §84.6: last_seen считается ТОЙ ЖЕ агрегацией — дополнительного прохода по
	// таблице не появляется. toUnixTimestamp64Milli(0) на пустом наборе даёт
	// эпоху, поэтому ниже стоит guard по total: без него «нет запросов»
	// превратилось бы в «последняя активность 56 лет назад».
	sql := fmt.Sprintf(`SELECT
		%s AS total,
		%s AS delivered,
		quantile(0.95)(duration) AS p95,
		quantile(0.99)(duration) AS p99,
		toInt64(toUnixTimestamp64Milli(toDateTime64(max(date_request), 3))) AS last_seen_ms
	FROM %s%s`, totalExpr, deliveredExpr, q.Table, where)
	var total, delivered uint64
	var p95, p99 float64
	var lastSeenMs int64
	if err := conn.QueryRow(ctx, sql, args...).Scan(&total, &delivered, &p95, &p99, &lastSeenMs); err != nil {
		return port.NodeKPI{}, classifyCHErr("clickhouse node kpi", err)
	}
	if delivered > total {
		delivered = total
	}
	p95, p99 = nanToZero(p95), nanToZero(p99)
	// §84.6: пустой набор даёт эпоху, а не NULL — «56 лет назад» вместо «нет
	// запросов». Признак пустоты здесь один и надёжный: total.
	if total == 0 {
		lastSeenMs = 0
	}
	return port.NodeKPI{
		Total:      total,
		Delivered:  delivered,
		Errors:     subUnsigned(total, delivered),
		P95ms:      p95,
		P99ms:      p99,
		LastSeenMs: lastSeenMs,
	}, nil
}

// defaultChartStepSec — шаг по умолчанию, если вызывающий его не задал (час).
// В норме шаг всегда приходит из usecase, согласованный с окном.
const defaultChartStepSec = 3600

// NodeChart — временной ряд трафика узла под фильтрами q, столбцами шириной
// c.StepSec. Плотный ряд: интервалы без данных — нули, ASC по времени,
// выравнивание как у toStartOfInterval (по эпохе).
//
// §79.5, две формы столбца (см. port.ChartQuery):
//
//   - ByRecord=true — запись относится к интервалу своего ПЕРВОГО прогона, а
//     «ошибка» определяется итоговым статусом записи в окне. Требует свёртки
//     строк в записи по всему окну, зато график ведёт себя как KPI и список:
//     доставленная повтором запись перестаёт быть красной в своём столбце;
//   - ByRecord=false — запись считается в том интервале, куда попал её прогон,
//     статус берётся по прогонам интервала. Одна стадия, дешевле.
//
// Обе формы считают ЗАПИСИ, а не строки: до §79.5 столбец был count() строк и
// на узле с недоступным приёмником завышался кратно числу повторов.
func (r *LogReaderCH) NodeChart(ctx context.Context, q port.LogQuery, c port.ChartQuery) ([]port.SeriesPoint, error) {
	if !isSafeTableName(q.Table) {
		return nil, fmt.Errorf("invalid table name: %q", q.Table)
	}
	if q.UntilMs <= q.SinceMs {
		return []port.SeriesPoint{}, nil
	}
	stepSec := c.StepSec
	if stepSec <= 0 {
		stepSec = defaultChartStepSec
	}
	stepMs := stepSec * 1000
	q.BeforeID, q.Limit = "", 0

	conds, args := r.searchConds(ctx, q)
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	conn, err := r.liveConn()
	if err != nil {
		return nil, err
	}
	sql := chartSQL(q.Table, where, stepSec, c)
	rows, err := conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, classifyCHErr("clickhouse node chart", err)
	}
	defer rows.Close()
	type bkt struct{ total, delivered uint64 }
	got := make(map[int64]bkt)
	for rows.Next() {
		var bsec int64
		var total, delivered uint64
		if err := rows.Scan(&bsec, &total, &delivered); err != nil {
			return nil, fmt.Errorf("scan node chart: %w", err)
		}
		got[bsec*1000] = bkt{total: total, delivered: delivered}
	}
	if err := rows.Err(); err != nil {
		return nil, classifyCHErr("clickhouse node chart", err)
	}
	// Плотный ряд от выровненного начала окна (как toStartOfInterval по эпохе).
	startMs := (q.SinceMs / stepMs) * stepMs
	out := make([]port.SeriesPoint, 0, (q.UntilMs-startMs)/stepMs+2)
	for ts := startMs; ts <= q.UntilMs; ts += stepMs {
		b := got[ts]
		out = append(out, port.SeriesPoint{
			TsMs:   ts,
			Count:  b.total,
			Errors: subUnsigned(b.total, b.delivered),
		})
	}
	return out, nil
}

// chartSQL — запрос ряда под выбранную форму столбца.
//
// nanToZero — quantile по пустому набору ClickHouse отдаёт NaN, а тот
// невыразим в JSON и уронил бы сериализацию ответа. Тот же приём, что в
// NodeKPI, но вынесенный в функцию: там два поля, здесь — по два на каждый
// интервал окна.
func nanToZero(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	return v
}

// NodeLatencyChart — перцентили длительности по интервалам окна (§84.5).
//
// Единица — ПОПЫТКА (строка), а не запись: длительность характеризует внешний
// вызов, и та же единица у p95/p99 в NodeKPI (§79.5). Отсюда одностадийность —
// сворачивать строки в записи здесь нечего и не нужно.
//
// WHERE строится тем же searchConds, что список логов и NodeChart: отдельный
// набор условий для латентности гарантированно разошёлся бы с ними (§79.4).
//
// Плотный ряд достраивается нулём ПОПЫТОК, а не нулевой латентностью: пустой
// интервал — разрыв линии, и отличить его клиент может только по Attempts.
func (r *LogReaderCH) NodeLatencyChart(ctx context.Context, q port.LogQuery, stepSec int64) ([]port.LatencyPoint, error) {
	if !isSafeTableName(q.Table) {
		return nil, fmt.Errorf("invalid table name: %q", q.Table)
	}
	if q.UntilMs <= q.SinceMs {
		return []port.LatencyPoint{}, nil
	}
	if stepSec <= 0 {
		stepSec = defaultChartStepSec
	}
	stepMs := stepSec * 1000
	q.BeforeID, q.Limit = "", 0

	conds, args := r.searchConds(ctx, q)
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	conn, err := r.liveConn()
	if err != nil {
		return nil, err
	}
	sql := fmt.Sprintf(`SELECT
		toInt64(toUnixTimestamp(toStartOfInterval(date_request, INTERVAL %d SECOND))) AS bucket_s,
		quantile(0.5)(duration) AS p50,
		quantile(0.95)(duration) AS p95,
		count() AS attempts
	FROM %s%s
	GROUP BY bucket_s ORDER BY bucket_s`, stepSec, q.Table, where)
	rows, err := conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, classifyCHErr("clickhouse node latency", err)
	}
	defer rows.Close()

	type bkt struct {
		p50, p95 float64
		attempts uint64
	}
	got := make(map[int64]bkt)
	for rows.Next() {
		var bsec int64
		var p50, p95 float64
		var attempts uint64
		if err := rows.Scan(&bsec, &p50, &p95, &attempts); err != nil {
			return nil, fmt.Errorf("scan node latency: %w", err)
		}
		// quantile по пустому набору даёт NaN — в JSON он невыразим и уронил бы
		// сериализацию ответа. Тот же приём, что в NodeKPI.
		got[bsec*1000] = bkt{p50: nanToZero(p50), p95: nanToZero(p95), attempts: attempts}
	}
	if err := rows.Err(); err != nil {
		return nil, classifyCHErr("clickhouse node latency", err)
	}

	startMs := (q.SinceMs / stepMs) * stepMs
	out := make([]port.LatencyPoint, 0, (q.UntilMs-startMs)/stepMs+2)
	for ts := startMs; ts <= q.UntilMs; ts += stepMs {
		b := got[ts]
		out = append(out, port.LatencyPoint{
			TsMs:     ts,
			P50ms:    b.p50,
			P95ms:    b.p95,
			Attempts: b.attempts,
		})
	}
	return out, nil
}

// Точный режим двухстадийный: сначала строки сворачиваются в записи
// (min(date_request) — когда запрос пришёл, max(done) — доставлен ли он в итоге),
// и только потом раскладываются по интервалам. Дешёвый режим раскладывает сразу
// и считает уникальные ID внутри интервала.
func chartSQL(table, where string, stepSec int64, c port.ChartQuery) string {
	if c.ByRecord {
		return fmt.Sprintf(`SELECT
			toInt64(toUnixTimestamp(toStartOfInterval(first_req, INTERVAL %d SECOND))) AS bucket_s,
			count() AS total,
			countIf(ok) AS delivered
		FROM (
			SELECT ID, min(date_request) AS first_req, max(done) AS ok
			FROM %s%s
			GROUP BY ID
		)
		GROUP BY bucket_s ORDER BY bucket_s`, stepSec, table, where)
	}
	totalExpr, deliveredExpr := uniqueExprs(c.Approx)
	return fmt.Sprintf(`SELECT
		toInt64(toUnixTimestamp(toStartOfInterval(date_request, INTERVAL %d SECOND))) AS bucket_s,
		%s AS total,
		%s AS delivered
	FROM %s%s
	GROUP BY bucket_s ORDER BY bucket_s`, stepSec, totalExpr, deliveredExpr, table, where)
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
