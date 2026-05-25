package http

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"bus/internal/domain"
	"bus/internal/platform/logging"
	"bus/internal/web/usecase"
)

// ReplayHandler — POST /api/logs/{id}/replay (§7.4.1 ТЗ).
type ReplayHandler struct {
	uc     *usecase.ReplayUsecase
	logger logging.Logger
}

func NewReplayHandler(uc *usecase.ReplayUsecase, logger logging.Logger) *ReplayHandler {
	return &ReplayHandler{uc: uc, logger: logger}
}

// ReplayRequest — тело POST /api/logs/{id}/replay.
type ReplayRequest struct {
	NodeID       string  `json:"node_id" binding:"required"`
	BodyOverride *string `json:"body_override"`
	SyncOverride bool    `json:"sync_override"`
	UseNodeAuth  *bool   `json:"use_node_auth"`
	CustomAuth   string  `json:"custom_auth"`
}

// Replay godoc
// @Summary  Повторно отправить запрос через шину (§7.4.1).
// @Description  Берёт оригинал из ClickHouse-лога, применяет переопределения, отправляет через Receiver.
// @Tags     logs
// @Accept   json
// @Produce  json
// @Param    id    path  string         true  "log id (UUID v4)"
// @Param    body  body  ReplayRequest  true  "опции"
// @Success  200   {object}  usecase.ReplayResult
// @Failure  400   {object}  map[string]string
// @Failure  404   {object}  map[string]string
// @Failure  409   {object}  map[string]string  "node disabled"
// @Failure  429   {object}  map[string]string  "rate limit exceeded"
// @Security CookieAuth
// @Router   /api/logs/{id}/replay [post]
func (h *ReplayHandler) Replay(c *gin.Context) {
	logID := c.Param("id")
	if logID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "log id required"})
		return
	}
	var req ReplayRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	opts := usecase.ReplayOptions{
		SyncOverride: req.SyncOverride,
		UseNodeAuth:  req.UseNodeAuth == nil || *req.UseNodeAuth,
		CustomAuth:   req.CustomAuth,
	}
	if req.BodyOverride != nil {
		opts.BodyOverride = []byte(*req.BodyOverride)
	}

	res, err := h.uc.Replay(c.Request.Context(), actorFromCtx(c), logID, req.NodeID, opts)
	switch {
	case err == nil:
		c.JSON(http.StatusOK, res)
	case errors.Is(err, usecase.ErrReplayRateLimit):
		c.Header("Retry-After", "60")
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "rate limit exceeded"})
	case errors.Is(err, usecase.ErrReplayTooOldFailure):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, domain.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "log not found"})
	case errors.Is(err, domain.ErrNodeNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
	case errors.Is(err, domain.ErrNodeDisabled):
		c.JSON(http.StatusConflict, gin.H{"error": "node is disabled; enable it before replay"})
	default:
		h.logger.ErrorWithOp("replay failed", err, "log.replay",
			h.logger.Str("log_id", logID))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
	}
}
