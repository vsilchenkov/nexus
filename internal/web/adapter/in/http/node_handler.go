package http

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"bus/internal/domain"
	"bus/internal/platform/logging"
	"bus/internal/web/usecase"
	"bus/internal/web/usecase/port"
)

// NodeHandler — HTTP-обработчики для /api/nodes.
type NodeHandler struct {
	uc     *usecase.NodeUsecase
	logger logging.Logger
}

func NewNodeHandler(uc *usecase.NodeUsecase, logger logging.Logger) *NodeHandler {
	return &NodeHandler{uc: uc, logger: logger}
}

// List godoc
// @Summary  Список узлов команды.
// @Tags     nodes
// @Produce  json
// @Param    search       query  string  false  "поиск по path или target_url"
// @Param    root_method  query  string  false  "request | requestAsync"
// @Param    limit        query  int     false  "лимит, max 500"
// @Param    offset       query  int     false  "смещение"
// @Success  200          {object}  map[string]any
// @Security CookieAuth
// @Security ApiTokenAuth
// @Router   /api/nodes [get]
func (h *NodeHandler) List(c *gin.Context) {
	f := port.ListNodesFilter{
		TeamID:     "default",
		Search:     c.Query("search"),
		RootMethod: c.Query("root_method"),
	}
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.Limit = n
		}
	}
	if v := c.Query("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.Offset = n
		}
	}

	nodes, err := h.uc.List(c.Request.Context(), f)
	if err != nil {
		h.replyServerError(c, err, "node.list")
		return
	}
	resp := make([]NodeResponse, 0, len(nodes))
	for _, n := range nodes {
		resp = append(resp, nodeToResponse(n))
	}
	c.JSON(http.StatusOK, gin.H{"items": resp})
}

// Get godoc
// @Summary  Один узел по id.
// @Tags     nodes
// @Produce  json
// @Param    id   path  string  true  "node id"
// @Success  200  {object}  NodeResponse
// @Failure  404  {object}  map[string]string
// @Security CookieAuth
// @Security ApiTokenAuth
// @Router   /api/nodes/{id} [get]
func (h *NodeHandler) Get(c *gin.Context) {
	n, err := h.uc.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.replyDomainError(c, err, "node.get")
		return
	}
	c.JSON(http.StatusOK, nodeToResponse(n))
}

// Create godoc
// @Summary  Создать узел.
// @Description  Только admin. §3.3 ТЗ, лимиты в §3.3.
// @Tags     nodes
// @Accept   json
// @Produce  json
// @Param    body  body  CreateNodeRequest  true  "node config"
// @Success  201   {object}  NodeResponse
// @Failure  400   {object}  map[string]string
// @Failure  409   {object}  map[string]string  "path already exists"
// @Security CookieAuth
// @Router   /api/nodes [post]
func (h *NodeHandler) Create(c *gin.Context) {
	var req CreateNodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	n := reqToDomain(req)
	if err := h.uc.Create(c.Request.Context(), actorFromCtx(c), n); err != nil {
		h.replyDomainError(c, err, "node.create")
		return
	}
	c.JSON(http.StatusCreated, nodeToResponse(n))
}

// actorFromCtx — извлекает Actor для audit-логирования.
// В Phase 2 нет аутентификации → SystemActor (user_login="system") + IP клиента.
// В Phase 3 будет читать user_id/user_login из Gin-context, куда положит
// auth-middleware после проверки session-cookie.
func actorFromCtx(c *gin.Context) usecase.Actor {
	a := usecase.SystemActor()
	a.IPAddress = c.ClientIP()
	return a
}

func (h *NodeHandler) Update(c *gin.Context) {
	id := c.Param("id")
	existing, err := h.uc.Get(c.Request.Context(), id)
	if err != nil {
		h.replyDomainError(c, err, "node.update.get")
		return
	}

	var req UpdateNodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	updated := reqToDomain(req)
	updated.ID = existing.ID
	updated.TeamID = existing.TeamID
	updated.CreatedAt = existing.CreatedAt
	// «Пустые креды в запросе = оставить старое значение» — §5.5 ТЗ.
	if req.AuthCredentials == "" {
		updated.AuthCredentials = existing.AuthCredentials
	}
	if req.IncomingAuthCreds == "" {
		updated.IncomingAuthCredentials = existing.IncomingAuthCredentials
	}

	if err := h.uc.Update(c.Request.Context(), actorFromCtx(c), updated); err != nil {
		h.replyDomainError(c, err, "node.update")
		return
	}
	c.JSON(http.StatusOK, nodeToResponse(updated))
}

func (h *NodeHandler) Delete(c *gin.Context) {
	if err := h.uc.Delete(c.Request.Context(), actorFromCtx(c), c.Param("id")); err != nil {
		h.replyDomainError(c, err, "node.delete")
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *NodeHandler) replyDomainError(c *gin.Context, err error, op string) {
	switch {
	case errors.Is(err, domain.ErrNodeNotFound):
		localizedError(c, http.StatusNotFound, "node.not_found")
	case errors.Is(err, domain.ErrNodeAlreadyExists):
		localizedError(c, http.StatusConflict, "node.already_exists")
	case errors.Is(err, domain.ErrLimitReached):
		localizedError(c, http.StatusBadRequest, "node.limit_reached")
	case isValidationError(err):
		// Сообщение валидации содержит конкретное имя поля — передаём как есть;
		// локализация валидации полей — отдельная работа (Phase 6).
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	default:
		h.replyServerError(c, err, op)
	}
}

func (h *NodeHandler) replyServerError(c *gin.Context, err error, op string) {
	h.logger.ErrorWithOp("web handler error", err, op,
		h.logger.Str("path", c.Request.URL.Path))
	localizedError(c, http.StatusInternalServerError, "error.internal")
}

// isValidationError — все доменные ошибки валидации полей Node.
func isValidationError(err error) bool {
	for _, target := range []error{
		domain.ErrNodeInvalidRootMethod, domain.ErrNodeInvalidURLMode,
		domain.ErrNodeInvalidAuthType, domain.ErrNodeInvalidIncomingAuthType,
		domain.ErrNodeInvalidAuthDynSource, domain.ErrNodeInvalidStatus,
		domain.ErrNodePathLength, domain.ErrNodePathFormat,
		domain.ErrNodeTargetURLLength, domain.ErrNodeStaticNeedsTargetURL,
		domain.ErrNodeParamNameLength, domain.ErrNodeParamNameFormat,
		domain.ErrNodeAuthDynFieldLength, domain.ErrNodeAuthDynFieldFormat,
		domain.ErrNodeTimeoutRange, domain.ErrNodeRetryCountRange,
		domain.ErrNodeRetryBackoffRange, domain.ErrNodeAllowedHostsSize,
		domain.ErrNodeForwardHeadersSize,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}
