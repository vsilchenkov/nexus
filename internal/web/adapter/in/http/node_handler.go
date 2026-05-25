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

func (h *NodeHandler) Get(c *gin.Context) {
	n, err := h.uc.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.replyDomainError(c, err, "node.get")
		return
	}
	c.JSON(http.StatusOK, nodeToResponse(n))
}

func (h *NodeHandler) Create(c *gin.Context) {
	var req CreateNodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	n := reqToDomain(req)
	if err := h.uc.Create(c.Request.Context(), n); err != nil {
		h.replyDomainError(c, err, "node.create")
		return
	}
	c.JSON(http.StatusCreated, nodeToResponse(n))
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

	if err := h.uc.Update(c.Request.Context(), updated); err != nil {
		h.replyDomainError(c, err, "node.update")
		return
	}
	c.JSON(http.StatusOK, nodeToResponse(updated))
}

func (h *NodeHandler) Delete(c *gin.Context) {
	if err := h.uc.Delete(c.Request.Context(), c.Param("id")); err != nil {
		h.replyDomainError(c, err, "node.delete")
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *NodeHandler) replyDomainError(c *gin.Context, err error, op string) {
	switch {
	case errors.Is(err, domain.ErrNodeNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
	case errors.Is(err, domain.ErrNodeAlreadyExists):
		c.JSON(http.StatusConflict, gin.H{"error": "node with this path already exists"})
	case errors.Is(err, domain.ErrLimitReached):
		c.JSON(http.StatusBadRequest, gin.H{"error": "node limit reached, contact administrator"})
	case isValidationError(err):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	default:
		h.replyServerError(c, err, op)
	}
}

func (h *NodeHandler) replyServerError(c *gin.Context, err error, op string) {
	h.logger.ErrorWithOp("web handler error", err, op,
		h.logger.Str("path", c.Request.URL.Path))
	c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
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
