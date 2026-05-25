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

	"bus/internal/domain"
	"bus/internal/platform/logging"
	"bus/internal/web/usecase"
	"bus/internal/web/usecase/port"
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
	Action     string         `json:"action"`
	TargetType string         `json:"target_type,omitempty"`
	TargetID   string         `json:"target_id,omitempty"`
	Details    map[string]any `json:"details"`
	IPAddress  string         `json:"ip_address,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
}

func toAuditResp(e *domain.AuditEntry) auditEntryResponse {
	return auditEntryResponse{
		ID: e.ID, UserID: e.UserID, UserLogin: e.UserLogin,
		Action: e.Action, TargetType: e.TargetType, TargetID: e.TargetID,
		Details: e.Details, IPAddress: e.IPAddress, CreatedAt: e.CreatedAt,
	}
}

// auditFilterFromQuery собирает фильтр из query-параметров. defaultLimit —
// значение по умолчанию для Limit (0 = без него). maxLimit — верхняя планка.
func auditFilterFromQuery(c *gin.Context, defaultLimit, maxLimit int) port.AuditFilter {
	f := port.AuditFilter{
		UserID:     c.Query("user_id"),
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

// ExportCSV — выгрузка журнала в CSV (§7.13, Phase 6.6). Тот же набор
// фильтров, что у List, но допустим больший limit (до 50000). По умолчанию
// возвращает первые 10000 строк, чтобы избежать DoS.
//
// CSV-формат: id, created_at, user_login, user_id, action, target_type,
// target_id, ip_address, details (JSON-строка).
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
		"id", "created_at", "user_login", "user_id", "action",
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
