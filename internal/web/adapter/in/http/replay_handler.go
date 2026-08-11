package http

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"nexus/internal/domain"
	"nexus/internal/domain/logsearch"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
	"nexus/internal/web/usecase/port"
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

// replayPeriodRequest — тело обоих эндпоинтов §85.
//
// Фильтр приходит НЕ здесь, а query-параметрами: их разбирает тот же
// logQueryFromContext, что список и счётчик журнала. Свой разбор в теле означал
// бы второй парсер тех же фильтров, а расхождение показало бы в предпросмотре
// одно множество, а отправило другое.
type replayPeriodRequest struct {
	// Cursor — позиция продолжения из предыдущего батча; отсутствует у первого.
	Cursor *replayCursorDTO `json:"cursor"`
	// Limit — размер батча; 0 = серверный дефолт.
	Limit int `json:"limit"`
	// SkipReplayCopies — не брать записи, порождённые прошлыми повторами (§85.6).
	// Указатель, чтобы отличить «не прислано» (дефолт true) от явного false.
	SkipReplayCopies *bool `json:"skip_replay_copies"`
}

// replayCursorDTO — keyset-позиция обхода журнала (§85.5).
type replayCursorDTO struct {
	AfterMs int64  `json:"after_ms"`
	AfterID string `json:"after_id"`
}

// replayPeriodInput — общая сборка входа для обоих эндпоинтов.
func replayPeriodInput(c *gin.Context, req replayPeriodRequest) usecase.ReplayPeriodInput {
	in := usecase.ReplayPeriodInput{
		NodeID:           c.Param("id"),
		TeamID:           currentTeamID(c),
		Filter:           logQueryFromContext(c),
		Limit:            req.Limit,
		SkipReplayCopies: req.SkipReplayCopies == nil || *req.SkipReplayCopies,
	}
	if req.Cursor != nil {
		in.Cursor = port.ReplayCursor{AfterMs: req.Cursor.AfterMs, AfterID: req.Cursor.AfterID}
	}
	return in
}

// bindReplayPeriod — разбор тела (допускается пустое) с проверкой курсора.
func bindReplayPeriod(c *gin.Context) (replayPeriodRequest, bool) {
	var req replayPeriodRequest
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return req, false
		}
	}
	return req, true
}

// PlanPeriod godoc
// @Summary  Предпросмотр повторной отправки из логов за период (§85.7).
// @Description  Read-only: сообщает, сколько записей окна будет отправлено, а сколько нет и почему (тело обрезано / multipart / не сохранено / синхронная запись / запись-повтор). Ничего не отправляет и в аудит не пишет. Фильтр — те же query-параметры, что у GET /api/nodes/{id}/logs. Флаг exact=false означает, что окно разобрано не целиком (потолок разбора) и числа неполные. Границы окна возвращаются в ответе — их надо слать обратно в каждый батч, иначе «по сейчас» затянет в набор собственные повторы.
// @Tags     logs
// @Accept   json
// @Produce  json
// @Param    id    path   string  true   "node id"
// @Param    from  query  string  true   "начало окна (RFC3339 или UnixMilli)"
// @Param    to    query  string  true   "конец окна (RFC3339 или UnixMilli)"
// @Param    body  body   replayPeriodRequest  false  "опции"
// @Success  200   {object}  usecase.ReplayPeriodPlan
// @Failure  400   {object}  ErrorResponse  "нет границ окна / плохой поисковый запрос"
// @Failure  404   {object}  ErrorResponse
// @Failure  409   {object}  ErrorResponse  "узел отключён или синхронный"
// @Failure  422   {object}  ErrorResponse  "узел не логирует тело запроса"
// @Security CookieAuth
// @Router   /api/nodes/{id}/logs/replay-period/plan [post]
func (h *ReplayHandler) PlanPeriod(c *gin.Context) {
	req, ok := bindReplayPeriod(c)
	if !ok {
		return
	}
	plan, err := h.uc.PlanPeriod(c.Request.Context(), replayPeriodInput(c, req))
	if err != nil {
		h.replayPeriodError(c, err, "node.replay_period.plan")
		return
	}
	c.JSON(http.StatusOK, plan)
}

// RunPeriod godoc
// @Summary  Батч повторной отправки из логов за период (§85.5).
// @Description  Реинжектирует до limit записей окна через Receiver и возвращает курсор продолжения; next_cursor=null означает, что окно пройдено. Цикл батчей крутит клиент. Повторяются только async-записи; уже отправленные повторно не берутся благодаря курсору. Аудит пишется на каждый батч.
// @Tags     logs
// @Accept   json
// @Produce  json
// @Param    id    path   string  true   "node id"
// @Param    from  query  string  true   "начало окна (RFC3339 или UnixMilli)"
// @Param    to    query  string  true   "конец окна (RFC3339 или UnixMilli)"
// @Param    body  body   replayPeriodRequest  false  "курсор и опции"
// @Success  200   {object}  usecase.ReplayPeriodBatch
// @Failure  400   {object}  ErrorResponse  "нет границ окна / плохой поисковый запрос"
// @Failure  404   {object}  ErrorResponse
// @Failure  409   {object}  ErrorResponse  "узел отключён или синхронный"
// @Failure  422   {object}  ErrorResponse  "узел не логирует тело запроса"
// @Failure  429   {object}  ErrorResponse  "rate limit exceeded"
// @Security CookieAuth
// @Router   /api/nodes/{id}/logs/replay-period/run [post]
func (h *ReplayHandler) RunPeriod(c *gin.Context) {
	req, ok := bindReplayPeriod(c)
	if !ok {
		return
	}
	res, err := h.uc.ReplayPeriod(c.Request.Context(), actorFromCtx(c), replayPeriodInput(c, req))
	if err != nil {
		h.replayPeriodError(c, err, "node.replay_period.run")
		return
	}
	c.JSON(http.StatusOK, res)
}

// replayPeriodError — общий маппинг отказов §85 на HTTP.
func (h *ReplayHandler) replayPeriodError(c *gin.Context, err error, op string) {
	switch {
	case errors.Is(err, usecase.ErrReplayRateLimit):
		c.Header("Retry-After", "60")
		localizedError(c, http.StatusTooManyRequests, "error.rate_limited")
	case errors.Is(err, usecase.ErrReplayPeriodNoWindow):
		localizedError(c, http.StatusBadRequest, "replay_period.no_window")
	case errors.Is(err, logsearch.ErrBadQuery):
		localizedError(c, http.StatusBadRequest, "error.bad_search_query")
	case errors.Is(err, usecase.ErrReplayPeriodSyncNode):
		localizedError(c, http.StatusConflict, "replay_period.sync_node")
	case errors.Is(err, usecase.ErrReplayPeriodBodyNotLogged):
		localizedError(c, http.StatusUnprocessableEntity, "replay_period.body_not_logged")
	case errors.Is(err, domain.ErrNodeLogsNotConfigured):
		localizedError(c, http.StatusUnprocessableEntity, "replay_period.logs_disabled")
	case errors.Is(err, domain.ErrNodeNotFound), errors.Is(err, domain.ErrNotFound):
		localizedError(c, http.StatusNotFound, "node.not_found")
	case errors.Is(err, domain.ErrNodeDisabled):
		localizedError(c, http.StatusConflict, "node.disabled")
	case errors.Is(err, domain.ErrLogsBackendUnavailable):
		// Логи временно недоступны — это не 500: та же мягкая деградация, что у
		// счётчиков журнала (§67), только здесь операция просто не начинается.
		localizedError(c, http.StatusServiceUnavailable, "error.logs_unavailable")
	case errors.Is(err, context.Canceled):
		// Клиент ушёл (кнопка «Остановить» или закрытая вкладка) — не ошибка
		// сервера и не повод для записи в Sentry.
		h.logger.Debug("replay period aborted by client", h.logger.Str("node_id", c.Param("id")))
	default:
		h.logger.ErrorWithOp("replay period failed", err, op,
			h.logger.Str("node_id", c.Param("id")))
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
