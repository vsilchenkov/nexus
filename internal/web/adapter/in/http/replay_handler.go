package http

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
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
// @Failure  422   {object}  map[string]string  "original body not logged — provide manually"
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

	res, err := h.uc.Replay(c.Request.Context(), actorFromCtx(c), logID, req.NodeID, currentTeamID(c), opts)
	switch {
	case err == nil:
		c.JSON(http.StatusOK, res)
	case errors.Is(err, usecase.ErrReplayRateLimit):
		c.Header("Retry-After", "60")
		localizedError(c, http.StatusTooManyRequests, "error.rate_limited")
	case errors.Is(err, usecase.ErrReplayTooOldFailure):
		localizedError(c, http.StatusBadRequest, "replay.too_old")
	case errors.Is(err, usecase.ErrReplayBodyUnavailable):
		localizedError(c, http.StatusUnprocessableEntity, "replay.body_unavailable")
	case errors.Is(err, domain.ErrNotFound):
		localizedError(c, http.StatusNotFound, "error.not_found")
	case errors.Is(err, domain.ErrNodeNotFound):
		localizedError(c, http.StatusNotFound, "node.not_found")
	case errors.Is(err, domain.ErrNodeDisabled):
		localizedError(c, http.StatusConflict, "node.disabled")
	default:
		h.logger.ErrorWithOp("replay failed", err, "log.replay",
			h.logger.Str("log_id", logID))
		localizedError(c, http.StatusInternalServerError, "error.internal")
	}
}
