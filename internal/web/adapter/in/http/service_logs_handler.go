package http

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
)

// ServiceLogsHandler — консоль служебных логов трёх сервисов (§51.5,
// admin-only): GET /api/logs (JSON-хвост) + GET /api/logs/download
// (text/plain attachment). НЕ путать с логами узлов /api/nodes/:id/logs.
type ServiceLogsHandler struct {
	uc     *usecase.ServiceLogsUsecase
	logger logging.Logger
}

// NewServiceLogsHandler создаёт handler консоли служебных логов.
func NewServiceLogsHandler(uc *usecase.ServiceLogsUsecase, logger logging.Logger) *ServiceLogsHandler {
	return &ServiceLogsHandler{uc: uc, logger: logger}
}

// serviceLogsQuery разбирает общие query-параметры List/Download.
// service: "all" | csv из receiver|sender|web; limit: int; min_level: строка.
func serviceLogsQuery(c *gin.Context) (services []string, limit int, minLevel string, err error) {
	if raw := strings.TrimSpace(c.Query("service")); raw != "" {
		for s := range strings.SplitSeq(raw, ",") {
			if s = strings.TrimSpace(s); s != "" {
				services = append(services, s)
			}
		}
	}
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			return nil, 0, "", fmt.Errorf("limit must be an integer: %w", err)
		}
	}
	return services, limit, strings.TrimSpace(c.Query("min_level")), nil
}

// List godoc
// @Summary  Хвост служебных логов сервисов (§51.5).
// @Description  Свежие записи slog-логов Receiver/Sender/Web из Redis-колец nexus:logs:*, слитые по времени (новые первыми). Это логи самих сервисов, не логи запросов узлов.
// @Tags     service-logs
// @Produce  json
// @Param    service    query  string  false  "all (дефолт) | csv из receiver,sender,web"
// @Param    limit      query  int     false  "максимум записей (дефолт 500, кап 2000)"
// @Param    min_level  query  string  false  "минимальный уровень: debug|info|warn|error"
// @Success  200  {array}   domain.ServiceLogEntry
// @Failure  400  {object}  ErrorResponse
// @Failure  500  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/logs [get]
func (h *ServiceLogsHandler) List(c *gin.Context) {
	services, limit, minLevel, err := serviceLogsQuery(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	entries, err := h.uc.Tail(c.Request.Context(), services, limit, minLevel)
	if err != nil {
		if errors.Is(err, domain.ErrServiceLogInvalidService) || errors.Is(err, domain.ErrServiceLogInvalidLevel) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		h.logger.ErrorWithOp("service logs tail failed", err, "service_logs.list")
		localizedError(c, http.StatusInternalServerError, "error.internal")
		return
	}
	if entries == nil {
		entries = []domain.ServiceLogEntry{}
	}
	c.JSON(http.StatusOK, entries)
}

// Download godoc
// @Summary  Скачать служебные логи файлом (§51.5).
// @Description  Тот же хвост, что GET /api/logs, но text/plain attachment (nexus-logs-<ts>.log), строки в хронологическом порядке.
// @Tags     service-logs
// @Produce  plain
// @Param    service    query  string  false  "all (дефолт) | csv из receiver,sender,web"
// @Param    limit      query  int     false  "максимум записей (дефолт 500, кап 2000)"
// @Param    min_level  query  string  false  "минимальный уровень: debug|info|warn|error"
// @Success  200  {string}  string  "лог-файл"
// @Failure  400  {object}  ErrorResponse
// @Failure  500  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/logs/download [get]
func (h *ServiceLogsHandler) Download(c *gin.Context) {
	services, limit, minLevel, err := serviceLogsQuery(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	entries, err := h.uc.Tail(c.Request.Context(), services, limit, minLevel)
	if err != nil {
		if errors.Is(err, domain.ErrServiceLogInvalidService) || errors.Is(err, domain.ErrServiceLogInvalidLevel) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		h.logger.ErrorWithOp("service logs download failed", err, "service_logs.download")
		localizedError(c, http.StatusInternalServerError, "error.internal")
		return
	}

	svcPart := "all"
	if raw := strings.TrimSpace(c.Query("service")); raw != "" {
		svcPart = safeFilePart(strings.ReplaceAll(raw, ",", "-"))
	}
	filename := fmt.Sprintf("nexus-logs-%s-%s.log", svcPart, time.Now().UTC().Format("20060102-150405"))
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Cache-Control", "no-store")

	w := bufio.NewWriter(c.Writer)
	// Tail отдаёт свежие первыми; файл пишем в хронологическом порядке.
	for _, e := range slices.Backward(entries) {
		w.WriteString(FormatServiceLogLine(e)) //nolint:errcheck // ошибка всплывёт на Flush
		w.WriteByte('\n')                      //nolint:errcheck
	}
	if err := w.Flush(); err != nil {
		h.logger.Warn("service logs download flush failed", h.logger.Err(err))
	}
}

// FormatServiceLogLine — одна строка лог-файла: время, уровень, сервис,
// сообщение и compact-JSON атрибутов. Экспортирована как единый формат
// (тесты сверяют тело download с ним).
func FormatServiceLogLine(e domain.ServiceLogEntry) string {
	var b strings.Builder
	b.WriteString(e.TS.UTC().Format("2006-01-02 15:04:05.000"))
	b.WriteByte(' ')
	b.WriteString(strings.ToUpper(e.Level))
	b.WriteString(" [")
	b.WriteString(e.Service)
	b.WriteString("] ")
	b.WriteString(e.Msg)
	if len(e.Attrs) > 0 {
		if raw, err := json.Marshal(e.Attrs); err == nil {
			b.WriteByte(' ')
			b.Write(raw)
		}
	}
	return b.String()
}
