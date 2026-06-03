package http

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"nexus/internal/domain"
	"nexus/internal/platform/clientip"
	"nexus/internal/platform/i18n"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
	"nexus/internal/web/usecase/port"
)

// RMQHealthReader — порт чтения runtime-health Puller-воркера узла
// RabbitMQAsync (§27.8). Реализуется Redis-адаптером; nil — health не отдаётся.
type RMQHealthReader interface {
	Get(ctx context.Context, nodePath string) (*domain.RMQHealth, error)
}

// NodeHandler — HTTP-обработчики для /api/nodes.
type NodeHandler struct {
	uc     *usecase.NodeUsecase
	health RMQHealthReader
	logger logging.Logger
}

func NewNodeHandler(uc *usecase.NodeUsecase, health RMQHealthReader, logger logging.Logger) *NodeHandler {
	return &NodeHandler{uc: uc, health: health, logger: logger}
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
		TeamID:     currentTeamID(c),
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
	n, err := h.uc.Get(c.Request.Context(), c.Param("id"), currentTeamID(c))
	if err != nil {
		h.replyDomainError(c, err, "node.get")
		return
	}
	resp := nodeToResponse(n)
	// §27.8: для RabbitMQAsync доклеиваем runtime-health из общего стора.
	if n.RootMethod.IsPull() && h.health != nil {
		if hp, herr := h.health.Get(c.Request.Context(), n.Path); herr == nil && hp != nil {
			resp.RMQStatus = rmqHealthToDTO(hp)
		}
	}
	c.JSON(http.StatusOK, resp)
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
	n.TeamID = currentTeamID(c)
	if err := h.uc.Create(c.Request.Context(), actorFromCtx(c), n); err != nil {
		h.replyDomainError(c, err, "node.create")
		return
	}
	c.JSON(http.StatusCreated, nodeToResponse(n))
}

// actorFromCtx — извлекает Actor для audit-логирования.
// До auth-middleware — SystemActor (user_login="system") + IP клиента.
// При наличии сессии — UserID + CurrentTeamID (multi-tenancy v2,
// Phase 10.F.1). Phase 1 комментарий устарел: middleware теперь
// гарантированно ставит сессию в ctx до handler'а.
func actorFromCtx(c *gin.Context) usecase.Actor {
	a := usecase.SystemActor()
	a.IPAddress = clientip.NormalizeIPv4(c.ClientIP())
	if s, ok := sessionFromCtx(c); ok {
		a.UserID = s.UserID
		a.TeamID = s.CurrentTeamID
	}
	return a
}

// Update godoc
// @Summary  Обновить узел.
// @Description  Только admin. Пустые auth_credentials/incoming_auth_credentials в body означают «оставить старое значение» (§5.5 ТЗ).
// @Tags     nodes
// @Accept   json
// @Produce  json
// @Param    id    path  string             true  "node id"
// @Param    body  body  UpdateNodeRequest  true  "node config"
// @Success  200   {object}  NodeResponse
// @Failure  400   {object}  map[string]string
// @Failure  404   {object}  map[string]string
// @Failure  409   {object}  map[string]string  "path already exists"
// @Security CookieAuth
// @Router   /api/nodes/{id} [put]
func (h *NodeHandler) Update(c *gin.Context) {
	id := c.Param("id")
	team := currentTeamID(c)
	existing, err := h.uc.Get(c.Request.Context(), id, team)
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
	// §27: пустой rmq_password = оставить старый (как остальные креды).
	if req.RMQPassword == "" {
		updated.RMQPassword = existing.RMQPassword
	}

	if err := h.uc.Update(c.Request.Context(), actorFromCtx(c), updated, team); err != nil {
		h.replyDomainError(c, err, "node.update")
		return
	}
	c.JSON(http.StatusOK, nodeToResponse(updated))
}

// Delete godoc
// @Summary  Удалить узел.
// @Description  Только admin. Удаляет запись и инвалидирует Redis-кеш. ClickHouse-таблица узла остаётся (см. §7.10 — orphan-cleanup).
// @Tags     nodes
// @Produce  json
// @Param    id   path  string  true  "node id"
// @Success  204
// @Failure  404  {object}  map[string]string
// @Security CookieAuth
// @Router   /api/nodes/{id} [delete]
func (h *NodeHandler) Delete(c *gin.Context) {
	if err := h.uc.Delete(c.Request.Context(), actorFromCtx(c), c.Param("id"), currentTeamID(c)); err != nil {
		h.replyDomainError(c, err, "node.delete")
		return
	}
	c.Status(http.StatusNoContent)
}

// MoveNodeRequest — тело POST /api/nodes/{id}/move.
type MoveNodeRequest struct {
	TargetTeamSlug string `json:"target_team_slug" binding:"required"`
}

// Move godoc
// @Summary  Перенести узел в другую команду (admin only, multi-tenancy v2).
// @Description  Меняет team_id узла и переносит ClickHouse-таблицу логов (RENAME TABLE, best-effort). Конфликт пути в целевой команде → 409.
// @Tags     nodes
// @Accept   json
// @Produce  json
// @Param    id    path  string           true  "node id"
// @Param    body  body  MoveNodeRequest  true  "target team slug"
// @Success  204
// @Failure  400   {object}  map[string]string
// @Failure  403   {object}  map[string]string  "same team / not allowed"
// @Failure  404   {object}  map[string]string  "node or target team not found"
// @Failure  409   {object}  map[string]string  "path already exists in target team"
// @Security CookieAuth
// @Router   /api/nodes/{id}/move [post]
func (h *NodeHandler) Move(c *gin.Context) {
	var req MoveNodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	err := h.uc.Move(c.Request.Context(), actorFromCtx(c), c.Param("id"),
		currentTeamID(c), req.TargetTeamSlug)
	if err != nil {
		h.replyDomainError(c, err, "node.move")
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *NodeHandler) replyDomainError(c *gin.Context, err error, op string) {
	switch {
	case errors.Is(err, domain.ErrNodeNotFound),
		errors.Is(err, domain.ErrTeamNotFound):
		localizedError(c, http.StatusNotFound, "node.not_found")
	case errors.Is(err, domain.ErrPermissionDenied):
		c.JSON(http.StatusForbidden, gin.H{"error": "operation not allowed"})
	case errors.Is(err, domain.ErrNodeAlreadyExists):
		localizedError(c, http.StatusConflict, "node.already_exists")
	case errors.Is(err, domain.ErrLimitReached):
		localizedError(c, http.StatusBadRequest, "node.limit_reached")
	case isValidationError(err):
		// §28 Пункт 5: локализованное сообщение + код + имя поля для inline-вывода
		// у конкретного поля формы. "error" — fallback, "code" фронт переводит в
		// языке UI, "field" подсвечивает поле.
		code, field, _ := nodeValidationCode(err)
		c.JSON(http.StatusBadRequest, gin.H{
			"error": i18n.Translate(i18n.FromGin(c), code),
			"code":  code,
			"field": field,
		})
	default:
		h.replyServerError(c, err, op)
	}
}

func (h *NodeHandler) replyServerError(c *gin.Context, err error, op string) {
	h.logger.ErrorWithOp("web handler error", err, op,
		h.logger.Str("path", c.Request.URL.Path))
	localizedError(c, http.StatusInternalServerError, "error.internal")
}

// isValidationError — доменная ошибка валидации поля Node. Единый источник —
// карта nodeValidationErrors (node_validation.go): если ошибка в ней есть,
// это валидация конкретного поля.
func isValidationError(err error) bool {
	_, _, ok := nodeValidationCode(err)
	return ok
}
