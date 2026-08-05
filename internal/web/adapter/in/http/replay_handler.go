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
	// ParamsOverride — query-строка ("a=1&b=2") вместо параметров оригинала;
	// null = взять из лога, "" = replay без параметров.
	ParamsOverride *string `json:"params_override"`
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
// @Failure  400   {object}  ErrorResponse  "bad params_override / too old failure"
// @Failure  404   {object}  ErrorResponse
// @Failure  409   {object}  ErrorResponse  "node disabled"
// @Failure  422   {object}  ErrorResponse  "original body not logged — provide manually (не применяется к GET)"
// @Failure  429   {object}  ErrorResponse  "rate limit exceeded"
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
		SyncOverride:   req.SyncOverride,
		UseNodeAuth:    req.UseNodeAuth == nil || *req.UseNodeAuth,
		CustomAuth:     req.CustomAuth,
		ParamsOverride: req.ParamsOverride,
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
	case errors.Is(err, usecase.ErrReplayBadParams):
		localizedError(c, http.StatusBadRequest, "replay.bad_params")
	case errors.Is(err, usecase.ErrReplayBodyUnavailable):
		localizedError(c, http.StatusUnprocessableEntity, "replay.body_unavailable")
	case errors.Is(err, usecase.ErrReplayBodyMultipart):
		localizedError(c, http.StatusUnprocessableEntity, "replay.multipart_unavailable")
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

// ReplayFailed godoc
// @Summary  Повторить все неудачные доставки узла сейчас (§36.11).
// @Description  Пере-инжектирует через Receiver недоставленные запросы узла за период (§79.1: записи без единого успешного прогона — уже доставленные не переотправляются), отменяет их оригиналы в DLQ (без двойной доставки) и убирает их строки done=0 из «Неудачных доставок» (§79.2, поле cleaned; на внешней §64 и чужой §70.4 таблице очистка пропускается). Пустые from/to = всё. Admin-only.
// @Tags     async-queue
// @Accept   json
// @Produce  json
// @Param    id    path  string             true   "node id"
// @Param    body  body  queuePurgeRequest  false  "период (пусто = всё)"
// @Success  200   {object}  usecase.ReplayBulkResult
// @Failure  400   {object}  ErrorResponse
// @Failure  404   {object}  ErrorResponse
// @Failure  409   {object}  ErrorResponse  "node disabled"
// @Failure  429   {object}  ErrorResponse  "rate limit exceeded"
// @Security CookieAuth
// @Router   /api/nodes/{id}/async-queue/replay-failed [post]
func (h *ReplayHandler) ReplayFailed(c *gin.Context) {
	var req queuePurgeRequest
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	if !req.From.IsZero() && !req.To.IsZero() && req.From.After(req.To) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "from must be before to"})
		return
	}
	res, err := h.uc.ReplayFailed(c.Request.Context(), actorFromCtx(c), c.Param("id"), currentTeamID(c), req.From, req.To)
	switch {
	case err == nil:
		c.JSON(http.StatusOK, res)
	case errors.Is(err, usecase.ErrReplayRateLimit):
		c.Header("Retry-After", "60")
		localizedError(c, http.StatusTooManyRequests, "error.rate_limited")
	case errors.Is(err, domain.ErrNodeNotFound):
		localizedError(c, http.StatusNotFound, "node.not_found")
	case errors.Is(err, domain.ErrNodeDisabled):
		localizedError(c, http.StatusConflict, "node.disabled")
	default:
		h.logger.ErrorWithOp("replay-all failed", err, "node.replay_all",
			h.logger.Str("node_id", c.Param("id")))
		localizedError(c, http.StatusInternalServerError, "error.internal")
	}
}
