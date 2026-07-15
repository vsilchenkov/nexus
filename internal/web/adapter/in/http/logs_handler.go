package http

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"nexus/internal/domain"
	"nexus/internal/domain/logsearch"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
	"nexus/internal/web/usecase/port"
)

// LogsHandler — чтение логов узла из ClickHouse.
//
//	GET /api/nodes/{id}/logs         — snapshot со стандартной пагинацией.
//	GET /api/nodes/{id}/logs/stream  — SSE live-tail (§7.4).
type LogsHandler struct {
	uc     *usecase.LogsUsecase
	logger logging.Logger
}

func NewLogsHandler(uc *usecase.LogsUsecase, logger logging.Logger) *LogsHandler {
	return &LogsHandler{uc: uc, logger: logger}
}

// LogRecordDTO — JSON-форма строки лога. Поля совпадают с domain.LogRecord,
// но используют snake_case (как принято в API шины).
type LogRecordDTO struct {
	ID               string    `json:"id"`
	Type             string    `json:"type"`
	HTTPMethod       string    `json:"http_method"` // §39: HTTP-глагол (GET/POST/…)
	URL              string    `json:"url"`
	Method           string    `json:"method"` // §39: подпуть запроса (хвост passthrough)
	Parameters       string    `json:"parameters"`
	Request          string    `json:"request,omitempty"`
	Response         string    `json:"response,omitempty"`
	Status           int32     `json:"status"`
	Reason           string    `json:"reason,omitempty"`
	DateRequest      time.Time `json:"date_request"`
	DateResponse     time.Time `json:"date_response"`
	Duration         int32     `json:"duration_ms"`
	Done             bool      `json:"done"`
	ChecksumRequest  string    `json:"checksum_request,omitempty"`
	ChecksumResponse string    `json:"checksum_response,omitempty"`
	Host             string    `json:"host,omitempty"`
	IP               string    `json:"ip,omitempty"`
	Attempts         int32     `json:"attempts"`
	AttemptsDetails  string    `json:"attempts_details,omitempty"`

	// §42: полные длины тел в рунах. В Get поля request/response несут только
	// ПРЕВЬЮ (первые bodyPreviewRunes рун); если *_len > длины превью, на фронте
	// показывается «показать весь / скачать». В списках (List/Stream) не заданы.
	RequestLen  int64 `json:"request_len,omitempty"`
	ResponseLen int64 `json:"response_len,omitempty"`
}

const (
	// bodyPreviewRunes — сколько рун тела отдаём при первом разворачивании строки
	// лога (GET /log/:logId). Остальное — лениво через /body (§42).
	bodyPreviewRunes = 64 * 1024
	// bodyChunkMaxRunes — верхний кап на limit одного среза GET /body.
	bodyChunkMaxRunes = 4 * 1024 * 1024
	// bodyDownloadChunkRunes — размер среза при потоковом скачивании тела.
	bodyDownloadChunkRunes = 1 * 1024 * 1024
)

// toLogDTO маппит запись лога в JSON-DTO. includeBodies=false вырезает тяжёлые
// поля request/response (и parameters): списки/стрим отдают только метаданные,
// чтобы snapshot из сотен строк с большими телами не вешал фронт. Полные тела
// доступны через GET /api/nodes/{id}/log/{logId} лениво по клику (§7.4.1).
func toLogDTO(r *domain.LogRecord, includeBodies bool) LogRecordDTO {
	dto := LogRecordDTO{
		ID:               r.ID,
		Type:             string(r.Type),
		HTTPMethod:       r.HTTPMethod,
		URL:              r.URL,
		Method:           r.Method,
		Status:           r.Status,
		Reason:           r.Reason,
		DateRequest:      r.DateRequest,
		DateResponse:     r.DateResponse,
		Duration:         r.Duration,
		Done:             r.Done,
		ChecksumRequest:  r.ChecksumRequest,
		ChecksumResponse: r.ChecksumResponse,
		Host:             r.Host,
		IP:               r.IP,
		Attempts:         r.Attempts,
		AttemptsDetails:  r.AttemptsDetails,
	}
	if includeBodies {
		dto.Parameters = r.Parameters
		dto.Request = r.Request
		dto.Response = r.Response
	}
	return dto
}

// logQueryFromContext извлекает расширенные фильтры (§7.4, Phase 6.8; §48) из
// query-параметров: from/to (RFC3339 или UnixMilli), ip, host, status, done,
// q + режимы q_case/q_word/q_regex и method. Используется и List, и Stream.
// ip/host из UI убраны (§48.4), но параметры API сохранены для токен-клиентов.
func logQueryFromContext(c *gin.Context) port.LogQuery {
	q := port.LogQuery{
		IP:     c.Query("ip"),
		Host:   c.Query("host"),
		Status: c.Query("status"), // "ok" / "err" / ""
		Done:   c.Query("done"),   // "yes" / "no" / ""
		Method: c.Query("method"), // §48: exact match по колонке method (§39)
		Q:      c.Query("q"),
		QCase:  boolFlag(c.Query("q_case")),
		QWord:  boolFlag(c.Query("q_word")),
		QRegex: boolFlag(c.Query("q_regex")),
	}
	if v := c.Query("from"); v != "" {
		q.SinceMs = parseTimeMs(v)
	}
	if v := c.Query("to"); v != "" {
		q.UntilMs = parseTimeMs(v)
	}
	// §44/45-fix: тай-брейкер keyset-пагинации (ID самой старой строки прошлой
	// страницы) — вместе с to даёт строгий курсор (date_request, ID).
	q.BeforeID = c.Query("before_id")
	return q
}

// boolFlag — булев query-флаг: включён при "1" или "true" (§48).
func boolFlag(v string) bool {
	return v == "1" || v == "true"
}

// parseTimeMs — принимает либо RFC3339 ("2026-01-15T10:00:00Z"), либо
// UnixMilli как строку. Невалидное значение → 0 (фильтр отключён).
func parseTimeMs(v string) int64 {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.UnixMilli()
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return n
	}
	return 0
}

// List godoc
// @Summary  Snapshot логов узла из ClickHouse с фильтрами.
// @Tags     logs
// @Produce  json
// @Param    id        path   string  true   "node id"
// @Param    since_ms  query  int     false  "legacy cursor по date_request (UnixMilli); если задан — фильтры from/to/ip/host/status/done/q игнорируются"
// @Param    limit     query  int     false  "1..500, default 100"
// @Param    from      query  string  false  "начало диапазона (RFC3339 или UnixMilli)"
// @Param    to        query  string  false  "конец диапазона (RFC3339 или UnixMilli); для keyset-пагинации — date_request самой старой загруженной строки"
// @Param    before_id query  string  false  "тай-брейкер keyset-пагинации: id самой старой загруженной строки (вместе с to — строгий курсор по плотным секундам)"
// @Param    ip        query  string  false  "exact match по IP клиента (с §48 недоступно из UI, параметр API сохранён)"
// @Param    host      query  string  false  "exact match по Host (с §48 недоступно из UI, параметр API сохранён)"
// @Param    status    query  string  false  "ok | err | (пусто)"
// @Param    done      query  string  false  "yes | no | (пусто)"
// @Param    method    query  string  false  "exact match по подпути запроса (колонка method, §39/§48)"
// @Param    q         query  string  false  "поиск по url/parameters/request/response (§48.1): & — И, | — ИЛИ, -терм — НЕ, префиксы url:/params:/req:/resp: скоупят на колонку, \\ экранирует спецсимволы; в regex-режиме весь q — одно RE2"
// @Param    q_case    query  bool    false  "1/true — с учётом регистра (§48.2)"
// @Param    q_word    query  bool    false  "1/true — только целое слово (§48.2)"
// @Param    q_regex   query  bool    false  "1/true — q как RE2-выражение, мини-язык отключён (§48.2)"
// @Success  200       {object}  ListLogsResponse
// @Failure  400       {object}  ErrorResponse  "невалидный поисковый запрос (синтаксис/regex)"
// @Security CookieAuth
// @Security ApiTokenAuth
// @Router   /api/nodes/{id}/logs [get]
func (h *LogsHandler) List(c *gin.Context) {
	nodeID := c.Param("id")
	team := currentTeamID(c)
	limit, _ := strconv.Atoi(c.Query("limit"))

	var (
		recs []*domain.LogRecord
		err  error
	)
	// Если задан since_ms — legacy-режим (без расширенных фильтров) для
	// SSE-cursor'а и обратной совместимости.
	if v := c.Query("since_ms"); v != "" {
		since, _ := strconv.ParseInt(v, 10, 64)
		recs, err = h.uc.ListSince(c.Request.Context(), nodeID, team, since, limit)
	} else {
		q := logQueryFromContext(c)
		q.Limit = limit
		recs, err = h.uc.Search(c.Request.Context(), nodeID, team, q)
	}
	if err != nil {
		// §48: синтаксическая ошибка мини-языка / невалидный RE2 — вина запроса,
		// не сервера: 400 с локализованным сообщением, без ERROR-лога.
		if errors.Is(err, logsearch.ErrBadQuery) {
			localizedError(c, http.StatusBadRequest, "error.bad_search_query")
			return
		}
		if errors.Is(err, domain.ErrNodeNotFound) {
			localizedError(c, http.StatusNotFound, "node.not_found")
			return
		}
		// Узел без clickhouse_table — логирование не настроено. Это штатное
		// состояние (таблица опциональна, провижинится только по шаблону),
		// а не сбой: отдаём пустой список с флагом logs_configured=false,
		// без ERR-лога и без 500 (иначе поллинг UI спамит ошибками).
		if errors.Is(err, domain.ErrNodeLogsNotConfigured) {
			c.JSON(http.StatusOK, gin.H{"items": []LogRecordDTO{}, "logs_configured": false})
			return
		}
		// ClickHouse временно недоступен (сеть/таймаут/сервер лёг) — мягкая
		// деградация, как у метрик: 200 + logs_available=false и WARN-лог
		// (не ERROR). Иначе поллинг UI сыпал бы 500 и флудил Sentry, скрывая
		// реальные write-path ошибки CH (см. domain.ErrLogsBackendUnavailable).
		if errors.Is(err, domain.ErrLogsBackendUnavailable) {
			h.logger.Warn("logs list degraded: clickhouse unavailable",
				h.logger.Str("node_id", nodeID), h.logger.Err(err))
			c.JSON(http.StatusOK, gin.H{"items": []LogRecordDTO{}, "logs_available": false})
			return
		}
		h.logger.ErrorWithOp("logs list failed", err, "logs.list",
			h.logger.Str("node_id", nodeID))
		localizedError(c, http.StatusInternalServerError, "error.internal")
		return
	}
	out := make([]LogRecordDTO, 0, len(recs))
	for _, r := range recs {
		out = append(out, toLogDTO(r, false))
	}
	c.JSON(http.StatusOK, gin.H{"items": out, "logs_available": true})
}

// CountFailed godoc
// @Summary  Число недоставленных записей узла (done=0) за период (§35).
// @Description  Дешёвый count из ClickHouse для KPI «неудачные доставки» на вкладке «Очередь». Узел без clickhouse_table → 200 + logs_configured=false, count=0.
// @Tags     logs
// @Produce  json
// @Param    id    path   string  true   "node id"
// @Param    from  query  string  false  "начало диапазона (RFC3339 или UnixMilli)"
// @Param    to    query  string  false  "конец диапазона"
// @Success  200   {object}  FailedCountResponse
// @Security CookieAuth
// @Security ApiTokenAuth
// @Router   /api/nodes/{id}/logs/failed-count [get]
func (h *LogsHandler) CountFailed(c *gin.Context) {
	nodeID := c.Param("id")
	var sinceMs, untilMs int64
	if v := c.Query("from"); v != "" {
		sinceMs = parseTimeMs(v)
	}
	if v := c.Query("to"); v != "" {
		untilMs = parseTimeMs(v)
	}
	n, err := h.uc.CountFailed(c.Request.Context(), nodeID, currentTeamID(c), sinceMs, untilMs)
	if err != nil {
		if errors.Is(err, domain.ErrNodeNotFound) {
			localizedError(c, http.StatusNotFound, "node.not_found")
			return
		}
		// Логирование не настроено — штатно (как List): 200 + флаг, без 500/спама.
		if errors.Is(err, domain.ErrNodeLogsNotConfigured) {
			c.JSON(http.StatusOK, gin.H{"count": 0, "logs_configured": false})
			return
		}
		// ClickHouse недоступен — мягкая деградация (см. List): 200 +
		// logs_available=false, WARN-лог. KPI «неудачные доставки» на вкладке
		// «Очередь» покажет «—», а не «0» и не сорвётся в 500-поллинг.
		if errors.Is(err, domain.ErrLogsBackendUnavailable) {
			h.logger.Warn("logs failed-count degraded: clickhouse unavailable",
				h.logger.Str("node_id", nodeID), h.logger.Err(err))
			c.JSON(http.StatusOK, gin.H{"count": 0, "logs_configured": true, "logs_available": false})
			return
		}
		h.logger.ErrorWithOp("logs failed-count failed", err, "logs.failed_count",
			h.logger.Str("node_id", nodeID))
		localizedError(c, http.StatusInternalServerError, "error.internal")
		return
	}
	c.JSON(http.StatusOK, gin.H{"count": n, "logs_configured": true, "logs_available": true})
}

// Methods godoc
// @Summary  Уникальные значения method узла для фасета фильтра (§48.3).
// @Description  DISTINCT по колонке method (подпуть запроса, §39) для дропдауна Method. Дёргается лениво при открытии списка — всегда свежие данные. До 200 значений, отсортированы. Узел без clickhouse_table → 200 + logs_configured=false; CH недоступен → 200 + logs_available=false.
// @Tags     logs
// @Produce  json
// @Param    id  path  string  true  "node id"
// @Success  200  {object}  LogMethodsResponse
// @Failure  404  {object}  ErrorResponse
// @Security CookieAuth
// @Security ApiTokenAuth
// @Router   /api/nodes/{id}/logs/methods [get]
func (h *LogsHandler) Methods(c *gin.Context) {
	nodeID := c.Param("id")
	items, err := h.uc.Methods(c.Request.Context(), nodeID, currentTeamID(c))
	if h.writeFacetError(c, nodeID, "logs.methods", err, gin.H{"items": []string{}}) {
		return
	}
	if items == nil {
		items = []string{}
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "logs_configured": true, "logs_available": true})
}

// DateRange godoc
// @Summary  Диапазон дат логов узла (min/max date_request, UnixMilli) (§48.3).
// @Description  Фасет для ограничения полей дат фильтра (атрибуты min/max). Дёргается лениво при фокусе поля даты — всегда свежие данные (логи прибывают, пока страница открыта). Нет записей → min_ms=0, max_ms=0 (ограничения не ставятся). Деградация как у /logs/methods.
// @Tags     logs
// @Produce  json
// @Param    id  path  string  true  "node id"
// @Success  200  {object}  LogDateRangeResponse
// @Failure  404  {object}  ErrorResponse
// @Security CookieAuth
// @Security ApiTokenAuth
// @Router   /api/nodes/{id}/logs/date-range [get]
func (h *LogsHandler) DateRange(c *gin.Context) {
	nodeID := c.Param("id")
	minMs, maxMs, err := h.uc.DateRange(c.Request.Context(), nodeID, currentTeamID(c))
	if h.writeFacetError(c, nodeID, "logs.date_range", err, gin.H{"min_ms": 0, "max_ms": 0}) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"min_ms": minMs, "max_ms": maxMs, "logs_configured": true, "logs_available": true})
}

// writeFacetError — общий маппинг ошибок фасет-эндпоинтов §48 (Methods/
// DateRange) в HTTP: 404 по узлу, мягкая деградация «не настроено» /
// «CH недоступен» (200 + флаги + пустой payload), 500 на прочее. Возвращает
// true, если ответ уже отправлен.
func (h *LogsHandler) writeFacetError(c *gin.Context, nodeID, op string, err error, empty gin.H) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, domain.ErrNodeNotFound):
		localizedError(c, http.StatusNotFound, "node.not_found")
	case errors.Is(err, domain.ErrNodeLogsNotConfigured):
		payload := gin.H{"logs_configured": false, "logs_available": false}
		maps.Copy(payload, empty)
		c.JSON(http.StatusOK, payload)
	case errors.Is(err, domain.ErrLogsBackendUnavailable):
		h.logger.Warn("logs facet degraded: clickhouse unavailable",
			h.logger.Str("node_id", nodeID), h.logger.Err(err))
		payload := gin.H{"logs_configured": true, "logs_available": false}
		maps.Copy(payload, empty)
		c.JSON(http.StatusOK, payload)
	default:
		h.logger.ErrorWithOp("logs facet failed", err, op, h.logger.Str("node_id", nodeID))
		localizedError(c, http.StatusInternalServerError, "error.internal")
	}
	return true
}

// Get godoc
// @Summary  Одна запись лога целиком (с телами request/response).
// @Description  Тела грузятся лениво по клику на строку — списки (List/Stream) их не возвращают, чтобы snapshot из сотен строк с большими JSON не вешал фронт (§7.4.1).
// @Tags     logs
// @Produce  json
// @Param    id     path  string  true  "node id"
// @Param    logId  path  string  true  "log record id"
// @Success  200    {object}  LogRecordDTO
// @Failure  404    {object}  ErrorResponse
// @Security CookieAuth
// @Security ApiTokenAuth
// @Router   /api/nodes/{id}/log/{logId} [get]
func (h *LogsHandler) Get(c *gin.Context) {
	nodeID := c.Param("id")
	logID := c.Param("logId")
	// §42: только ПРЕВЬЮ тел (первые bodyPreviewRunes рун) + полные длины —
	// большой ответ больше не тянется целиком и не вешает фронт. Полное тело —
	// лениво через GET /body. Replay по-прежнему читает полное тело (GetByID).
	rec, reqLen, respLen, err := h.uc.GetByIDPreview(c.Request.Context(), nodeID, currentTeamID(c), logID, bodyPreviewRunes)
	if h.writeLogReadError(c, nodeID, "logs.get", err) {
		return
	}
	dto := toLogDTO(rec, true)
	dto.RequestLen = reqLen
	dto.ResponseLen = respLen
	c.JSON(http.StatusOK, dto)
}

// writeLogReadError мапит ошибку чтения лога из ClickHouse в HTTP-ответ (общая
// для Get/GetBody/GetBodyDownload). Возвращает true, если ошибка обработана и
// ответ отправлен. CH-недоступность → 503 + WARN (не ERROR/500): тела тянутся
// лениво по клику, временная недоступность не должна флудить Sentry.
func (h *LogsHandler) writeLogReadError(c *gin.Context, nodeID, op string, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, domain.ErrNodeNotFound), errors.Is(err, domain.ErrNotFound),
		errors.Is(err, domain.ErrNodeLogsNotConfigured):
		localizedError(c, http.StatusNotFound, "node.not_found")
	case errors.Is(err, domain.ErrLogsBackendUnavailable):
		h.logger.Warn("log read degraded: clickhouse unavailable",
			h.logger.Str("node_id", nodeID), h.logger.Err(err))
		localizedError(c, http.StatusServiceUnavailable, "error.logs_unavailable")
	default:
		h.logger.ErrorWithOp("log read failed", err, op, h.logger.Str("node_id", nodeID))
		localizedError(c, http.StatusInternalServerError, "error.internal")
	}
	return true
}

// GetBody godoc
// @Summary  Срез тела (request|response) одной записи лога по рунам (§42).
// @Description  Для постраничной подгрузки «показать весь»: возвращает срез тела и его полную длину в рунах. which обязателен (request|response); offset/limit — в рунах (limit капится).
// @Tags     logs
// @Produce  json
// @Param    id      path   string  true   "node id"
// @Param    logId   path   string  true   "log record id"
// @Param    which   query  string  true   "request | response"
// @Param    offset  query  int     false  "смещение в рунах (default 0)"
// @Param    limit   query  int     false  "сколько рун вернуть (default 65536, max 4194304)"
// @Success  200     {object}  LogBodyChunkResponse
// @Failure  400     {object}  ErrorResponse
// @Failure  404     {object}  ErrorResponse
// @Security CookieAuth
// @Security ApiTokenAuth
// @Router   /api/nodes/{id}/log/{logId}/body [get]
func (h *LogsHandler) GetBody(c *gin.Context) {
	nodeID := c.Param("id")
	logID := c.Param("logId")
	which := c.Query("which")
	if which != "request" && which != "response" {
		localizedError(c, http.StatusBadRequest, "error.bad_request")
		return
	}
	offset, _ := strconv.Atoi(c.Query("offset"))
	if offset < 0 {
		offset = 0
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	if limit <= 0 {
		limit = bodyPreviewRunes
	}
	if limit > bodyChunkMaxRunes {
		limit = bodyChunkMaxRunes
	}
	chunk, total, err := h.uc.GetBodyChunk(c.Request.Context(), nodeID, currentTeamID(c), logID, which, offset, limit)
	if h.writeLogReadError(c, nodeID, "logs.body", err) {
		return
	}
	returned := len([]rune(chunk))
	c.JSON(http.StatusOK, gin.H{
		"which":    which,
		"total":    total,
		"offset":   offset,
		"returned": returned,
		"chunk":    chunk,
		"eof":      int64(offset+returned) >= total,
	})
}

// GetBodyDownload godoc
// @Summary  Скачать тело (request|response) записи лога целиком файлом (§42).
// @Description  Стримит полное тело как text/plain attachment, нарезая по рунам — память на сервере ограничена размером среза. which обязателен.
// @Tags     logs
// @Produce  plain
// @Param    id     path   string  true   "node id"
// @Param    logId  path   string  true   "log record id"
// @Param    which  query  string  true   "request | response"
// @Success  200    {string}  string  "body stream"
// @Failure  400    {object}  ErrorResponse
// @Failure  404    {object}  ErrorResponse
// @Security CookieAuth
// @Security ApiTokenAuth
// @Router   /api/nodes/{id}/log/{logId}/body/download [get]
func (h *LogsHandler) GetBodyDownload(c *gin.Context) {
	nodeID := c.Param("id")
	logID := c.Param("logId")
	which := c.Query("which")
	if which != "request" && which != "response" {
		localizedError(c, http.StatusBadRequest, "error.bad_request")
		return
	}
	ctx := c.Request.Context()
	team := currentTeamID(c)

	// Первый срез фетчим ДО отправки заголовков: он отдаёт total и валидирует
	// запись, чтобы ошибку (404/503) вернуть нормальным JSON, а не битым стримом.
	first, total, err := h.uc.GetBodyChunk(ctx, nodeID, team, logID, which, 0, bodyDownloadChunkRunes)
	if h.writeLogReadError(c, nodeID, "logs.body.download", err) {
		return
	}

	filename := fmt.Sprintf("%s-%s.txt", which, safeFilePart(logID))
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	c.Header("X-Content-Type-Options", "nosniff")

	w := c.Writer
	if _, err := w.WriteString(first); err != nil {
		return
	}
	w.Flush()
	offset := len([]rune(first))
	for int64(offset) < total {
		chunk, _, err := h.uc.GetBodyChunk(ctx, nodeID, team, logID, which, offset, bodyDownloadChunkRunes)
		if err != nil {
			// Заголовки уже ушли — корректный JSON-ответ невозможен, обрываем
			// поток и логируем (WARN: частая причина — клиент закрыл соединение).
			h.logger.Warn("log body download interrupted",
				h.logger.Str("node_id", nodeID), h.logger.Err(err))
			return
		}
		n := len([]rune(chunk))
		if n == 0 {
			break
		}
		if _, err := w.WriteString(chunk); err != nil {
			return
		}
		w.Flush()
		offset += n
	}
}

// safeFilePart оставляет только безопасные для имени файла и HTTP-заголовка
// символы [A-Za-z0-9._-] — защита от header-инъекции в Content-Disposition.
func safeFilePart(s string) string {
	b := make([]rune, 0, len(s))
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.':
			b = append(b, c)
		}
	}
	if len(b) == 0 {
		return "body"
	}
	return string(b)
}

// Stream godoc
// @Summary  SSE live-tail логов узла (§7.4).
// @Description  Доступно только UI-сессиям (API-токены отклоняются — §7.14). Каждое новое событие приходит как SSE event "log". Принимает те же фильтры, что и List (q/q_case/q_word/q_regex/method/status/done/ip/host, §48); термы по телам request/response в live не матчатся — тела в потоке не читаются (§48.6). Невалидный q → SSE event "error".
// @Tags     logs
// @Produce  text/event-stream
// @Param    id       path   string  true   "node id"
// @Param    method   query  string  false  "exact match по подпути запроса (§48)"
// @Param    q        query  string  false  "поисковое выражение (§48.1); в live ищет по url+parameters"
// @Param    q_case   query  bool    false  "1/true — с учётом регистра"
// @Param    q_word   query  bool    false  "1/true — только целое слово"
// @Param    q_regex  query  bool    false  "1/true — q как RE2"
// @Success  200  {string}  string  "event-stream"
// @Failure  403  {object}  ErrorResponse
// @Failure  404  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/nodes/{id}/logs/stream [get]
func (h *LogsHandler) Stream(c *gin.Context) {
	nodeID := c.Param("id")

	// SSE-заголовки.
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no") // отключаем буферизацию nginx
	c.Writer.Flush()

	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()

	filter := logQueryFromContext(c)
	ch, errCh, err := h.uc.Subscribe(ctx, nodeID, currentTeamID(c), filter)
	if err != nil {
		// §48: EventSource не читает тело 4xx — ошибка запроса уходит SSE-событием.
		// UI всегда сначала делает snapshot (List) с теми же параметрами и покажет
		// нормальную 400 оттуда.
		if errors.Is(err, logsearch.ErrBadQuery) {
			c.SSEvent("error", gin.H{"error": "bad search query"})
			return
		}
		if errors.Is(err, domain.ErrNodeNotFound) {
			c.SSEvent("error", gin.H{"error": "node not found"})
			return
		}
		// Логирование для узла не настроено — штатное состояние, не сбой.
		if errors.Is(err, domain.ErrNodeLogsNotConfigured) {
			c.SSEvent("error", gin.H{"error": "logs not configured"})
			return
		}
		// CH недоступен — мягкая деградация: отдельное событие logs_unavailable
		// и WARN (не ERROR), чтобы live-tail не флудил Sentry при простое CH.
		if errors.Is(err, domain.ErrLogsBackendUnavailable) {
			h.logger.Warn("logs stream degraded: clickhouse unavailable",
				h.logger.Str("node_id", nodeID), h.logger.Err(err))
			c.SSEvent("logs_unavailable", gin.H{"logs_available": false})
			return
		}
		h.logger.ErrorWithOp("logs subscribe failed", err, "logs.stream",
			h.logger.Str("node_id", nodeID))
		c.SSEvent("error", gin.H{"error": "internal error"})
		return
	}

	// Heartbeat-таймер — раз в 15 сек шлёт comment-keepalive, чтобы прокси
	// не закрывали idle соединение.
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			_, _ = fmt.Fprintf(c.Writer, ": ping\n\n")
			c.Writer.Flush()
		case e, ok := <-errCh:
			if !ok {
				return
			}
			// CH отвалился во время live-tail — мягкое событие, без ERROR/Sentry.
			if errors.Is(e, domain.ErrLogsBackendUnavailable) {
				h.logger.Warn("logs stream degraded mid-tail: clickhouse unavailable",
					h.logger.Str("node_id", nodeID), h.logger.Err(e))
				c.SSEvent("logs_unavailable", gin.H{"logs_available": false})
				c.Writer.Flush()
				return
			}
			c.SSEvent("error", gin.H{"error": e.Error()})
			c.Writer.Flush()
			return
		case rec, ok := <-ch:
			if !ok {
				return
			}
			c.SSEvent("log", toLogDTO(rec, false))
			c.Writer.Flush()
		}
	}
}
