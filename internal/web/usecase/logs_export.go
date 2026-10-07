package usecase

import (
	"context"
	"errors"
	"time"

	"nexus/internal/domain"
	"nexus/internal/platform/clock"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// Ошибки выгрузки журнала (§115.5). Обе возвращаются ДО начала потока —
// handler отвечает на них обычным JSON.
var (
	// ErrLogsExportBusy — на реплике уже идёт MaxConcurrent выгрузок. Ответ
	// 503 + Retry-After: nginx перекидывает GET на соседнюю реплику.
	ErrLogsExportBusy = errors.New("logs export: all slots are busy")
	// ErrLogsExportRateLimited — превышена частота запусков пользователя (429).
	ErrLogsExportRateLimited = errors.New("logs export: rate limited")
)

// LogsExportStatus — полнота выгрузки (§115.8), пишется на лист «Параметры».
type LogsExportStatus string

const (
	// LogsExportComplete — выгружены все подходящие записи.
	LogsExportComplete LogsExportStatus = "complete"
	// LogsExportLimitedRows — подходящих больше запрошенного числа строк.
	LogsExportLimitedRows LogsExportStatus = "limited_rows"
	// LogsExportLimitedBytes — исчерпан бюджет текста ячеек MaxBytes.
	LogsExportLimitedBytes LogsExportStatus = "limited_bytes"
	// LogsExportIncomplete — ClickHouse отказал посреди выгрузки; файл
	// закрыт корректно, причина — в LogsExportSummary.Reason.
	LogsExportIncomplete LogsExportStatus = "incomplete"
	// LogsExportAborted — получатель ушёл (Остановить, закрытие вкладки) или
	// запись в ответ не удалась: файл не дописан.
	LogsExportAborted LogsExportStatus = "aborted"
)

// LogsExportLimits — потолки выгрузки (секция web.logs_export, §115.6).
type LogsExportLimits struct {
	MaxRows       int           // записей в файле
	MaxBytes      int64         // бюджет текста ячеек, байт UTF-8
	MaxBodyChars  int           // символов тела в ячейке
	MaxConcurrent int           // одновременных выгрузок на реплику
	RatePerMinute int           // запусков на пользователя в минуту; 0 — без лимита
	QueryTimeout  time.Duration // таймаут шага: отбор ключей целиком и каждый батч строк; 0 — без своего
}

// LogsExportAuditor — запись в журнал аудита (реализует *AuditUsecase).
type LogsExportAuditor interface {
	Log(ctx context.Context, a Actor, action, targetType, targetID string, details map[string]any)
}

// LogsExportDeps — зависимости NewLogsExportUsecase.
type LogsExportDeps struct {
	Reader  port.LogExportReader
	Nodes   logsNodeGetter
	Limiter RateLimiter // nil — частота не ограничивается
	Audit   LogsExportAuditor
	Clock   clock.Clock // nil — системные часы
	Limits  LogsExportLimits
	Logger  logging.Logger
}

// LogsExportOptions — выбор пользователя в форме выгрузки.
type LogsExportOptions struct {
	Rows   int  // предел записей; ≤0 или больше MaxRows — MaxRows
	Bodies bool // выгружать request/response
}

// LogsExportMeta — что известно к началу потока: sink по нему называет файл и
// заполняет лист «Параметры».
type LogsExportMeta struct {
	Node      *domain.Node
	Query     port.LogQuery
	Actor     Actor
	Rows      int  // действующий предел записей
	Bodies    bool // тела в файле
	BodyChars int  // символов тела в ячейке (0 — тел нет)
	Selected  int  // отобрано записей
	More      bool // подходящих больше Rows
	StartedAt time.Time
	Limits    LogsExportLimits
}

// LogsExportSummary — итог выгрузки.
type LogsExportSummary struct {
	Status          LogsExportStatus
	Reason          string // incomplete: timeout | unavailable | query_failed; aborted: этап
	Written         int    // записей в файле
	Bytes           int64  // текста ячеек, байт UTF-8
	Vanished        int    // отобраны, но исчезли до чтения строк (§79.2, TTL)
	TruncatedBodies int    // тел, урезанных до BodyChars
	FinishedAt      time.Time
}

// LogsExportSink — получатель потока (handler пишет XLSX прямо в ответ).
//
// Ошибка любого метода значит «получатель ушёл»: выгрузка прерывается со
// статусом aborted без Finish.
type LogsExportSink interface {
	Begin(ctx context.Context, m LogsExportMeta) error
	Row(ctx context.Context, r port.LogExportRow) error
	Finish(ctx context.Context, s LogsExportSummary) error
}

// LogsExportUsecase — выгрузка журнала узла в файл (§115).
type LogsExportUsecase struct {
	d     LogsExportDeps
	slots chan struct{}
}

// NewLogsExportUsecase собирает usecase. Слоты — на процесс (реплику Web).
func NewLogsExportUsecase(d LogsExportDeps) *LogsExportUsecase {
	if d.Clock == nil {
		d.Clock = clock.System()
	}
	return &LogsExportUsecase{d: d, slots: make(chan struct{}, max(d.Limits.MaxConcurrent, 1))}
}

// Limits — действующие потолки (форма показывает их пользователю).
func (u *LogsExportUsecase) Limits() LogsExportLimits { return u.d.Limits }

// BodyCapBytes — кап байт одного тела в оценке объёма: символ ячейки считается
// за два байта (кириллица). Для ASCII-тел оценка завышена вдвое, для тел из
// 4-байтных символов — занижена; это порядок величины, а не обещание.
func (u *LogsExportUsecase) BodyCapBytes() int64 { return int64(u.d.Limits.MaxBodyChars) * 2 }

// Estimate — оценка выгрузки под фильтрами q (§115.5). Частоту и слоты не
// расходует: форма зовёт её при каждой смене фильтра.
func (u *LogsExportUsecase) Estimate(ctx context.Context, nodeID, teamID string, q port.LogQuery) (port.LogExportStats, error) {
	_, q, err := u.prepare(ctx, nodeID, teamID, q)
	if err != nil {
		return port.LogExportStats{}, err
	}
	return u.d.Reader.ExportStats(ctx, q, u.BodyCapBytes())
}

// Export — выгрузка записей журнала в sink (§115.7, §115.8).
//
// Ошибка возвращается только до sink.Begin (синтаксис поиска, узел, занятость,
// частота, отказ ClickHouse при отборе ключей) — тогда в ответ ещё ничего не
// ушло. После Begin итог — в LogsExportSummary.Status, ошибка nil: заголовки
// уже отправлены, неполноту сообщает только файл. Аудит пишется на любой исход
// после Begin, в том числе прерванный.
func (u *LogsExportUsecase) Export(
	ctx context.Context, actor Actor, nodeID, teamID string,
	q port.LogQuery, opt LogsExportOptions, sink LogsExportSink,
) (LogsExportSummary, error) {
	n, q, err := u.prepare(ctx, nodeID, teamID, q)
	if err != nil {
		return LogsExportSummary{}, err
	}
	release, err := u.acquire(ctx, actor)
	if err != nil {
		return LogsExportSummary{}, err
	}
	defer release()

	meta := u.meta(n, q, actor, opt)
	keys, err := u.selectKeys(ctx, q, meta.Rows+1)
	if err != nil {
		return LogsExportSummary{}, err
	}
	meta.More = len(keys) > meta.Rows
	keys = keys[:min(len(keys), meta.Rows)]
	meta.Selected = len(keys)

	sum := u.stream(ctx, q, keys, meta, sink)
	u.d.Audit.Log(context.WithoutCancel(ctx), actor, domain.ActionNodeLogsExport, "node", n.ID, exportAuditDetails(meta, sum))
	return sum, nil
}

// prepare — разбор поиска, узел, таблица и снятие параметров, которые в
// выгрузке смысла не имеют (курсор страницы, лимит страницы, «Неудачные» §79.1).
// Автоокно §77 не применяется: предел выгрузки и top-N сами ограничивают чтение.
func (u *LogsExportUsecase) prepare(ctx context.Context, nodeID, teamID string, q port.LogQuery) (*domain.Node, port.LogQuery, error) {
	// Синтаксис поиска — до узла: 400 приоритетнее «логи не настроены».
	if err := parseSearch(&q); err != nil {
		return nil, q, err
	}
	n, err := resolveLogsNode(ctx, u.d.Nodes, nodeID, teamID)
	if err != nil {
		return nil, q, err
	}
	q.Table, q.NodeID = n.ClickHouseTable, n.ID
	setDateCreateAligned(&q, n, u.d.Logger)
	if q.Unresolved || q.BeforeID != "" || q.Limit != 0 {
		u.d.Logger.Debug("logs export: list-only parameters dropped",
			u.d.Logger.Str("node_id", n.ID),
			u.d.Logger.Any("unresolved", q.Unresolved),
			u.d.Logger.Any("cursor", q.BeforeID != ""),
			u.d.Logger.Int("limit", q.Limit))
	}
	q.Unresolved, q.BeforeID, q.Limit = false, "", 0
	return n, q, nil
}

// acquire занимает слот реплики, затем расходует частоту пользователя. Порядок
// важен: при занятой реплике nginx повторяет GET на соседней, и попытка, не
// получившая слота, не должна съедать лимит частоты.
func (u *LogsExportUsecase) acquire(ctx context.Context, actor Actor) (func(), error) {
	select {
	case u.slots <- struct{}{}:
	default:
		u.d.Logger.Debug("logs export: no free slot",
			u.d.Logger.Str("user_id", actor.UserID), u.d.Logger.Int("slots", cap(u.slots)))
		return nil, ErrLogsExportBusy
	}
	release := func() { <-u.slots }
	if err := u.allow(ctx, actor); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

// allow — лимит частоты на пользователя. Недоступный лимитер не блокирует
// выгрузку (fail-open, как у повтора §7.4.1).
func (u *LogsExportUsecase) allow(ctx context.Context, actor Actor) error {
	if u.d.Limiter == nil || u.d.Limits.RatePerMinute <= 0 {
		return nil
	}
	ok, err := u.d.Limiter.Allow(ctx, "logs_export:"+actor.UserID, u.d.Limits.RatePerMinute)
	if err != nil {
		u.d.Logger.Warn("logs export rate limit check failed; allowing",
			u.d.Logger.Str("user_id", actor.UserID), u.d.Logger.Err(err))
		return nil
	}
	if !ok {
		return ErrLogsExportRateLimited
	}
	return nil
}

// meta — параметры выгрузки к началу потока.
func (u *LogsExportUsecase) meta(n *domain.Node, q port.LogQuery, actor Actor, opt LogsExportOptions) LogsExportMeta {
	rows := opt.Rows
	if rows <= 0 || rows > u.d.Limits.MaxRows {
		rows = u.d.Limits.MaxRows
	}
	m := LogsExportMeta{
		Node: n, Query: q, Actor: actor, Rows: rows, Bodies: opt.Bodies,
		StartedAt: u.d.Clock.Now(), Limits: u.d.Limits,
	}
	if opt.Bodies {
		m.BodyChars = u.d.Limits.MaxBodyChars
	}
	return m
}

// queryCtx — контекст одного вызова ClickHouse с таймаутом выгрузки.
// clickhouse-go переводит дедлайн в max_execution_time запроса.
func (u *LogsExportUsecase) queryCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	if u.d.Limits.QueryTimeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, u.d.Limits.QueryTimeout)
}

// selectKeys — отбор ключей записей (§115.7, шаг 1).
func (u *LogsExportUsecase) selectKeys(ctx context.Context, q port.LogQuery, limit int) ([]port.LogExportKey, error) {
	cctx, cancel := u.queryCtx(ctx)
	defer cancel()
	return u.d.Reader.ExportKeys(cctx, q, limit)
}
