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

// RequestFieldCatalogHandler — справочник полей запроса (§41). GET доступен
// любой сессии (combobox формы узла); POST (idempotent create из combobox) —
// manager+ (как каталог заголовков §24).
type RequestFieldCatalogHandler struct {
	uc     *usecase.RequestFieldCatalogUsecase
	logger logging.Logger
}

func NewRequestFieldCatalogHandler(uc *usecase.RequestFieldCatalogUsecase, logger logging.Logger) *RequestFieldCatalogHandler {
	return &RequestFieldCatalogHandler{uc: uc, logger: logger}
}

type requestFieldRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type requestFieldResponse struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	UsageCount  int       `json:"usage_count"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func requestFieldToResponse(e *domain.RequestFieldCatalogEntry) requestFieldResponse {
	return requestFieldResponse{
		ID: e.ID, Name: e.Name, Description: e.Description, UsageCount: e.UsageCount,
		CreatedBy: e.CreatedBy, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
	}
}

// Search godoc
// @Summary  Справочник полей запроса: автодополнение (§41).
// @Tags     request-fields
// @Produce  json
// @Param    q      query  string  false  "prefix поиска по имени; пусто = топ-используемые"
// @Param    limit  query  int     false  "лимит (по умолчанию 20)"
// @Success  200  {object}  ListRequestFieldsResponse
// @Security CookieAuth
// @Router   /api/request-fields [get]
func (h *RequestFieldCatalogHandler) Search(c *gin.Context) {
	items, err := h.uc.Search(c.Request.Context(), c.Query("q"), atoiDefault(c.Query("limit"), 20))
	if err != nil {
		h.replyServerError(c, err, "request_field.search")
		return
	}
	out := make([]requestFieldResponse, 0, len(items))
	for _, e := range items {
		out = append(out, requestFieldToResponse(e))
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// Create godoc
// @Summary  Создать поле запроса в справочнике (manager, §41).
// @Description  Идемпотентно по case-insensitive имени: повтор вернёт существующую запись (200), а не ошибку.
// @Tags     request-fields
// @Accept   json
// @Produce  json
// @Param    body  body  requestFieldRequest  true  "request field"
// @Success  200  {object}  requestFieldResponse
// @Failure  400  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/request-fields [post]
func (h *RequestFieldCatalogHandler) Create(c *gin.Context) {
	var req requestFieldRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	e := &domain.RequestFieldCatalogEntry{Name: req.Name, Description: req.Description, CreatedBy: actorFromCtx(c).UserLogin}
	if err := h.uc.Create(c.Request.Context(), actorFromCtx(c), e); err != nil {
		h.replyDomainError(c, err, "request_field.create")
		return
	}
	c.JSON(http.StatusOK, requestFieldToResponse(e))
}

func (h *RequestFieldCatalogHandler) replyDomainError(c *gin.Context, err error, op string) {
	switch {
	case errors.Is(err, domain.ErrRequestFieldNameLength):
		h.replyCode(c, http.StatusBadRequest, "request_field.name_length")
	case errors.Is(err, domain.ErrRequestFieldNameFormat):
		h.replyCode(c, http.StatusBadRequest, "request_field.name_format")
	case errors.Is(err, domain.ErrRequestFieldDescriptionLength):
		h.replyCode(c, http.StatusBadRequest, "request_field.description_length")
	case errors.Is(err, domain.ErrRequestFieldNotFound):
		h.replyCode(c, http.StatusNotFound, "request_field.not_found")
	default:
		h.replyServerError(c, err, op)
	}
}

func (h *RequestFieldCatalogHandler) replyCode(c *gin.Context, status int, code string) {
	c.JSON(status, gin.H{"error": i18n.Translate(i18n.FromGin(c), code), "code": code})
}

func (h *RequestFieldCatalogHandler) replyServerError(c *gin.Context, err error, op string) {
	h.logger.ErrorWithOp("request field catalog handler error", err, op,
		h.logger.Str("path", c.Request.URL.Path))
	localizedError(c, http.StatusInternalServerError, "error.internal")
}
