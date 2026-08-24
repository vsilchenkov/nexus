package http

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"nexus/internal/domain"
	"nexus/internal/platform/i18n"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
)

// LogMaskHandler — справочник маскирования логов узлов (§95). Целиком admin-only
// (см. routes.go): шаблоны описывают, как выглядят секреты, — их не показываем
// operator'ам. Мутации публикуют reload, Sender перечитывает набор.
type LogMaskHandler struct {
	uc     *usecase.LogMaskUsecase
	logger logging.Logger
}

func NewLogMaskHandler(uc *usecase.LogMaskUsecase, logger logging.Logger) *LogMaskHandler {
	return &LogMaskHandler{uc: uc, logger: logger}
}

type logMaskRequest struct {
	Pattern     string `json:"pattern"`
	Replacement string `json:"replacement"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
	SortOrder   int    `json:"sort_order"`
}

type logMaskResponse struct {
	ID          string    `json:"id"`
	Pattern     string    `json:"pattern"`
	Replacement string    `json:"replacement"`
	Description string    `json:"description"`
	Enabled     bool      `json:"enabled"`
	SortOrder   int       `json:"sort_order"`
	CreatedBy   string    `json:"created_by"`
	UpdatedBy   string    `json:"updated_by"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func logMaskToResponse(e *domain.LogMaskPattern) logMaskResponse {
	return logMaskResponse{
		ID: e.ID, Pattern: e.Pattern, Replacement: e.Replacement, Description: e.Description,
		Enabled: e.Enabled, SortOrder: e.SortOrder,
		CreatedBy: e.CreatedBy, UpdatedBy: e.UpdatedBy, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
	}
}

type logMaskPreviewRequest struct {
	Pattern     string `json:"pattern"`
	Replacement string `json:"replacement"`
	Sample      string `json:"sample"`
}

// List godoc
// @Summary  Справочник маскирования логов: список (admin, §95).
// @Tags     log-masks
// @Produce  json
// @Param    q  query  string  false  "поиск по шаблону/описанию"
// @Success  200  {object}  ListLogMasksResponse
// @Security CookieAuth
// @Router   /api/log-masks [get]
func (h *LogMaskHandler) List(c *gin.Context) {
	items, err := h.uc.List(c.Request.Context(), c.Query("q"))
	if err != nil {
		h.replyServerError(c, err, "log_mask.list")
		return
	}
	out := make([]logMaskResponse, 0, len(items))
	for _, e := range items {
		out = append(out, logMaskToResponse(e))
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// Create godoc
// @Summary  Создать шаблон маскирования (admin, §95).
// @Tags     log-masks
// @Accept   json
// @Produce  json
// @Param    body  body  logMaskRequest  true  "log mask"
// @Success  201  {object}  logMaskResponse
// @Failure  400  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/log-masks [post]
func (h *LogMaskHandler) Create(c *gin.Context) {
	var req logMaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	e := &domain.LogMaskPattern{
		Pattern: req.Pattern, Replacement: req.Replacement, Description: req.Description,
		Enabled: req.Enabled, SortOrder: req.SortOrder,
	}
	if err := h.uc.Create(c.Request.Context(), actorFromCtx(c), e); err != nil {
		h.replyDomainError(c, err, "log_mask.create")
		return
	}
	c.JSON(http.StatusCreated, logMaskToResponse(e))
}

// Update godoc
// @Summary  Изменить шаблон маскирования (admin, §95).
// @Tags     log-masks
// @Accept   json
// @Produce  json
// @Param    id    path  string          true  "log mask id"
// @Param    body  body  logMaskRequest  true  "log mask"
// @Success  204
// @Failure  400  {object}  ErrorResponse
// @Failure  404  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/log-masks/{id} [patch]
func (h *LogMaskHandler) Update(c *gin.Context) {
	var req logMaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	e := &domain.LogMaskPattern{
		ID: c.Param("id"), Pattern: req.Pattern, Replacement: req.Replacement,
		Description: req.Description, Enabled: req.Enabled, SortOrder: req.SortOrder,
	}
	if err := h.uc.Update(c.Request.Context(), actorFromCtx(c), e); err != nil {
		h.replyDomainError(c, err, "log_mask.update")
		return
	}
	c.Status(http.StatusNoContent)
}

// Delete godoc
// @Summary  Удалить шаблон маскирования (admin, §95).
// @Tags     log-masks
// @Produce  json
// @Param    id   path  string  true  "log mask id"
// @Success  204
// @Failure  404  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/log-masks/{id} [delete]
func (h *LogMaskHandler) Delete(c *gin.Context) {
	if err := h.uc.Delete(c.Request.Context(), actorFromCtx(c), c.Param("id")); err != nil {
		h.replyDomainError(c, err, "log_mask.delete")
		return
	}
	c.Status(http.StatusNoContent)
}

// Preview godoc
// @Summary  Превью маскирования: как шаблон изменит образец (admin, §95).
// @Tags     log-masks
// @Accept   json
// @Produce  json
// @Param    body  body  logMaskPreviewRequest  true  "preview"
// @Success  200  {object}  LogMaskPreviewResponse
// @Failure  400  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/log-masks/preview [post]
func (h *LogMaskHandler) Preview(c *gin.Context) {
	var req logMaskPreviewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	res, err := h.uc.Preview(req.Pattern, req.Replacement, req.Sample)
	if err != nil {
		// Недописанный/невалидный regex в живой форме — нормальный ответ
		// «valid:false», а не 400 (по образцу host-preview): UI покажет состояние
		// без сетевой ошибки в консоли.
		if errors.Is(err, domain.ErrLogMaskPatternInvalid) {
			c.JSON(http.StatusOK, gin.H{"valid": false, "result": "", "matched": false})
			return
		}
		h.replyServerError(c, err, "log_mask.preview")
		return
	}
	c.JSON(http.StatusOK, gin.H{"valid": true, "result": res.Result, "matched": res.Matched})
}

func (h *LogMaskHandler) replyDomainError(c *gin.Context, err error, op string) {
	switch {
	case errors.Is(err, domain.ErrLogMaskNotFound):
		h.replyCode(c, http.StatusNotFound, "log_mask.not_found")
	case errors.Is(err, domain.ErrLogMaskPatternLength):
		h.replyCode(c, http.StatusBadRequest, "log_mask.pattern_length")
	case errors.Is(err, domain.ErrLogMaskPatternInvalid):
		h.replyCode(c, http.StatusBadRequest, "log_mask.pattern_invalid")
	case errors.Is(err, domain.ErrLogMaskReplacementLength):
		h.replyCode(c, http.StatusBadRequest, "log_mask.replacement_length")
	case errors.Is(err, domain.ErrLogMaskDescriptionLength):
		h.replyCode(c, http.StatusBadRequest, "log_mask.description_length")
	case errors.Is(err, domain.ErrLogMaskSortOrderNegative):
		h.replyCode(c, http.StatusBadRequest, "log_mask.sort_order_negative")
	default:
		h.replyServerError(c, err, op)
	}
}

func (h *LogMaskHandler) replyCode(c *gin.Context, status int, code string) {
	c.JSON(status, gin.H{"error": i18n.Translate(i18n.FromGin(c), code), "code": code})
}

func (h *LogMaskHandler) replyServerError(c *gin.Context, err error, op string) {
	h.logger.ErrorWithOp("log mask handler error", err, op,
		h.logger.Str("path", c.Request.URL.Path))
	localizedError(c, http.StatusInternalServerError, "error.internal")
}
