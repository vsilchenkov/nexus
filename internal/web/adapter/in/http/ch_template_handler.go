package http

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
)

// CHTemplateHandler — CRUD каталога шаблонов CH-таблиц (§19).
// GET доступен любой сессии (нужен для селектора при настройке узла);
// мутации и verify — admin-only (см. routes.go).
type CHTemplateHandler struct {
	uc     *usecase.CHTemplateUsecase
	logger logging.Logger
}

func NewCHTemplateHandler(uc *usecase.CHTemplateUsecase, logger logging.Logger) *CHTemplateHandler {
	return &CHTemplateHandler{uc: uc, logger: logger}
}

type chTemplateRequest struct {
	Name        string                `json:"name"`
	Description string                `json:"description"`
	Spec        domain.CHTemplateSpec `json:"spec"`
	IsDefault   bool                  `json:"is_default"`
}

type chTemplateResponse struct {
	ID          string                `json:"id"`
	Name        string                `json:"name"`
	Description string                `json:"description"`
	Spec        domain.CHTemplateSpec `json:"spec"`
	IsDefault   bool                  `json:"is_default"`
	CreatedAt   time.Time             `json:"created_at"`
	UpdatedAt   time.Time             `json:"updated_at"`
}

func chTemplateToResponse(t *domain.CHTemplate) chTemplateResponse {
	return chTemplateResponse{
		ID: t.ID, Name: t.Name, Description: t.Description, Spec: t.Spec,
		IsDefault: t.IsDefault, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
}

// List godoc
// @Summary  Список шаблонов CH-таблиц (§19).
// @Tags     ch-templates
// @Produce  json
// @Success  200  {object}  map[string]interface{}
// @Security CookieAuth
// @Router   /api/ch-templates [get]
func (h *CHTemplateHandler) List(c *gin.Context) {
	items, err := h.uc.List(c.Request.Context())
	if err != nil {
		h.replyServerError(c, err, "ch_template.list")
		return
	}
	out := make([]chTemplateResponse, 0, len(items))
	for _, t := range items {
		out = append(out, chTemplateToResponse(t))
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// Get godoc
// @Summary  Один шаблон CH-таблицы по id (§19).
// @Tags     ch-templates
// @Produce  json
// @Param    id   path  string  true  "template id"
// @Success  200  {object}  chTemplateResponse
// @Failure  404  {object}  map[string]string
// @Security CookieAuth
// @Router   /api/ch-templates/{id} [get]
func (h *CHTemplateHandler) Get(c *gin.Context) {
	t, err := h.uc.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.replyDomainError(c, err, "ch_template.get")
		return
	}
	c.JSON(http.StatusOK, chTemplateToResponse(t))
}

// Create godoc
// @Summary  Создать шаблон CH-таблицы (admin, §19).
// @Tags     ch-templates
// @Accept   json
// @Produce  json
// @Param    body  body  chTemplateRequest  true  "template"
// @Success  201   {object}  chTemplateResponse
// @Failure  400   {object}  map[string]string
// @Failure  409   {object}  map[string]string
// @Security CookieAuth
// @Router   /api/ch-templates [post]
func (h *CHTemplateHandler) Create(c *gin.Context) {
	var req chTemplateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	t := &domain.CHTemplate{Name: req.Name, Description: req.Description, Spec: req.Spec, IsDefault: req.IsDefault}
	if err := h.uc.Create(c.Request.Context(), actorFromCtx(c), t); err != nil {
		h.replyDomainError(c, err, "ch_template.create")
		return
	}
	c.JSON(http.StatusCreated, chTemplateToResponse(t))
}

// Update godoc
// @Summary  Обновить шаблон CH-таблицы (admin, §19).
// @Tags     ch-templates
// @Accept   json
// @Produce  json
// @Param    id    path  string             true  "template id"
// @Param    body  body  chTemplateRequest  true  "template"
// @Success  200   {object}  chTemplateResponse
// @Failure  400   {object}  map[string]string
// @Failure  404   {object}  map[string]string
// @Failure  409   {object}  map[string]string
// @Security CookieAuth
// @Router   /api/ch-templates/{id} [put]
func (h *CHTemplateHandler) Update(c *gin.Context) {
	var req chTemplateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	t := &domain.CHTemplate{ID: c.Param("id"), Name: req.Name, Description: req.Description, Spec: req.Spec, IsDefault: req.IsDefault}
	if err := h.uc.Update(c.Request.Context(), actorFromCtx(c), t); err != nil {
		h.replyDomainError(c, err, "ch_template.update")
		return
	}
	c.JSON(http.StatusOK, chTemplateToResponse(t))
}

// Delete godoc
// @Summary  Удалить шаблон CH-таблицы (admin, §19).
// @Description  Нельзя удалить default или используемый узлами шаблон (409).
// @Tags     ch-templates
// @Produce  json
// @Param    id   path  string  true  "template id"
// @Success  204
// @Failure  403  {object}  map[string]string
// @Failure  404  {object}  map[string]string
// @Failure  409  {object}  map[string]string
// @Security CookieAuth
// @Router   /api/ch-templates/{id} [delete]
func (h *CHTemplateHandler) Delete(c *gin.Context) {
	if err := h.uc.Delete(c.Request.Context(), actorFromCtx(c), c.Param("id")); err != nil {
		h.replyDomainError(c, err, "ch_template.delete")
		return
	}
	c.Status(http.StatusNoContent)
}

// Verify godoc
// @Summary  Проверить шаблон CH-таблицы (admin, §19.4).
// @Description  Статическая валидация + пробное создание временной таблицы в ClickHouse. Возвращает {ok} или {ok:false,error}.
// @Tags     ch-templates
// @Accept   json
// @Produce  json
// @Param    body  body  chTemplateRequest  true  "template"
// @Success  200   {object}  map[string]interface{}
// @Failure  503   {object}  map[string]string  "clickhouse unavailable"
// @Security CookieAuth
// @Router   /api/ch-templates/verify [post]
func (h *CHTemplateHandler) Verify(c *gin.Context) {
	var req chTemplateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	t := &domain.CHTemplate{Name: req.Name, Description: req.Description, Spec: req.Spec, IsDefault: req.IsDefault}
	err := h.uc.Verify(c.Request.Context(), t)
	switch {
	case err == nil:
		c.JSON(http.StatusOK, gin.H{"ok": true})
	case errors.Is(err, usecase.ErrCHUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "clickhouse unavailable"})
	default:
		// И статическая, и live-ошибка означают «шаблон невалиден» — отдаём в теле.
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error()})
	}
}

func (h *CHTemplateHandler) replyDomainError(c *gin.Context, err error, op string) {
	switch {
	case errors.Is(err, domain.ErrCHTemplateNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "template not found"})
	case errors.Is(err, domain.ErrCHTemplateAlreadyExists):
		c.JSON(http.StatusConflict, gin.H{"error": "template with this name already exists"})
	case errors.Is(err, domain.ErrCHTemplateInUse):
		c.JSON(http.StatusConflict, gin.H{"error": "template is used by nodes"})
	case errors.Is(err, domain.ErrCHTemplateDefaultImmutable):
		c.JSON(http.StatusForbidden, gin.H{"error": "default template cannot be deleted"})
	case errors.Is(err, usecase.ErrCHUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "clickhouse unavailable"})
	case isCHTemplateValidationError(err):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	default:
		h.replyServerError(c, err, op)
	}
}

func (h *CHTemplateHandler) replyServerError(c *gin.Context, err error, op string) {
	h.logger.ErrorWithOp("ch_template handler error", err, op,
		h.logger.Str("path", c.Request.URL.Path))
	localizedError(c, http.StatusInternalServerError, "error.internal")
}

func isCHTemplateValidationError(err error) bool {
	for _, target := range []error{
		domain.ErrCHTemplateNameFormat, domain.ErrCHTemplateDescriptionLength,
		domain.ErrCHTemplateInvalidEngine, domain.ErrCHTemplateInvalidPartition,
		domain.ErrCHTemplateEmptyOrderBy, domain.ErrCHTemplateInvalidOrderBy,
		domain.ErrCHTemplateUnknownColumn, domain.ErrCHTemplateInvalidCodec,
		domain.ErrCHTemplateInvalidIndexName, domain.ErrCHTemplateInvalidIndexExpr,
		domain.ErrCHTemplateInvalidIndexType, domain.ErrCHTemplateInvalidIndexGranularity,
		domain.ErrCHTemplateInvalidTTLMode, domain.ErrCHTemplateTTLDaysRequired,
		domain.ErrCHTemplateInvalidTableName,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}
