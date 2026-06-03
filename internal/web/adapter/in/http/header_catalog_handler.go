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

// HeaderCatalogHandler — справочник HTTP-заголовков (§24). GET доступен любой
// сессии (combobox); POST (idempotent create из combobox) — admin-only.
type HeaderCatalogHandler struct {
	uc     *usecase.HeaderCatalogUsecase
	logger logging.Logger
}

func NewHeaderCatalogHandler(uc *usecase.HeaderCatalogUsecase, logger logging.Logger) *HeaderCatalogHandler {
	return &HeaderCatalogHandler{uc: uc, logger: logger}
}

type headerRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type headerResponse struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	UsageCount  int       `json:"usage_count"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func headerToResponse(e *domain.HeaderCatalogEntry) headerResponse {
	return headerResponse{
		ID: e.ID, Name: e.Name, Description: e.Description, UsageCount: e.UsageCount,
		CreatedBy: e.CreatedBy, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
	}
}

// Search godoc
// @Summary  Справочник заголовков: автодополнение (§24).
// @Tags     headers
// @Produce  json
// @Param    q      query  string  false  "prefix поиска по имени; пусто = топ-используемые"
// @Param    limit  query  int     false  "лимит (по умолчанию 20)"
// @Success  200  {object}  map[string]interface{}
// @Security CookieAuth
// @Router   /api/headers [get]
func (h *HeaderCatalogHandler) Search(c *gin.Context) {
	items, err := h.uc.Search(c.Request.Context(), c.Query("q"), atoiDefault(c.Query("limit"), 20))
	if err != nil {
		h.replyServerError(c, err, "header.search")
		return
	}
	out := make([]headerResponse, 0, len(items))
	for _, e := range items {
		out = append(out, headerToResponse(e))
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// Create godoc
// @Summary  Создать заголовок в справочнике (admin, §24).
// @Description  Идемпотентно по case-insensitive имени: повтор вернёт существующую запись (200), а не ошибку.
// @Tags     headers
// @Accept   json
// @Produce  json
// @Param    body  body  headerRequest  true  "header"
// @Success  200  {object}  headerResponse
// @Failure  400  {object}  map[string]string
// @Security CookieAuth
// @Router   /api/headers [post]
func (h *HeaderCatalogHandler) Create(c *gin.Context) {
	var req headerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	e := &domain.HeaderCatalogEntry{Name: req.Name, Description: req.Description, CreatedBy: actorFromCtx(c).UserLogin}
	if err := h.uc.Create(c.Request.Context(), actorFromCtx(c), e); err != nil {
		h.replyDomainError(c, err, "header.create")
		return
	}
	c.JSON(http.StatusOK, headerToResponse(e))
}

func (h *HeaderCatalogHandler) replyDomainError(c *gin.Context, err error, op string) {
	switch {
	case errors.Is(err, domain.ErrHeaderNameLength):
		h.replyCode(c, http.StatusBadRequest, "header.name_length")
	case errors.Is(err, domain.ErrHeaderNameFormat):
		h.replyCode(c, http.StatusBadRequest, "header.name_format")
	case errors.Is(err, domain.ErrHeaderDescriptionLength):
		h.replyCode(c, http.StatusBadRequest, "header.description_length")
	case errors.Is(err, domain.ErrHeaderNotFound):
		h.replyCode(c, http.StatusNotFound, "header.not_found")
	default:
		h.replyServerError(c, err, op)
	}
}

func (h *HeaderCatalogHandler) replyCode(c *gin.Context, status int, code string) {
	c.JSON(status, gin.H{"error": i18n.Translate(i18n.FromGin(c), code), "code": code})
}

func (h *HeaderCatalogHandler) replyServerError(c *gin.Context, err error, op string) {
	h.logger.ErrorWithOp("header catalog handler error", err, op,
		h.logger.Str("path", c.Request.URL.Path))
	localizedError(c, http.StatusInternalServerError, "error.internal")
}
