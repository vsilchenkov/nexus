package http

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"nexus/internal/domain"
	"nexus/internal/platform/i18n"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
)

// HostAllowlistHandler — каталог разрешённых хостов (§23) и привязка к узлам.
// GET доступен любой сессии (combobox формы узла); мутации каталога и
// привязки — admin-only (см. routes.go).
type HostAllowlistHandler struct {
	uc     *usecase.HostAllowlistUsecase
	logger logging.Logger
}

func NewHostAllowlistHandler(uc *usecase.HostAllowlistUsecase, logger logging.Logger) *HostAllowlistHandler {
	return &HostAllowlistHandler{uc: uc, logger: logger}
}

type hostRequest struct {
	Pattern     string `json:"pattern"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
}

type hostResponse struct {
	ID          string    `json:"id"`
	Pattern     string    `json:"pattern"`
	Kind        string    `json:"kind"`
	Description string    `json:"description"`
	UsageCount  int       `json:"usage_count"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func hostToResponse(e *domain.HostAllowlistEntry) hostResponse {
	return hostResponse{
		ID: e.ID, Pattern: e.Pattern, Kind: string(e.Kind), Description: e.Description,
		UsageCount: e.UsageCount, CreatedBy: e.CreatedBy, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
	}
}

type hostPreviewRequest struct {
	Pattern  string   `json:"pattern"`
	Kind     string   `json:"kind"`
	TestURLs []string `json:"test_urls"`
}

type attachHostRequest struct {
	HostID string `json:"host_id"`
}

// Search godoc
// @Summary  Каталог разрешённых хостов: поиск/листинг (§23).
// @Tags     allowed-hosts
// @Produce  json
// @Param    q      query  string  false  "поиск по паттерну и описанию"
// @Param    kind   query  string  false  "фильтр по типу: exact|wildcard|regex"
// @Param    limit  query  int     false  "лимит (по умолчанию 50)"
// @Success  200  {object}  ListHostsResponse
// @Security CookieAuth
// @Router   /api/allowed-hosts [get]
func (h *HostAllowlistHandler) Search(c *gin.Context) {
	limit := atoiDefault(c.Query("limit"), 50)
	items, err := h.uc.Search(c.Request.Context(), c.Query("q"), domain.HostKind(c.Query("kind")), limit)
	if err != nil {
		h.replyServerError(c, err, "host.search")
		return
	}
	out := make([]hostResponse, 0, len(items))
	for _, e := range items {
		out = append(out, hostToResponse(e))
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// Create godoc
// @Summary  Создать разрешённый хост (admin, §23).
// @Description  Идемпотентно по case-insensitive паттерну: повтор вернёт существующую запись.
// @Tags     allowed-hosts
// @Accept   json
// @Produce  json
// @Param    body  body  hostRequest  true  "host"
// @Success  201  {object}  hostResponse
// @Failure  400  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/allowed-hosts [post]
func (h *HostAllowlistHandler) Create(c *gin.Context) {
	var req hostRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	e := &domain.HostAllowlistEntry{
		Pattern: req.Pattern, Kind: domain.HostKind(req.Kind), Description: req.Description,
		CreatedBy: actorFromCtx(c).UserLogin,
	}
	if err := h.uc.Create(c.Request.Context(), actorFromCtx(c), e); err != nil {
		h.replyDomainError(c, err, "host.create")
		return
	}
	c.JSON(http.StatusCreated, hostToResponse(e))
}

// Update godoc
// @Summary  Изменить разрешённый хост (admin, §23).
// @Description  Описание меняется всегда; паттерн/тип — только если хост не используется (usage_count=0).
// @Tags     allowed-hosts
// @Accept   json
// @Produce  json
// @Param    id    path  string       true  "host id"
// @Param    body  body  hostRequest  true  "host"
// @Success  204
// @Failure  400  {object}  ErrorResponse
// @Failure  404  {object}  ErrorResponse
// @Failure  409  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/allowed-hosts/{id} [patch]
func (h *HostAllowlistHandler) Update(c *gin.Context) {
	var req hostRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.uc.Update(c.Request.Context(), actorFromCtx(c), c.Param("id"), req.Pattern, domain.HostKind(req.Kind), req.Description); err != nil {
		h.replyDomainError(c, err, "host.update")
		return
	}
	c.Status(http.StatusNoContent)
}

// Delete godoc
// @Summary  Удалить разрешённый хост (admin, §23).
// @Description  Нельзя удалить хост, используемый узлами (409).
// @Tags     allowed-hosts
// @Produce  json
// @Param    id   path  string  true  "host id"
// @Success  204
// @Failure  404  {object}  ErrorResponse
// @Failure  409  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/allowed-hosts/{id} [delete]
func (h *HostAllowlistHandler) Delete(c *gin.Context) {
	if err := h.uc.Delete(c.Request.Context(), actorFromCtx(c), c.Param("id")); err != nil {
		h.replyDomainError(c, err, "host.delete")
		return
	}
	c.Status(http.StatusNoContent)
}

// Preview godoc
// @Summary  Превью паттерна: какие URL разрешит/заблокирует (§23).
// @Tags     allowed-hosts
// @Accept   json
// @Produce  json
// @Param    body  body  hostPreviewRequest  true  "preview"
// @Success  200  {object}  HostPreviewResponse
// @Failure  400  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/allowed-hosts/preview [post]
func (h *HostAllowlistHandler) Preview(c *gin.Context) {
	var req hostPreviewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	res, err := h.uc.Preview(c.Request.Context(), req.Pattern, domain.HostKind(req.Kind), req.TestURLs)
	if err != nil {
		// Превью — это «что будет», а не мутация: недописанный/невалидный
		// паттерн (живой ввод в форме) — нормальный ответ «valid:false», а не
		// ошибка клиента. Возвращаем 200 с флагом, чтобы UI показал состояние
		// без сетевого 400 в консоли. Иные ошибки — внутренние (500).
		if code, _, ok := hostErrorCode(err); ok {
			c.JSON(http.StatusOK, gin.H{
				"valid": false, "reason": code,
				"allowed": []string{}, "blocked": []string{},
			})
			return
		}
		h.replyServerError(c, err, "host.preview")
		return
	}
	c.JSON(http.StatusOK, gin.H{"valid": true, "allowed": res.Allowed, "blocked": res.Blocked})
}

// ListByNode godoc
// @Summary  Хосты, привязанные к узлу (§23).
// @Tags     allowed-hosts
// @Produce  json
// @Param    id   path  string  true  "node id"
// @Success  200  {object}  ListHostsResponse
// @Failure  404  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/nodes/{id}/allowed-hosts [get]
func (h *HostAllowlistHandler) ListByNode(c *gin.Context) {
	items, err := h.uc.ListByNode(c.Request.Context(), c.Param("id"), currentTeamID(c))
	if err != nil {
		h.replyDomainError(c, err, "host.list_by_node")
		return
	}
	out := make([]hostResponse, 0, len(items))
	for _, e := range items {
		out = append(out, hostToResponse(e))
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// Attach godoc
// @Summary  Привязать хост к узлу (admin, §23).
// @Tags     allowed-hosts
// @Accept   json
// @Produce  json
// @Param    id    path  string             true  "node id"
// @Param    body  body  attachHostRequest  true  "host_id"
// @Success  204
// @Failure  404  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/nodes/{id}/allowed-hosts [post]
func (h *HostAllowlistHandler) Attach(c *gin.Context) {
	var req attachHostRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.uc.Attach(c.Request.Context(), actorFromCtx(c), c.Param("id"), req.HostID, currentTeamID(c)); err != nil {
		h.replyDomainError(c, err, "host.attach")
		return
	}
	c.Status(http.StatusNoContent)
}

// Detach godoc
// @Summary  Отвязать хост от узла (admin, §23).
// @Tags     allowed-hosts
// @Produce  json
// @Param    id       path  string  true  "node id"
// @Param    host_id  path  string  true  "host id"
// @Success  204
// @Failure  404  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/nodes/{id}/allowed-hosts/{host_id} [delete]
func (h *HostAllowlistHandler) Detach(c *gin.Context) {
	if err := h.uc.Detach(c.Request.Context(), actorFromCtx(c), c.Param("id"), c.Param("host_id"), currentTeamID(c)); err != nil {
		h.replyDomainError(c, err, "host.detach")
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *HostAllowlistHandler) replyDomainError(c *gin.Context, err error, op string) {
	if code, status, ok := hostErrorCode(err); ok {
		c.JSON(status, gin.H{"error": i18n.Translate(i18n.FromGin(c), code), "code": code})
		return
	}
	h.replyServerError(c, err, op)
}

func (h *HostAllowlistHandler) replyServerError(c *gin.Context, err error, op string) {
	h.logger.ErrorWithOp("host allowlist handler error", err, op,
		h.logger.Str("path", c.Request.URL.Path))
	localizedError(c, http.StatusInternalServerError, "error.internal")
}

// hostErrorCode сопоставляет доменную ошибку каталога хостов с i18n-ключом и
// HTTP-статусом. ok=false — ошибка не относится к каталогу (внутренняя).
func hostErrorCode(err error) (code string, status int, ok bool) {
	switch {
	case errors.Is(err, domain.ErrHostNotFound):
		return "host.not_found", http.StatusNotFound, true
	case errors.Is(err, domain.ErrNodeNotFound):
		return "node.not_found", http.StatusNotFound, true
	case errors.Is(err, domain.ErrHostAlreadyExists):
		return "host.already_exists", http.StatusConflict, true
	case errors.Is(err, domain.ErrHostInUse):
		return "host.in_use", http.StatusConflict, true
	case errors.Is(err, domain.ErrHostInvalidKind):
		return "host.invalid_kind", http.StatusBadRequest, true
	case errors.Is(err, domain.ErrHostPatternLength):
		return "host.pattern_length", http.StatusBadRequest, true
	case errors.Is(err, domain.ErrHostExactFormat):
		return "host.exact_format", http.StatusBadRequest, true
	case errors.Is(err, domain.ErrHostWildcardFormat):
		return "host.wildcard_format", http.StatusBadRequest, true
	case errors.Is(err, domain.ErrHostRegexInvalid):
		return "host.regex_invalid", http.StatusBadRequest, true
	case errors.Is(err, domain.ErrHostDescriptionLength):
		return "host.description_length", http.StatusBadRequest, true
	}
	return "", 0, false
}

// atoiDefault парсит положительное целое из query-параметра; пустое/невалидное
// значение → def.
func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return n
	}
	return def
}
