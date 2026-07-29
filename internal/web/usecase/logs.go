package usecase

import (
	"context"
	"errors"
	"time"

	"nexus/internal/domain"
	"nexus/internal/domain/logsearch"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/safego"
	"nexus/internal/web/usecase/port"
)

// LogsUsecase — чтение логов узлов из ClickHouse + SSE live-tail (§7.4 ТЗ).
type LogsUsecase struct {
	logs   port.LogReader
	nodes  port.NodeRepo
	logger logging.Logger

	pollInterval time.Duration
	streamLimit  int
}

func NewLogsUsecase(logs port.LogReader, nodes port.NodeRepo, logger logging.Logger) *LogsUsecase {
	return &LogsUsecase{
		logs:         logs,
		nodes:        nodes,
		logger:       logger,
		pollInterval: 1 * time.Second,
		streamLimit:  200,
	}
}

// ListSince — snapshot последних записей.
//
// sinceMs — Unix-миллисекунды; 0 = последние limit записей. limit — 1..500;
// дефолт 100.
//
// teamID — multi-tenancy scope (Phase 10.D); пустая строка пропускает
// проверку (legacy CLI/тесты). Чужой узел → ErrNodeNotFound.
func (u *LogsUsecase) ListSince(ctx context.Context, nodeID, teamID string, sinceMs int64, limit int) ([]*domain.LogRecord, error) {
	n, err := u.resolveNode(ctx, nodeID, teamID)
	if err != nil {
		return nil, err
	}
	return u.logs.ListSince(ctx, n.ClickHouseTable, n.ID, sinceMs, limit)
}

// Search — snapshot с расширенными фильтрами (Phase 6.8, §48).
// Подставляет n.ClickHouseTable в q.Table.
func (u *LogsUsecase) Search(ctx context.Context, nodeID, teamID string, q port.LogQuery) ([]*domain.LogRecord, error) {
	// §48: разбор q ДО резолва узла — синтаксическая ошибка (→400) приоритетнее
	// деградации «логи не настроены».
	if err := parseSearch(&q); err != nil {
		return nil, err
	}
	n, err := u.resolveNode(ctx, nodeID, teamID)
	if err != nil {
		return nil, err
	}
	q.Table = n.ClickHouseTable
	q.NodeID = n.ID
	u.applyDateCreateAligned(&q, n)
	return u.logs.Search(ctx, q)
}

// CountLogs — точное число записей под теми же фильтрами, что и Search (§67,
// счётчик «Показано N из M»). Тот же разбор Q и резолв узла — счётчик обязан
// считать ровно то, что показывает список.
func (u *LogsUsecase) CountLogs(ctx context.Context, nodeID, teamID string, q port.LogQuery) (uint64, error) {
	if err := parseSearch(&q); err != nil {
		return 0, err
	}
	n, err := u.resolveNode(ctx, nodeID, teamID)
	if err != nil {
		return 0, err
	}
	q.Table = n.ClickHouseTable
	q.NodeID = n.ID
	u.applyDateCreateAligned(&q, n)
	return u.logs.Count(ctx, q)
}

// applyDateCreateAligned разрешает адаптеру продублировать временное окно по
// date_create — колонке PARTITION BY таблицы логов (§72.4). Разрешение
// опирается на инвариант write-path Nexus (date_create == UTC-день
// date_request) и потому не действует для внешних таблиц §64: туда пишет
// посторонний сервис, а сужение по несогласованной колонке молча теряло бы
// записи.
func (u *LogsUsecase) applyDateCreateAligned(q *port.LogQuery, n *domain.Node) {
	q.DateCreateAligned = !n.ExternalTable
	if n.ExternalTable {
		u.logger.Debug("logs: date_create narrowing disabled for external table",
			u.logger.Str("node_id", n.ID), u.logger.Str("table", n.ClickHouseTable))
	}
}

// parseSearch — разбор сырого Q (мини-язык §48.1 либо RE2 в regex-режиме
// §48.2) в QExpr по флагам QCase/QWord/QRegex. Ошибка синтаксиса/regex —
// logsearch.ErrBadQuery (handler мапит на HTTP 400). Пустое Q → фильтр
// выключен.
func parseSearch(q *port.LogQuery) error {
	if q.Q == "" {
		q.QExpr = nil
		return nil
	}
	expr, err := logsearch.Parse(q.Q, logsearch.Options{
		CaseSensitive: q.QCase,
		WholeWord:     q.QWord,
		Regex:         q.QRegex,
	})
	if err != nil {
		return err
	}
	q.QExpr = expr
	return nil
}

// GetByID — одна запись лога целиком (включая тела request/response).
//
// Отдельный путь от ListSince/Search: списки возвращают только метаданные
// (без тел), а полное тело тянется лениво по клику на конкретную строку —
// иначе snapshot из сотен строк с большими JSON-телами вешает фронт (§7.4.1).
// Запись не найдена → domain.ErrNotFound.
func (u *LogsUsecase) GetByID(ctx context.Context, nodeID, teamID, logID string) (*domain.LogRecord, error) {
	n, err := u.resolveNode(ctx, nodeID, teamID)
	if err != nil {
		return nil, err
	}
	return u.logs.GetByID(ctx, n.ClickHouseTable, logID)
}

// GetByIDPreview — метаданные записи + ОГРАНИЧЕННОЕ превью тел (первые
// previewRunes рун request/response) + их полные длины в рунах (§42). Дефолтный
// путь разворачивания строки лога в UI: не тянет тела целиком, поэтому большой
// ответ не вешает фронт. Полное тело — лениво через GetBodyChunk.
func (u *LogsUsecase) GetByIDPreview(ctx context.Context, nodeID, teamID, logID string, previewRunes int) (*domain.LogRecord, int64, int64, error) {
	n, err := u.resolveNode(ctx, nodeID, teamID)
	if err != nil {
		return nil, 0, 0, err
	}
	return u.logs.GetByIDPreview(ctx, n.ClickHouseTable, logID, previewRunes)
}

// GetBodyChunk — срез одного тела (which = "request"|"response") записи по рунам
// + его полная длина (§42). Для постраничной подгрузки «показать весь» и
// потокового скачивания. teamID — scope.
func (u *LogsUsecase) GetBodyChunk(ctx context.Context, nodeID, teamID, logID, which string, offset, limit int) (string, int64, error) {
	n, err := u.resolveNode(ctx, nodeID, teamID)
	if err != nil {
		return "", 0, err
	}
	return u.logs.GetBodyChunk(ctx, n.ClickHouseTable, logID, which, offset, limit)
}

// CountFailed — число недоставленных записей узла (done=0) за окно (§35).
// Для KPI «неудачные доставки» на вкладке «Очередь». teamID — scope.
func (u *LogsUsecase) CountFailed(ctx context.Context, nodeID, teamID string, sinceMs, untilMs int64) (uint64, error) {
	n, err := u.resolveNode(ctx, nodeID, teamID)
	if err != nil {
		return 0, err
	}
	return u.logs.CountFailed(ctx, n.ClickHouseTable, n.ID, sinceMs, untilMs)
}

// Methods — уникальные значения колонки method узла (§48.3, фасет дропдауна
// Method в фильтре логов). Лениво дёргается UI при открытии списка — данные
// всегда свежие. teamID — scope.
func (u *LogsUsecase) Methods(ctx context.Context, nodeID, teamID string) ([]string, error) {
	n, err := u.resolveNode(ctx, nodeID, teamID)
	if err != nil {
		return nil, err
	}
	return u.logs.DistinctMethods(ctx, n.ClickHouseTable, n.ID, 0)
}

// ClientHosts — уникальные значения колонки client_host узла (§67, фасет
// дропдауна «Хост клиента»). Как Methods: лениво дёргается UI при открытии
// списка. Таблица без колонки (§64) — пустой список (деградация в адаптере).
func (u *LogsUsecase) ClientHosts(ctx context.Context, nodeID, teamID string) ([]string, error) {
	n, err := u.resolveNode(ctx, nodeID, teamID)
	if err != nil {
		return nil, err
	}
	return u.logs.DistinctClientHosts(ctx, n.ClickHouseTable, n.ID, 0)
}

// DateRange — min/max date_request узла в UnixMilli (§48.3, ограничение полей
// дат фильтра). (0, 0) — записей нет, ограничения не ставятся. teamID — scope.
func (u *LogsUsecase) DateRange(ctx context.Context, nodeID, teamID string) (int64, int64, error) {
	n, err := u.resolveNode(ctx, nodeID, teamID)
	if err != nil {
		return 0, 0, err
	}
	return u.logs.DateRange(ctx, n.ClickHouseTable, n.ID)
}

// resolveNode — общий путь: получить узел, проверить team scope, убедиться
// что у него настроен ClickHouseTable.
func (u *LogsUsecase) resolveNode(ctx context.Context, nodeID, teamID string) (*domain.Node, error) {
	n, err := u.nodes.Get(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	if teamID != "" && n.TeamID != teamID {
		return nil, domain.ErrNodeNotFound
	}
	if n.ClickHouseTable == "" {
		return nil, domain.ErrNodeLogsNotConfigured
	}
	// §43.1: имя таблицы кривого формата (напр. legacy-узел с дефисом) — CH-чтение
	// упало бы на «invalid table name» и флудило Sentry на каждом поллинге.
	// Трактуем как «логи не настроены»: мягкая деградация (200 + флаг), без ERROR.
	if !domain.IsValidCHTableName(n.ClickHouseTable) {
		return nil, domain.ErrNodeLogsNotConfigured
	}
	return n, nil
}

// logRecordOK — «доставлено успешно»: запись закрыта и внешний узел ответил
// 2xx/3xx (§72.1). Зеркало SQL-предиката condLogOK адаптера; фильтр «Ошибки» —
// его отрицание, поэтому ok и err вместе покрывают все записи без пересечения.
// Держать оба определения в одном виде обязательно: live-tail и snapshot иначе
// показывают разные наборы под одним и тем же фильтром.
func logRecordOK(r *domain.LogRecord) bool {
	return r.Done && r.Status >= 200 && r.Status < 400
}

// matchLogFilter — клиентский фильтр для live-tail. Совпадает по семантике
// с SQL-фильтром в LogReaderCH.Search, но применяется in-memory ко всем
// событиям перед отправкой клиенту (избегаем динамической перестройки
// polling-запроса при смене фильтра в UI). Полнотекстовая часть зеркалится
// через общий logsearch.Expr.Match — тот же AST, что у exprConds адаптера.
//
// §48.6: записи приходят из ListSince (listCols) с ПУСТЫМИ телами
// request/response — в live термы по телам не матчатся (безпрефиксный терм
// фактически ищет по url+parameters; req:/resp:-термы не совпадают никогда).
// Pre-existing ограничение §42; snapshot ищет по полным телам всегда.
func matchLogFilter(r *domain.LogRecord, q port.LogQuery) bool {
	if q.IP != "" && r.IP != q.IP {
		return false
	}
	if q.Host != "" && r.Host != q.Host {
		return false
	}
	if q.ClientHost != "" && r.ClientHost != q.ClientHost { // §67
		return false
	}
	if q.Method != "" && r.Method != q.Method {
		return false
	}
	switch q.Status {
	case "ok":
		if !logRecordOK(r) {
			return false
		}
	case "err":
		if logRecordOK(r) {
			return false
		}
	}
	switch q.Done {
	case "yes":
		if !r.Done {
			return false
		}
	case "no":
		if r.Done {
			return false
		}
	}
	if q.QExpr != nil && !q.QExpr.Match(r.URL, r.Parameters, r.Request, r.Response) {
		return false
	}
	return true
}

// Subscribe — SSE live-tail (§7.4 ТЗ). filter применяется in-memory
// к каждому событию перед отправкой клиенту (Phase 6.8) —
// SinceMs/UntilMs/Limit/Table из filter игнорируются (курсор сам управляется).
//
// Реализация — простой polling раз в pollInterval с курсором по date_request.
// Канал закрывается, когда ctx отменён. errCh передаёт фатальные ошибки
// (например, удалена таблица); после ошибки оба канала закрываются.
//
// Это не самая дешёвая реализация (каждый клиент = свой опрос ClickHouse),
// но для админок этого хватает. Долгосрочный путь — pub/sub через
// Kafka nexus.logs (out of scope в v1).
func (u *LogsUsecase) Subscribe(ctx context.Context, nodeID, teamID string, filter port.LogQuery) (<-chan *domain.LogRecord, <-chan error, error) {
	// §48: как в Search — сначала валидация поискового выражения (→400).
	if err := parseSearch(&filter); err != nil {
		return nil, nil, err
	}
	n, err := u.resolveNode(ctx, nodeID, teamID)
	if err != nil {
		return nil, nil, err
	}

	ch := make(chan *domain.LogRecord, 64)
	errCh := make(chan error, 1)
	cursor := time.Now().UnixMilli()

	go func() {
		defer close(ch)
		defer close(errCh)
		defer safego.Recover(u.logger, "web.logsLiveTail")
		tick := time.NewTicker(u.pollInterval)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				recs, err := u.logs.ListSince(ctx, n.ClickHouseTable, n.ID, cursor, u.streamLimit)
				if err != nil {
					if errors.Is(err, context.Canceled) {
						return
					}
					select {
					case errCh <- err:
					default:
					}
					return
				}
				for _, r := range recs {
					if ms := r.DateRequest.UnixMilli(); ms > cursor {
						cursor = ms
					}
					if !matchLogFilter(r, filter) {
						continue
					}
					select {
					case ch <- r:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()
	return ch, errCh, nil
}
