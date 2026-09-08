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

// NodeGroupHandler — справочник групп узлов (§99). GET доступен любой сессии
// (экран «Узлы» группирует список для всех ролей, включая viewer); мутации —
// manager+ («Управление узлами»): менеджер и так создаёт группы из формы узла,
// поэтому страница управления от него не прячется.
type NodeGroupHandler struct {
	uc     *usecase.NodeGroupUsecase
	logger logging.Logger
}

func NewNodeGroupHandler(uc *usecase.NodeGroupUsecase, logger logging.Logger) *NodeGroupHandler {
	return &NodeGroupHandler{uc: uc, logger: logger}
}

type nodeGroupRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	SortOrder   int32  `json:"sort_order"`
}

type nodeGroupMoveRequest struct {
	Direction string `json:"direction" binding:"required,oneof=up down"`
}

type nodeGroupResponse struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	SortOrder   int32     `json:"sort_order"`
	UsageCount  int       `json:"usage_count"`
	CreatedBy   string    `json:"created_by"`
	UpdatedBy   string    `json:"updated_by"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func nodeGroupToResponse(g *domain.NodeGroup) nodeGroupResponse {
	return nodeGroupResponse{
		ID: g.ID, Name: g.Name, Description: g.Description, SortOrder: g.SortOrder,
		UsageCount: g.UsageCount, CreatedBy: g.CreatedBy, UpdatedBy: g.UpdatedBy,
		CreatedAt: g.CreatedAt, UpdatedAt: g.UpdatedAt,
	}
}

// List godoc
// @Summary  Справочник групп узлов (§99).
// @Description  Порядок вывода — sort_order, затем имя. Поиск — по подстроке имени без учёта регистра. Доступен любой сессии: экран «Узлы» группирует список для всех ролей.
// @Tags     node-groups
// @Produce  json
// @Param    q      query  string  false  "поиск подстроки в имени; пусто = весь справочник"
// @Param    limit  query  int     false  "лимит (по умолчанию 200)"
// @Success  200  {object}  ListNodeGroupsResponse
// @Security CookieAuth
// @Router   /api/node-groups [get]
func (h *NodeGroupHandler) List(c *gin.Context) {
	items, err := h.uc.List(c.Request.Context(), c.Query("q"), atoiDefault(c.Query("limit"), 200))
	if err != nil {
		h.replyServerError(c, err, "node_group.list")
		return
	}
	out := make([]nodeGroupResponse, 0, len(items))
	for _, g := range items {
		out = append(out, nodeGroupToResponse(g))
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// Create godoc
// @Summary  Создать группу узлов (manager, §99).
// @Description  Идемпотентно по case-insensitive имени: повтор вернёт существующую запись (200), а не ошибку — комбобокс формы узла создаёт группы без диалогов.
// @Tags     node-groups
// @Accept   json
// @Produce  json
// @Param    body  body  nodeGroupRequest  true  "node group"
// @Success  200  {object}  nodeGroupResponse
// @Failure  400  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/node-groups [post]
func (h *NodeGroupHandler) Create(c *gin.Context) {
	var req nodeGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	actor := actorFromCtx(c)
	g := &domain.NodeGroup{
		Name:        req.Name,
		Description: req.Description,
		SortOrder:   req.SortOrder,
		CreatedBy:   actor.UserLogin,
		UpdatedBy:   actor.UserLogin,
	}
	if err := h.uc.Create(c.Request.Context(), actor, g); err != nil {
		h.replyDomainError(c, err, "node_group.create")
		return
	}
	c.JSON(http.StatusOK, nodeGroupToResponse(g))
}

// Update godoc
// @Summary  Изменить группу узлов (manager, §99).
// @Description  Имя, описание и порядок меняются всегда: узлы ссылаются на группу по id, поэтому переименование ничего не осиротит (в отличие от заголовков §24). Занятое имя — 409.
// @Tags     node-groups
// @Accept   json
// @Produce  json
// @Param    id    path  string            true  "node group id"
// @Param    body  body  nodeGroupRequest  true  "node group"
// @Success  204
// @Failure  400  {object}  ErrorResponse
// @Failure  404  {object}  ErrorResponse
// @Failure  409  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/node-groups/{id} [patch]
func (h *NodeGroupHandler) Update(c *gin.Context) {
	var req nodeGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	err := h.uc.Update(c.Request.Context(), actorFromCtx(c), c.Param("id"), req.Name, req.Description, req.SortOrder)
	if err != nil {
		h.replyDomainError(c, err, "node_group.update")
		return
	}
	c.Status(http.StatusNoContent)
}

// Delete godoc
// @Summary  Удалить группу узлов (manager, §99).
// @Description  Нельзя удалить группу, к которой привязаны узлы (409) — сначала уберите узлы из группы.
// @Tags     node-groups
// @Produce  json
// @Param    id   path  string  true  "node group id"
// @Success  204
// @Failure  404  {object}  ErrorResponse
// @Failure  409  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/node-groups/{id} [delete]
func (h *NodeGroupHandler) Delete(c *gin.Context) {
	if err := h.uc.Delete(c.Request.Context(), actorFromCtx(c), c.Param("id")); err != nil {
		h.replyDomainError(c, err, "node_group.delete")
		return
	}
	c.Status(http.StatusNoContent)
}

// Move godoc
// @Summary  Сдвинуть группу в порядке вывода (manager, §99).
// @Description  Сдвиг на одну позицию вверх или вниз. Порядок переприсваивается целиком, поэтому работает и на группах с одинаковым sort_order. Сдвиг за край списка — no-op.
// @Tags     node-groups
// @Accept   json
// @Produce  json
// @Param    id    path  string                true  "node group id"
// @Param    body  body  nodeGroupMoveRequest  true  "direction"
// @Success  204
// @Failure  400  {object}  ErrorResponse
// @Failure  404  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/node-groups/{id}/move [post]
func (h *NodeGroupHandler) Move(c *gin.Context) {
	var req nodeGroupMoveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	err := h.uc.Move(c.Request.Context(), actorFromCtx(c), c.Param("id"), usecase.MoveDirection(req.Direction))
	if err != nil {
		h.replyDomainError(c, err, "node_group.move")
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *NodeGroupHandler) replyDomainError(c *gin.Context, err error, op string) {
	switch {
	case errors.Is(err, domain.ErrNodeGroupNameLength):
		h.replyCode(c, http.StatusBadRequest, "node_group.name_length")
	case errors.Is(err, domain.ErrNodeGroupDescriptionLength):
		h.replyCode(c, http.StatusBadRequest, "node_group.description_length")
	case errors.Is(err, domain.ErrNodeGroupSortOrderRange):
		h.replyCode(c, http.StatusBadRequest, "node_group.sort_order_range")
	case errors.Is(err, domain.ErrNodeGroupInvalidDirection):
		h.replyCode(c, http.StatusBadRequest, "node_group.invalid_direction")
	case errors.Is(err, domain.ErrNodeGroupNotFound):
		h.replyCode(c, http.StatusNotFound, "node_group.not_found")
	case errors.Is(err, domain.ErrNodeGroupAlreadyExists):
		h.replyCode(c, http.StatusConflict, "node_group.already_exists")
	case errors.Is(err, domain.ErrNodeGroupInUse):
		h.replyCode(c, http.StatusConflict, "node_group.in_use")
	case errors.Is(err, domain.ErrNodeGroupTooManyToReorder):
		h.replyCode(c, http.StatusConflict, "node_group.too_many_to_reorder")
	default:
		h.replyServerError(c, err, op)
	}
}

func (h *NodeGroupHandler) replyCode(c *gin.Context, status int, code string) {
	c.JSON(status, gin.H{"error": i18n.Translate(i18n.FromGin(c), code), "code": code})
}

func (h *NodeGroupHandler) replyServerError(c *gin.Context, err error, op string) {
	h.logger.ErrorWithOp("node group handler error", err, op,
		h.logger.Str("path", c.Request.URL.Path))
	localizedError(c, http.StatusInternalServerError, "error.internal")
}
