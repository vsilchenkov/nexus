package http

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
	"nexus/internal/web/usecase/port"
)

// AuditHandler — GET /api/audit (admin only).
type AuditHandler struct {
	uc     *usecase.AuditUsecase
	logger logging.Logger
}

func NewAuditHandler(uc *usecase.AuditUsecase, logger logging.Logger) *AuditHandler {
	return &AuditHandler{uc: uc, logger: logger}
}

type auditEntryResponse struct {
	ID         string         `json:"id"`
	UserID     string         `json:"user_id,omitempty"`
	UserLogin  string         `json:"user_login"`
	TeamID     string         `json:"team_id,omitempty"`
	Action     string         `json:"action"`
	TargetType string         `json:"target_type,omitempty"`
	TargetID   string         `json:"target_id,omitempty"`
	Details    map[string]any `json:"details"`
	IPAddress  string         `json:"ip_address,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
}

func toAuditResp(e *domain.AuditEntry) auditEntryResponse {
	return auditEntryResponse{
		ID: e.ID, UserID: e.UserID, UserLogin: e.UserLogin, TeamID: e.TeamID,
		Action: e.Action, TargetType: e.TargetType, TargetID: e.TargetID,
		Details: e.Details, IPAddress: e.IPAddress, CreatedAt: e.CreatedAt,
	}
}

// auditFilterFromQuery собирает фильтр из query-параметров. defaultLimit —
// значение по умолчанию для Limit (0 = без него). maxLimit — верхняя планка.
//
// TeamID по умолчанию — current_team_id из сессии (multi-tenancy v2,
// Phase 10.F.1). Чтобы посмотреть глобальный аудит, admin может передать
// ?team_id=* (или передать другой UUID — admin'у доверяем).
func auditFilterFromQuery(c *gin.Context, defaultLimit, maxLimit int) port.AuditFilter {
	teamScope := currentTeamID(c)
	if v := c.Query("team_id"); v != "" {
		if v == "*" {
			teamScope = ""
		} else {
			teamScope = v
		}
	}
	f := port.AuditFilter{
		UserID:     c.Query("user_id"),
		TeamID:     teamScope,
		TargetType: c.Query("target_type"),
		TargetID:   c.Query("target_id"),
	}
	if v := c.Query("action"); v != "" && c.Query("actions") == "" {
		f.Actions = []string{v}
	}
	if v := c.Query("actions"); v != "" {
		f.Actions = splitCSV(v)
	}
	if v := c.Query("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.From = &t
		}
	}
	if v := c.Query("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.To = &t
		}
	}
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.Limit = n
		}
	}
	if f.Limit <= 0 {
		f.Limit = defaultLimit
	}
	if maxLimit > 0 && f.Limit > maxLimit {
		f.Limit = maxLimit
	}
	if v := c.Query("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.Offset = n
		}
	}
	return f
}

// List godoc
// @Summary  Журнал audit-log (admin only, §7.13).
// @Description  Поддерживаемые actions: node.create/update/delete, user.create/update/delete/password_changed, token.create/revoke/delete, login.success/failure, ch_table.drop, settings.update и др.
// @Tags     audit
// @Produce  json
// @Param    user_id      query  string  false  "фильтр по user_id"
// @Param    action       query  string  false  "одно значение action"
// @Param    actions      query  string  false  "несколько action через запятую"
// @Param    target_type  query  string  false  "node|user|token|ch_table|..."
// @Param    target_id    query  string  false  "фильтр по target_id"
// @Param    from         query  string  false  "RFC3339 (начало)"
// @Param    to           query  string  false  "RFC3339 (конец)"
// @Param    limit        query  int     false  "default 100, max 1000"
// @Param    offset       query  int     false  "смещение"
// @Success  200          {object}  map[string]any
// @Failure  500          {object}  map[string]string
// @Security CookieAuth
// @Security ApiTokenAuth
// @Router   /api/audit [get]
func (h *AuditHandler) List(c *gin.Context) {
	f := auditFilterFromQuery(c, 100, 1000)

	entries, err := h.uc.List(c.Request.Context(), f)
	if err != nil {
		h.logger.ErrorWithOp("audit list failed", err, "audit.list")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	out := make([]auditEntryResponse, 0, len(entries))
	for _, e := range entries {
		out = append(out, toAuditResp(e))
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// ExportCSV godoc
// @Summary  Выгрузка audit-log в CSV (admin only, §7.13).
// @Description  Тот же набор фильтров, что у /api/audit. Default limit 10000, max 50000. CSV в UTF-8 с BOM (для Excel), 9 колонок: id, created_at, user_login, user_id, action, target_type, target_id, ip_address, details (JSON).
// @Tags     audit
// @Produce  text/csv
// @Param    user_id      query  string  false  "фильтр по user_id"
// @Param    action       query  string  false  "одно значение action"
// @Param    actions      query  string  false  "несколько action через запятую"
// @Param    target_type  query  string  false  "node|user|token|ch_table|..."
// @Param    target_id    query  string  false  "фильтр по target_id"
// @Param    from         query  string  false  "RFC3339 (начало)"
// @Param    to           query  string  false  "RFC3339 (конец)"
// @Param    limit        query  int     false  "default 10000, max 50000"
// @Param    offset       query  int     false  "смещение"
// @Success  200          {string}  string  "CSV"
// @Failure  500          {object}  map[string]string
// @Security CookieAuth
// @Security ApiTokenAuth
// @Router   /api/audit/export.csv [get]
func (h *AuditHandler) ExportCSV(c *gin.Context) {
	f := auditFilterFromQuery(c, 10000, 50000)

	entries, err := h.uc.List(c.Request.Context(), f)
	if err != nil {
		h.logger.ErrorWithOp("audit export failed", err, "audit.export")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	fname := fmt.Sprintf("audit-%s.csv", time.Now().UTC().Format("20060102-150405"))
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, fname))
	c.Header("Cache-Control", "no-store")

	w := csv.NewWriter(c.Writer)
	defer w.Flush()

	// BOM для корректного открытия в Excel (UTF-8).
	if _, err := c.Writer.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
		h.logger.ErrorWithOp("audit export bom failed", err, "audit.export")
		return
	}
	header := []string{
		"id", "created_at", "user_login", "user_id", "team_id", "action",
		"target_type", "target_id", "ip_address", "details",
	}
	if err := w.Write(header); err != nil {
		h.logger.ErrorWithOp("audit export header failed", err, "audit.export")
		return
	}
	for _, e := range entries {
		details := ""
		if len(e.Details) > 0 {
			if b, mErr := json.Marshal(e.Details); mErr == nil {
				details = string(b)
			}
		}
		row := []string{
			e.ID,
			e.CreatedAt.Format(time.RFC3339Nano),
			e.UserLogin,
			e.UserID,
			e.TeamID,
			e.Action,
			e.TargetType,
			e.TargetID,
			e.IPAddress,
			details,
		}
		if err := w.Write(row); err != nil {
			h.logger.ErrorWithOp("audit export row failed", err, "audit.export")
			return
		}
	}
}

func splitCSV(v string) []string {
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}
