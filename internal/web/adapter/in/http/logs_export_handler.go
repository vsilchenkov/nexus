package http

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"nexus/internal/domain"
	"nexus/internal/domain/logsearch"
	"nexus/internal/platform/clientgone"
	"nexus/internal/platform/i18n"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
	"nexus/internal/web/usecase/port"
)

// logsExportService — операции выгрузки журнала узла (§115), нужные handler'у.
type logsExportService interface {
	Limits() usecase.LogsExportLimits
	Estimate(ctx context.Context, nodeID, teamID string, q port.LogQuery) (port.LogExportStats, error)
	Export(ctx context.Context, actor usecase.Actor, nodeID, teamID string,
		q port.LogQuery, opt usecase.LogsExportOptions, sink usecase.LogsExportSink) (usecase.LogsExportSummary, error)
}

// logsExportBusyRetrySec — Retry-After при занятых слотах реплики: выгрузка
// длится секунды-минуты, повтор раньше почти наверняка снова упрётся.
const logsExportBusyRetrySec = "30"

// LogsExportHandler — выгрузка журнала узла в Excel (§115.5). Оба маршрута —
// для всех ролей, только браузерная сессия (RequireSessionOnly в routes.go).
type LogsExportHandler struct {
	uc      logsExportService
	version string
	logger  logging.Logger
}

// NewLogsExportHandler — version пишется на лист «Параметры».
func NewLogsExportHandler(uc logsExportService, version string, logger logging.Logger) *LogsExportHandler {
	return &LogsExportHandler{uc: uc, version: version, logger: logger}
}

// LogsExportEstimateResponse — оценка выгрузки для формы (§115.5).
type LogsExportEstimateResponse struct {
	Records        uint64 `json:"records"`        // уникальные записи под фильтром
	Rows           uint64 `json:"rows"`           // строки-прогоны под фильтром
	RequestBytes   int64  `json:"request_bytes"`  // сумма размеров тел запроса, каждый урезан капом ячейки
	ResponseBytes  int64  `json:"response_bytes"` // то же для тел ответа
	MaxRows        int    `json:"max_rows"`
	MaxBytes       int64  `json:"max_bytes"`
	MaxBodyChars   int    `json:"max_body_chars"`
	LogsConfigured bool   `json:"logs_configured"`
	LogsAvailable  bool   `json:"logs_available"`
}

// Estimate godoc
// @Summary  Оценка выгрузки журнала узла в Excel (§115.5).
// @Description  Сколько записей и строк подходит под фильтры журнала и сколько весят тела (суммы request_size/response_size, каждый урезан капом ячейки — верхняя граница). Фильтры — те же, что у /logs. Плюс действующие лимиты выгрузки. Узел без таблицы → logs_configured=false, ClickHouse недоступен или таймаут 10 с → logs_available=false (оба — 200). Только session-cookie.
// @Tags     logs
// @Produce  json
// @Param    id           path   string  true   "node id"
// @Param    from         query  string  false  "нижняя граница date_request (RFC3339 или UnixMilli)"
// @Param    to           query  string  false  "верхняя граница date_request (RFC3339 или UnixMilli)"
// @Param    q            query  string  false  "поиск (§48)"
// @Param    q_case       query  bool    false  "регистрозависимый поиск"
// @Param    q_word       query  bool    false  "целое слово"
// @Param    q_regex      query  bool    false  "RE2-выражение"
// @Param    method       query  string  false  "подпуть запроса"
// @Param    status       query  string  false  "ok | err"
// @Param    done         query  string  false  "yes | no"
// @Param    ip           query  string  false  "IP клиента"
// @Param    client_host  query  string  false  "имя клиента (PTR)"
// @Success  200  {object}  LogsExportEstimateResponse
// @Failure  400  {object}  ErrorResponse  "синтаксис поиска"
// @Failure  404  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/nodes/{id}/logs/export/estimate [get]
func (h *LogsExportHandler) Estimate(c *gin.Context) {
	nodeID := c.Param("id")
	lim := h.uc.Limits()
	resp := LogsExportEstimateResponse{
		MaxRows: lim.MaxRows, MaxBytes: lim.MaxBytes, MaxBodyChars: lim.MaxBodyChars,
		LogsConfigured: true, LogsAvailable: true,
	}
	st, err := h.uc.Estimate(c.Request.Context(), nodeID, currentTeamID(c), logQueryFromContext(c))
	switch {
	case err == nil:
		resp.Records, resp.Rows, resp.RequestBytes, resp.ResponseBytes = st.Records, st.Rows, st.ReqBytes, st.RespBytes
	case errors.Is(err, logsearch.ErrBadQuery):
		localizedError(c, http.StatusBadRequest, "error.bad_search_query")
		return
	case errors.Is(err, domain.ErrNodeNotFound):
		localizedError(c, http.StatusNotFound, "node.not_found")
		return
	case errors.Is(err, domain.ErrNodeLogsNotConfigured):
		resp.LogsConfigured, resp.LogsAvailable = false, false
	case errors.Is(err, domain.ErrLogsBackendUnavailable), errors.Is(err, context.DeadlineExceeded):
		h.logger.Warn("logs export estimate degraded: clickhouse unavailable or slow",
			h.logger.Str("node_id", nodeID), h.logger.Err(err))
		resp.LogsAvailable = false
	case clientgone.Is(c.Request.Context(), err):
		return
	default:
		logServerError(c, h.logger, "logs export estimate failed", err, "logs.export.estimate",
			h.logger.Str("node_id", nodeID))
		localizedError(c, http.StatusInternalServerError, "error.internal")
		return
	}
	c.JSON(http.StatusOK, resp)
}

// Export godoc
// @Summary  Выгрузка журнала узла в Excel (§115.5).
// @Description  Файл .xlsx потоком: лист «Логи» — колонки таблицы ClickHouse (запись = новейший подходящий прогон, порядок как в журнале), лист «Параметры» — фильтры, лимиты и статус полноты. Неполнота после начала потока (ClickHouse отказал, исчерпан объём) видна только в файле. Фильтры — те же, что у /logs. Тела не маскируются; выгрузка пишется в аудит (node.logs_export). Только session-cookie.
// @Tags     logs
// @Produce  application/vnd.openxmlformats-officedocument.spreadsheetml.sheet
// @Param    id           path   string  true   "node id"
// @Param    rows         query  int     false  "предел записей (по умолчанию и сверх лимита — max_rows)"
// @Param    bodies       query  bool    false  "выгружать тела request/response"
// @Param    tz           query  string  false  "часовой пояс дат (IANA, напр. Europe/Moscow); по умолчанию UTC"
// @Param    from         query  string  false  "нижняя граница date_request (RFC3339 или UnixMilli)"
// @Param    to           query  string  false  "верхняя граница date_request (RFC3339 или UnixMilli)"
// @Param    q            query  string  false  "поиск (§48)"
// @Param    q_case       query  bool    false  "регистрозависимый поиск"
// @Param    q_word       query  bool    false  "целое слово"
// @Param    q_regex      query  bool    false  "RE2-выражение"
// @Param    method       query  string  false  "подпуть запроса"
// @Param    status       query  string  false  "ok | err"
// @Param    done         query  string  false  "yes | no"
// @Param    ip           query  string  false  "IP клиента"
// @Param    client_host  query  string  false  "имя клиента (PTR)"
// @Success  200  {file}    file
// @Failure  400  {object}  ErrorResponse  "синтаксис поиска"
// @Failure  404  {object}  ErrorResponse
// @Failure  429  {object}  ErrorResponse  "превышена частота запусков (Retry-After)"
// @Failure  503  {object}  ErrorResponse  "все слоты реплики заняты (Retry-After) или ClickHouse недоступен"
// @Security CookieAuth
// @Router   /api/nodes/{id}/logs/export.xlsx [get]
func (h *LogsExportHandler) Export(c *gin.Context) {
	nodeID := c.Param("id")
	rows, _ := strconv.Atoi(c.Query("rows")) // не число → 0 → потолок max_rows
	opt := usecase.LogsExportOptions{Rows: rows, Bodies: boolFlag(c.Query("bodies"))}
	sink := newXLSXLogSink(c, exportLocation(c.Query("tz"), h.logger), i18n.FromGin(c), h.version)

	sum, err := h.uc.Export(c.Request.Context(), actorFromCtx(c), nodeID, currentTeamID(c),
		logQueryFromContext(c), opt, sink)
	if err != nil {
		h.replyExportError(c, nodeID, err)
		return
	}
	h.logger.Debug("logs export finished",
		h.logger.Str("node_id", nodeID),
		h.logger.Str("status", string(sum.Status)),
		h.logger.Str("reason", sum.Reason),
		h.logger.Int("written", sum.Written),
		h.logger.Any("bytes", sum.Bytes),
		h.logger.Any("duration_ms", sum.FinishedAt.Sub(sink.startedAt()).Milliseconds()))
}

// replyExportError — отказ ДО начала потока (§115.5): ответ ещё не начат,
// поэтому обычный JSON.
func (h *LogsExportHandler) replyExportError(c *gin.Context, nodeID string, err error) {
	switch {
	case clientgone.Is(c.Request.Context(), err):
		return
	case errors.Is(err, logsearch.ErrBadQuery):
		localizedError(c, http.StatusBadRequest, "error.bad_search_query")
	case errors.Is(err, domain.ErrNodeNotFound), errors.Is(err, domain.ErrNodeLogsNotConfigured):
		localizedError(c, http.StatusNotFound, "node.not_found")
	case errors.Is(err, usecase.ErrLogsExportRateLimited):
		c.Header("Retry-After", "60")
		localizedError(c, http.StatusTooManyRequests, "error.rate_limited")
	case errors.Is(err, usecase.ErrLogsExportBusy):
		c.Header("Retry-After", logsExportBusyRetrySec)
		localizedError(c, http.StatusServiceUnavailable, "logs_export.busy")
	case errors.Is(err, domain.ErrLogsBackendUnavailable), errors.Is(err, context.DeadlineExceeded):
		h.logger.Warn("logs export refused: clickhouse unavailable or slow",
			h.logger.Str("node_id", nodeID), h.logger.Err(err))
		localizedError(c, http.StatusServiceUnavailable, "error.logs_unavailable")
	default:
		logServerError(c, h.logger, "logs export failed", err, "logs.export", h.logger.Str("node_id", nodeID))
		localizedError(c, http.StatusInternalServerError, "error.internal")
	}
}

// exportLocation — часовой пояс дат файла из IANA-имени браузера. Пусто или
// неизвестное имя → UTC (tzdata в образе есть; промах — Debug, не ошибка).
func exportLocation(name string, logger logging.Logger) *time.Location {
	if name == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		logger.Debug("logs export: unknown time zone, using UTC",
			logger.Str("tz", name), logger.Err(err))
		return time.UTC
	}
	return loc
}
