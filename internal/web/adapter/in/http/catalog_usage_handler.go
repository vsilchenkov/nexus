package http

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
)

// CatalogUsageHandler — узлы, использующие элемент справочника (§104): диалог
// за счётчиком «узлов: N» в Настройках. Три тонких маршрута, по одному на
// справочник; вид справочника задаёт маршрут, а не параметр запроса.
//
// Только сессия (RequireSessionOnly): выдача строится по членствам
// пользователя, а API-токен закреплён за одной командой — как /search/nodes
// (§62). Любая роль: usage_count и так отдаёт GET справочника любой сессии.
type CatalogUsageHandler struct {
	uc     *usecase.CatalogUsageUsecase
	logger logging.Logger
}

func NewCatalogUsageHandler(uc *usecase.CatalogUsageUsecase, logger logging.Logger) *CatalogUsageHandler {
	return &CatalogUsageHandler{uc: uc, logger: logger}
}

// CatalogNodeItem — строка диалога: узел и команда-владелец для бейджа.
type CatalogNodeItem struct {
	ID         string `json:"id"`
	Path       string `json:"path"`
	TargetURL  string `json:"target_url"`
	URLMode    string `json:"url_mode"`
	RootMethod string `json:"root_method"`
	Status     string `json:"status"`
	TeamID     string `json:"team_id"`
	TeamSlug   string `json:"team_slug"`
	TeamName   string `json:"team_name"`
}

// CatalogNodesResponse — GET /api/{node-groups|headers|allowed-hosts}/{id}/nodes (§104.3).
type CatalogNodesResponse struct {
	// Items — узлы команд-членств, не больше 500, по path.
	Items []CatalogNodeItem `json:"items"`
	// Total — узлы всех команд тем же предикатом, что usage_count справочника.
	Total int `json:"total"`
	// Visible — все узлы команд-членств; больше len(items) — список усечён.
	Visible int `json:"visible"`
	// Hidden — узлы команд, где пользователя нет: только число.
	Hidden int `json:"hidden"`
}

// GroupNodes godoc
// @Summary  Узлы группы (§104).
// @Description  Узлы команд, где пользователь состоит, — списком (до 500, по path); узлы остальных команд — только числом hidden. total считается тем же предикатом, что usage_count группы. Только session-cookie, любая роль.
// @Tags     node-groups
// @Produce  json
// @Param    id   path  string  true  "id группы"
// @Success  200  {object}  CatalogNodesResponse
// @Failure  401  {object}  ErrorResponse
// @Failure  403  {object}  ErrorResponse  "API-токен"
// @Failure  404  {object}  ErrorResponse  "группы нет или id не UUID"
// @Security CookieAuth
// @Router   /api/node-groups/{id}/nodes [get]
func (h *CatalogUsageHandler) GroupNodes(c *gin.Context) {
	h.nodes(c, usecase.CatalogKindGroup, domain.ErrNodeGroupNotFound, "node_group.not_found")
}

// HeaderNodes godoc
// @Summary  Узлы, пробрасывающие заголовок (§104).
// @Description  Узлы команд, где пользователь состоит, — списком (до 500, по path); узлы остальных команд — только числом hidden. Имя в forward_headers узла сравнивается без учёта регистра, как в usage_count. Только session-cookie, любая роль.
// @Tags     headers
// @Produce  json
// @Param    id   path  string  true  "id заголовка"
// @Success  200  {object}  CatalogNodesResponse
// @Failure  401  {object}  ErrorResponse
// @Failure  403  {object}  ErrorResponse  "API-токен"
// @Failure  404  {object}  ErrorResponse  "заголовка нет или id не UUID"
// @Security CookieAuth
// @Router   /api/headers/{id}/nodes [get]
func (h *CatalogUsageHandler) HeaderNodes(c *gin.Context) {
	h.nodes(c, usecase.CatalogKindHeader, domain.ErrHeaderNotFound, "header.not_found")
}

// HostNodes godoc
// @Summary  Узлы, к которым привязан разрешённый хост (§104).
// @Description  Узлы команд, где пользователь состоит, — списком (до 500, по path); узлы остальных команд — только числом hidden. url_mode строки показывает, действует ли привязка: Receiver проверяет allowlist только при from_request. Только session-cookie, любая роль.
// @Tags     allowed-hosts
// @Produce  json
// @Param    id   path  string  true  "id разрешённого хоста"
// @Success  200  {object}  CatalogNodesResponse
// @Failure  401  {object}  ErrorResponse
// @Failure  403  {object}  ErrorResponse  "API-токен"
// @Failure  404  {object}  ErrorResponse  "хоста нет или id не UUID"
// @Security CookieAuth
// @Router   /api/allowed-hosts/{id}/nodes [get]
func (h *CatalogUsageHandler) HostNodes(c *gin.Context) {
	h.nodes(c, usecase.CatalogKindHost, domain.ErrHostNotFound, "host.not_found")
}

func (h *CatalogUsageHandler) nodes(c *gin.Context, kind usecase.CatalogKind, notFound error, notFoundKey string) {
	s, ok := sessionFromCtx(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	res, err := h.uc.NodesUsing(c.Request.Context(), kind, c.Param("id"), s.UserID)
	switch {
	case errors.Is(err, notFound):
		localizedError(c, http.StatusNotFound, notFoundKey)
		return
	case err != nil:
		logServerError(c, h.logger, "web handler error", err, "catalog_usage."+string(kind),
			h.logger.Str("path", c.Request.URL.Path))
		localizedError(c, http.StatusInternalServerError, "error.internal")
		return
	}
	c.JSON(http.StatusOK, catalogNodesToResponse(res))
}

func catalogNodesToResponse(res usecase.CatalogNodes) CatalogNodesResponse {
	items := make([]CatalogNodeItem, 0, len(res.Items))
	for _, n := range res.Items {
		items = append(items, CatalogNodeItem{
			ID:         n.ID,
			Path:       n.Path,
			TargetURL:  n.TargetURL,
			URLMode:    string(n.URLMode),
			RootMethod: string(n.RootMethod),
			Status:     string(n.Status),
			TeamID:     n.TeamID,
			TeamSlug:   n.TeamSlug,
			TeamName:   n.TeamName,
		})
	}
	return CatalogNodesResponse{Items: items, Total: res.Total, Visible: res.Visible, Hidden: res.Hidden}
}
