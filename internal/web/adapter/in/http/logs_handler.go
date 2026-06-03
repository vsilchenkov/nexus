package http

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"nexus/internal/domain"
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
	URL              string    `json:"url"`
	Method           string    `json:"method"`
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
}

func toLogDTO(r *domain.LogRecord) LogRecordDTO {
	return LogRecordDTO{
		ID:               r.ID,
		Type:             string(r.Type),
		URL:              r.URL,
		Method:           r.Method,
		Parameters:       r.Parameters,
		Request:          r.Request,
		Response:         r.Response,
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
}

// logQueryFromContext извлекает расширенные фильтры (§7.4, Phase 6.8) из
// query-параметров: from/to (RFC3339 или UnixMilli), ip, host, status,
// done, q. Используется и List, и Stream.
func logQueryFromContext(c *gin.Context) port.LogQuery {
	q := port.LogQuery{
		IP:     c.Query("ip"),
		Host:   c.Query("host"),
		Status: c.Query("status"), // "ok" / "err" / ""
		Done:   c.Query("done"),   // "yes" / "no" / ""
		Q:      c.Query("q"),
	}
	if v := c.Query("from"); v != "" {
		q.SinceMs = parseTimeMs(v)
	}
	if v := c.Query("to"); v != "" {
		q.UntilMs = parseTimeMs(v)
	}
	return q
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
// @Param    to        query  string  false  "конец диапазона (RFC3339 или UnixMilli)"
// @Param    ip        query  string  false  "exact match по IP клиента"
// @Param    host      query  string  false  "exact match по Host"
// @Param    status    query  string  false  "ok | err | (пусто)"
// @Param    done      query  string  false  "yes | no | (пусто)"
// @Param    q         query  string  false  "подстрока (case-insensitive) по url/request/response"
// @Success  200       {object}  map[string]any
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
		h.logger.ErrorWithOp("logs list failed", err, "logs.list",
			h.logger.Str("node_id", nodeID))
		localizedError(c, http.StatusInternalServerError, "error.internal")
		return
	}
	out := make([]LogRecordDTO, 0, len(recs))
	for _, r := range recs {
		out = append(out, toLogDTO(r))
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// Stream godoc
// @Summary  SSE live-tail логов узла (§7.4).
// @Description  Доступно только UI-сессиям (API-токены отклоняются — §7.14). Каждое новое событие приходит как SSE event "log".
// @Tags     logs
// @Produce  text/event-stream
// @Param    id  path  string  true  "node id"
// @Success  200  {string}  string  "event-stream"
// @Failure  403  {object}  map[string]string
// @Failure  404  {object}  map[string]string
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
		if errors.Is(err, domain.ErrNodeNotFound) {
			c.SSEvent("error", gin.H{"error": "node not found"})
			return
		}
		// Логирование для узла не настроено — штатное состояние, не сбой.
		if errors.Is(err, domain.ErrNodeLogsNotConfigured) {
			c.SSEvent("error", gin.H{"error": "logs not configured"})
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
			c.SSEvent("error", gin.H{"error": e.Error()})
			c.Writer.Flush()
			return
		case rec, ok := <-ch:
			if !ok {
				return
			}
			c.SSEvent("log", toLogDTO(rec))
			c.Writer.Flush()
		}
	}
}
