package http

import (
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

func (h *AuditHandler) List(c *gin.Context) {
	f := port.AuditFilter{
		UserID:     c.Query("user_id"),
		TargetType: c.Query("target_type"),
		TargetID:   c.Query("target_id"),
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
	if f.Limit == 0 {
		f.Limit = 100
	}
	if v := c.Query("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.Offset = n
		}
	}

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
