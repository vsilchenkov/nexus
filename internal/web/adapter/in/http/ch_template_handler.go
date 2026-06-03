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
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": i18n.Translate(i18n.FromGin(c), "ch_template.unavailable")})
	default:
		// Статическая ошибка валидации локализуема — отдаём i18n-код. Live-ошибка
		// ClickHouse динамическая (текст из БД), переводу не подлежит — отдаём как есть.
		if code, ok := chTemplateValidationCode(err); ok {
			c.JSON(http.StatusOK, gin.H{"ok": false, "error": i18n.Translate(i18n.FromGin(c), code), "code": code})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error()})
	}
}

func (h *CHTemplateHandler) replyDomainError(c *gin.Context, err error, op string) {
	if code, status, ok := chTemplateErrorCode(err); ok {
		h.replyCode(c, status, code)
		return
	}
	h.replyServerError(c, err, op)
}

// replyCode отвечает локализованным сообщением по i18n-ключу и дублирует
// сам ключ в поле "code" — фронт переводит его в языке UI (он может
// отличаться от Accept-Language), а "error" остаётся как fallback.
func (h *CHTemplateHandler) replyCode(c *gin.Context, status int, code string) {
	c.JSON(status, gin.H{"error": i18n.Translate(i18n.FromGin(c), code), "code": code})
}

func (h *CHTemplateHandler) replyServerError(c *gin.Context, err error, op string) {
	h.logger.ErrorWithOp("ch_template handler error", err, op,
		h.logger.Str("path", c.Request.URL.Path))
	localizedError(c, http.StatusInternalServerError, "error.internal")
}

// chTemplateErrorCode сопоставляет доменную ошибку каталога шаблонов с
// i18n-ключом и HTTP-статусом. ok=false — ошибка не относится к шаблонам
// (трактуется вызывающим как внутренняя ошибка сервера).
func chTemplateErrorCode(err error) (code string, status int, ok bool) {
	switch {
	case errors.Is(err, domain.ErrCHTemplateNotFound):
		return "ch_template.not_found", http.StatusNotFound, true
	case errors.Is(err, domain.ErrCHTemplateAlreadyExists):
		return "ch_template.already_exists", http.StatusConflict, true
	case errors.Is(err, domain.ErrCHTemplateInUse):
		return "ch_template.in_use", http.StatusConflict, true
	case errors.Is(err, domain.ErrCHTemplateDefaultImmutable):
		return "ch_template.default_immutable", http.StatusForbidden, true
	case errors.Is(err, usecase.ErrCHUnavailable):
		return "ch_template.unavailable", http.StatusServiceUnavailable, true
	}
	if code, ok := chTemplateValidationCode(err); ok {
		return code, http.StatusBadRequest, true
	}
	return "", 0, false
}

// chTemplateValidationCode выделяет статические ошибки валидации шаблона
// (400 Bad Request) и их i18n-ключи. Используется и хендлером CRUD, и
// Verify (где live-ошибки ClickHouse, наоборот, отдаются как есть).
func chTemplateValidationCode(err error) (string, bool) {
	switch {
	case errors.Is(err, domain.ErrCHTemplateNameFormat):
		return "ch_template.name_format", true
	case errors.Is(err, domain.ErrCHTemplateDescriptionLength):
		return "ch_template.description_length", true
	case errors.Is(err, domain.ErrCHTemplateInvalidEngine):
		return "ch_template.invalid_engine", true
	case errors.Is(err, domain.ErrCHTemplateInvalidPartition):
		return "ch_template.invalid_partition", true
	case errors.Is(err, domain.ErrCHTemplateEmptyOrderBy):
		return "ch_template.empty_order_by", true
	case errors.Is(err, domain.ErrCHTemplateInvalidOrderBy):
		return "ch_template.invalid_order_by", true
	case errors.Is(err, domain.ErrCHTemplateUnknownColumn):
		return "ch_template.unknown_column", true
	case errors.Is(err, domain.ErrCHTemplateInvalidCodec):
		return "ch_template.invalid_codec", true
	case errors.Is(err, domain.ErrCHTemplateInvalidIndexName):
		return "ch_template.invalid_index_name", true
	case errors.Is(err, domain.ErrCHTemplateInvalidIndexExpr):
		return "ch_template.invalid_index_expr", true
	case errors.Is(err, domain.ErrCHTemplateInvalidIndexType):
		return "ch_template.invalid_index_type", true
	case errors.Is(err, domain.ErrCHTemplateInvalidIndexGranularity):
		return "ch_template.invalid_index_granularity", true
	case errors.Is(err, domain.ErrCHTemplateInvalidTTLMode):
		return "ch_template.invalid_ttl_mode", true
	case errors.Is(err, domain.ErrCHTemplateTTLDaysRequired):
		return "ch_template.ttl_days_required", true
	case errors.Is(err, domain.ErrCHTemplateInvalidTableName):
		return "ch_template.invalid_table_name", true
	}
	return "", false
}
