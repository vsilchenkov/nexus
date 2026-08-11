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
// @Summary  Список узлов команды (или всех команд пользователя при scope=all).
// @Description  Без scope — узлы текущей команды сессии. scope=all (§86) — узлы ВСЕХ команд, в которых состоит пользователь; только session-cookie, по API-токену 403 (токен закреплён за одной командой).
// @Tags     nodes
// @Produce  json
// @Param    scope        query  string  false  "all — узлы всех команд пользователя (§86)"
// @Param    search       query  string  false  "поиск по path или target_url"
// @Param    root_method  query  string  false  "request | requestAsync"
// @Param    limit        query  int     false  "лимит, max 500"
// @Param    offset       query  int     false  "смещение"
// @Success  200          {object}  ListNodesResponse
// @Failure  403          {object}  ErrorResponse
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

	nodes, ok := h.listNodes(c, f)
	if !ok {
		return // ответ уже записан
	}
	resp := make([]NodeResponse, 0, len(nodes))
	for _, n := range nodes {
		resp = append(resp, nodeToResponse(n))
	}
	c.JSON(http.StatusOK, gin.H{"items": resp})
}

// listNodes — выбор скоупа для List: команда сессии либо все команды
// пользователя при scope=all (§86).
//
// ok=false означает, что ответ клиенту уже записан (403/401 недоступного режима
// или 500 репозитория) и вызывающий обязан прекратить обработку.
func (h *NodeHandler) listNodes(c *gin.Context, f port.ListNodesFilter) ([]*domain.Node, bool) {
	ctx := c.Request.Context()
	if wantsAllTeams(c) {
		userID, allowed := resolveAllTeamsUser(c)
		if !allowed {
			return nil, false
		}
		nodes, err := h.uc.ListAcrossTeams(ctx, userID, f)
		if err != nil {
			h.replyServerError(c, err, "node.list")
			return nil, false
		}
		return nodes, true
	}
	nodes, err := h.uc.List(ctx, f)
	if err != nil {
		h.replyServerError(c, err, "node.list")
		return nil, false
	}
	return nodes, true
}

// Get godoc
// @Summary  Один узел по id.
// @Tags     nodes
// @Produce  json
// @Param    id   path  string  true  "node id"
// @Success  200  {object}  NodeResponse
// @Failure  404  {object}  ErrorResponse
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
// @Failure  400   {object}  ErrorResponse
// @Failure  409   {object}  ErrorResponse  "path already exists"
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
		// §66: подпись автора — отображаемое имя (DisplayName фолбэчит на
		// Login для сессий, созданных до ввода имени). На сессиях без обоих
		// полей оставляем "system" из SystemActor (graceful).
		if dn := s.DisplayName(); dn != "" {
			a.UserLogin = dn
		}
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
// @Failure  400   {object}  ErrorResponse
// @Failure  404   {object}  ErrorResponse
// @Failure  409   {object}  ErrorResponse  "path already exists"
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

// UpdateNodeStatusRequest — тело PATCH /api/nodes/{id}/status (§35).
type UpdateNodeStatusRequest struct {
	Status string `json:"status" binding:"required,oneof=enabled paused disabled"`
}

// UpdateStatus godoc
// @Summary  Сменить только статус узла (§35).
// @Description  Лёгкая замена полного PUT для кнопок «Пауза»/«Отключить». manager+. Меняет лишь status, не трогая прочие поля/креды.
// @Tags     nodes
// @Accept   json
// @Produce  json
// @Param    id    path  string                  true  "node id"
// @Param    body  body  UpdateNodeStatusRequest  true  "enabled | paused | disabled"
// @Success  204
// @Failure  400   {object}  ErrorResponse
// @Failure  404   {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/nodes/{id}/status [patch]
func (h *NodeHandler) UpdateStatus(c *gin.Context) {
	var req UpdateNodeStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.uc.SetStatus(c.Request.Context(), actorFromCtx(c), c.Param("id"), currentTeamID(c), domain.NodeStatus(req.Status)); err != nil {
		h.replyDomainError(c, err, "node.set_status")
		return
	}
	c.Status(http.StatusNoContent)
}

// Delete godoc
// @Summary  Удалить узел.
// @Description  Только admin. Удаляет запись и инвалидирует Redis-кеш. ClickHouse-таблица узла остаётся (см. §7.10 — orphan-cleanup).
// @Tags     nodes
// @Produce  json
// @Param    id   path  string  true  "node id"
// @Success  204
// @Failure  404  {object}  ErrorResponse
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
// @Failure  400   {object}  ErrorResponse
// @Failure  403   {object}  ErrorResponse  "same team / not allowed"
// @Failure  404   {object}  ErrorResponse  "node or target team not found"
// @Failure  409   {object}  ErrorResponse  "path already exists in target team"
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

// MovePreview godoc
// @Summary  Предпросмотр переноса узла: что будет с таблицей логов (admin only).
// @Description  Считается ДО переноса, для предупреждения в диалоге. table_shared — исходную таблицу делят другие узлы (она останется у текущей команды, узлу создадут свою). target_table_exists — в целевой команде уже есть таблица с этим именем, и узел подключится к ней, увидев чужие записи.
// @Tags     nodes
// @Produce  json
// @Param    id                path   string  true  "node id"
// @Param    target_team_slug  query  string  true  "target team slug"
// @Success  200   {object}  usecase.MovePreview
// @Failure  400   {object}  ErrorResponse
// @Failure  403   {object}  ErrorResponse  "same team / not allowed"
// @Failure  404   {object}  ErrorResponse  "node or target team not found"
// @Security CookieAuth
// @Router   /api/nodes/{id}/move-preview [get]
func (h *NodeHandler) MovePreview(c *gin.Context) {
	slug := c.Query("target_team_slug")
	if slug == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "target_team_slug is required"})
		return
	}
	res, err := h.uc.MovePreview(c.Request.Context(), c.Param("id"), currentTeamID(c), slug)
	if err != nil {
		h.replyDomainError(c, err, "node.move_preview")
		return
	}
	c.JSON(http.StatusOK, res)
}

// CopyNodeRequest — тело POST /api/nodes/{id}/copy (§53).
type CopyNodeRequest struct {
	Path string `json:"path" binding:"required,max=255"`
}

// Copy godoc
// @Summary  Скопировать узел (§53).
// @Description  Manager+. Создаёт клон узла с новым path в той же команде: копируются все настройки, включая креды (перешифровка только внутри бэкенда, в ответе значения не возвращаются) и привязки allowlist-хостов. Копия всегда создаётся в статусе paused.
// @Tags     nodes
// @Accept   json
// @Produce  json
// @Param    id    path  string           true  "source node id"
// @Param    body  body  CopyNodeRequest  true  "new path"
// @Success  201   {object}  NodeResponse
// @Failure  400   {object}  ErrorResponse  "invalid path / limit reached"
// @Failure  404   {object}  ErrorResponse  "node not found"
// @Failure  409   {object}  ErrorResponse  "path already exists"
// @Security CookieAuth
// @Router   /api/nodes/{id}/copy [post]
func (h *NodeHandler) Copy(c *gin.Context) {
	var req CopyNodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	n, err := h.uc.Copy(c.Request.Context(), actorFromCtx(c), c.Param("id"), req.Path, currentTeamID(c))
	if err != nil {
		h.replyDomainError(c, err, "node.copy")
		return
	}
	c.JSON(http.StatusCreated, nodeToResponse(n))
}

// ResolveTeamResponse — тело GET /api/nodes/{id}/team (§58): команда, которой
// принадлежит узел. Фронт использует её, чтобы авто-переключить сессию на
// команду узла при открытии шаренной ссылки на страницу узла.
type ResolveTeamResponse struct {
	TeamID   string `json:"team_id"`
	TeamSlug string `json:"team_slug"`
	TeamName string `json:"team_name"`
}

// ResolveTeam godoc
// @Summary  Команда узла для авто-переключения (§58).
// @Description  Возвращает команду, которой принадлежит узел, если вызывающий пользователь состоит в ней. Нужен для шаринга ссылки на страницу узла: фронт узнаёт команду ДО загрузки узла (team-scoped GET вернул бы 404 при несовпадении текущей команды) и переключает сессию. Если узла нет или пользователь не член его команды — 404 (без утечки существования).
// @Tags     nodes
// @Produce  json
// @Param    id   path  string  true  "node id"
// @Success  200  {object}  ResolveTeamResponse
// @Failure  404  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/nodes/{id}/team [get]
func (h *NodeHandler) ResolveTeam(c *gin.Context) {
	s, ok := sessionFromCtx(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	team, err := h.uc.ResolveTeam(c.Request.Context(), s.UserID, c.Param("id"))
	if err != nil {
		h.replyDomainError(c, err, "node.resolve_team")
		return
	}
	c.JSON(http.StatusOK, ResolveTeamResponse{
		TeamID:   team.ID,
		TeamSlug: team.Slug,
		TeamName: team.Name,
	})
}

// NodeSearchItem — один узел в выдаче глобального поиска (§62): минимум для
// строки выпадающего списка + команда-владелец для бейджа и авто-переключения.
type NodeSearchItem struct {
	ID         string `json:"id"`
	Path       string `json:"path"`
	TargetURL  string `json:"target_url"`
	RootMethod string `json:"root_method"`
	Status     string `json:"status"`
	TeamID     string `json:"team_id"`
	TeamSlug   string `json:"team_slug"`
	TeamName   string `json:"team_name"`
}

// SearchNodesResponse — GET /api/search/nodes (§62).
type SearchNodesResponse struct {
	Items []NodeSearchItem `json:"items"`
}

// SearchAcrossTeams godoc
// @Summary  Глобальный поиск узлов по всем командам пользователя.
// @Description  §62: ILIKE по path/target_url в пределах членств пользователя. Каждый узел — с командой-владельцем (для бейджа и авто-переключения при выборе). Только session-cookie: API-токены однокомандные. q короче 2 рун → пустой список (не ошибка).
// @Tags     nodes
// @Produce  json
// @Param    q      query  string  true   "поисковая строка (≥ 2 рун)"
// @Param    limit  query  int     false  "лимит, дефолт 20, max 50"
// @Success  200    {object}  SearchNodesResponse
// @Failure  401    {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/search/nodes [get]
func (h *NodeHandler) SearchAcrossTeams(c *gin.Context) {
	s, ok := sessionFromCtx(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	limit := 0
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	hits, err := h.uc.SearchAcrossTeams(c.Request.Context(), s.UserID, c.Query("q"), limit)
	if err != nil {
		h.replyServerError(c, err, "node.search")
		return
	}
	items := make([]NodeSearchItem, 0, len(hits))
	for _, hit := range hits {
		items = append(items, NodeSearchItem{
			ID:         hit.Node.ID,
			Path:       hit.Node.Path,
			TargetURL:  hit.Node.TargetURL,
			RootMethod: string(hit.Node.RootMethod),
			Status:     string(hit.Node.Status),
			TeamID:     hit.Node.TeamID,
			TeamSlug:   hit.TeamSlug,
			TeamName:   hit.TeamName,
		})
	}
	c.JSON(http.StatusOK, SearchNodesResponse{Items: items})
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
