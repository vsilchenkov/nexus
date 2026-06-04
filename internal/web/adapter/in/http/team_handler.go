package http

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
)

// TeamHandler — /api/teams/* (multi-tenancy v2, §16 ТЗ, Phase 10.C).
// Все эндпоинты admin-only.
type TeamHandler struct {
	uc     *usecase.TeamUsecase
	logger logging.Logger
}

func NewTeamHandler(uc *usecase.TeamUsecase, logger logging.Logger) *TeamHandler {
	return &TeamHandler{uc: uc, logger: logger}
}

type teamResponse struct {
	ID         string    `json:"id"`
	Slug       string    `json:"slug"`
	Name       string    `json:"name"`
	CHDatabase string    `json:"ch_database"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type teamMemberResponse struct {
	UserID    string    `json:"user_id"`
	TeamID    string    `json:"team_id"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

type createTeamRequest struct {
	Slug string `json:"slug" binding:"required,min=1,max=32"`
	Name string `json:"name" binding:"required,min=1,max=255"`
}

type updateTeamRequest struct {
	Name string `json:"name" binding:"required,min=1,max=255"`
}

type addMemberRequest struct {
	UserID string `json:"user_id" binding:"required,uuid"`
	Role   string `json:"role" binding:"required,oneof=owner admin member"`
}

type updateMemberRoleRequest struct {
	Role string `json:"role" binding:"required,oneof=owner admin member"`
}

func toTeamResp(t *domain.Team) teamResponse {
	return teamResponse{
		ID: t.ID, Slug: t.Slug, Name: t.Name, CHDatabase: t.CHDatabase,
		CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
}

// List godoc
// @Summary  Список команд (admin only).
// @Tags     teams
// @Produce  json
// @Success  200  {object}  map[string]any
// @Security CookieAuth
// @Router   /api/teams [get]
func (h *TeamHandler) List(c *gin.Context) {
	teams, err := h.uc.List(c.Request.Context())
	if err != nil {
		h.logger.ErrorWithOp("list teams", err, "team.list")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	out := make([]teamResponse, 0, len(teams))
	for _, t := range teams {
		out = append(out, toTeamResp(t))
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// Get godoc
// @Summary  Одна команда по id (admin only).
// @Tags     teams
// @Produce  json
// @Param    id   path  string  true  "team id"
// @Success  200  {object}  teamResponse
// @Failure  404  {object}  map[string]string
// @Security CookieAuth
// @Router   /api/teams/{id} [get]
func (h *TeamHandler) Get(c *gin.Context) {
	t, err := h.uc.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.replyTeamError(c, err)
		return
	}
	c.JSON(http.StatusOK, toTeamResp(t))
}

// Create godoc
// @Summary  Создать команду (admin only).
// @Description  Атомарно: row в teams + CREATE DATABASE nexus_<slug>. ch_database вычисляется из slug. Creator → owner.
// @Tags     teams
// @Accept   json
// @Produce  json
// @Param    body  body  createTeamRequest  true  "slug + name"
// @Success  201   {object}  teamResponse
// @Failure  400   {object}  map[string]string
// @Failure  409   {object}  map[string]string  "slug or ch_database exists"
// @Failure  503   {object}  map[string]string  "ClickHouse unavailable"
// @Security CookieAuth
// @Router   /api/teams [post]
func (h *TeamHandler) Create(c *gin.Context) {
	var req createTeamRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	s, _ := sessionFromCtx(c)
	t, err := h.uc.Create(c.Request.Context(), userActor(c), req.Slug, req.Name, s.UserID)
	if err != nil {
		h.replyTeamError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toTeamResp(t))
}

// Update godoc
// @Summary  Обновить имя команды (admin only).
// @Description  Slug и ch_database immutable после создания.
// @Tags     teams
// @Accept   json
// @Produce  json
// @Param    id    path  string             true  "team id"
// @Param    body  body  updateTeamRequest  true  "name"
// @Success  200   {object}  teamResponse
// @Security CookieAuth
// @Router   /api/teams/{id} [put]
func (h *TeamHandler) Update(c *gin.Context) {
	var req updateTeamRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	t, err := h.uc.Update(c.Request.Context(), userActor(c), c.Param("id"), req.Name)
	if err != nil {
		h.replyTeamError(c, err)
		return
	}
	c.JSON(http.StatusOK, toTeamResp(t))
}

// Delete godoc
// @Summary  Удалить команду (admin only).
// @Description  default-team удалить нельзя. ON DELETE RESTRICT на nodes — если в команде есть узлы, удаление вернёт 409. ClickHouse-БД остаётся как orphan (drop через UI «Orphans», Phase 10.D).
// @Tags     teams
// @Param    id   path  string  true  "team id"
// @Success  204
// @Failure  403  {object}  map[string]string  "cannot delete default team"
// @Failure  404  {object}  map[string]string
// @Failure  409  {object}  map[string]string  "team has nodes"
// @Security CookieAuth
// @Router   /api/teams/{id} [delete]
func (h *TeamHandler) Delete(c *gin.Context) {
	if err := h.uc.Delete(c.Request.Context(), userActor(c), c.Param("id")); err != nil {
		h.replyTeamError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ListMembers godoc
// @Summary  Список членов команды (admin only).
// @Tags     teams
// @Produce  json
// @Param    id   path  string  true  "team id"
// @Success  200  {object}  map[string]any
// @Security CookieAuth
// @Router   /api/teams/{id}/members [get]
func (h *TeamHandler) ListMembers(c *gin.Context) {
	members, err := h.uc.ListMembers(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.logger.ErrorWithOp("list members", err, "team.members.list")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	out := make([]teamMemberResponse, 0, len(members))
	for _, m := range members {
		out = append(out, teamMemberResponse{
			UserID: m.UserID, TeamID: m.TeamID,
			Role: string(m.Role), CreatedAt: m.CreatedAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// AddMember godoc
// @Summary  Добавить пользователя в команду (admin only).
// @Description  Если membership уже есть — обновляется роль (UPSERT через ON CONFLICT).
// @Tags     teams
// @Accept   json
// @Param    id    path  string            true  "team id"
// @Param    body  body  addMemberRequest  true  "user_id + role"
// @Success  204
// @Security CookieAuth
// @Router   /api/teams/{id}/members [post]
func (h *TeamHandler) AddMember(c *gin.Context) {
	var req addMemberRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.uc.AddMember(c.Request.Context(), userActor(c),
		c.Param("id"), req.UserID, domain.TeamRole(req.Role)); err != nil {
		h.replyTeamError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// UpdateMemberRole godoc
// @Summary  Изменить роль пользователя в команде (admin only).
// @Tags     teams
// @Accept   json
// @Param    id       path  string                   true  "team id"
// @Param    user_id  path  string                   true  "user id"
// @Param    body     body  updateMemberRoleRequest  true  "role"
// @Success  204
// @Security CookieAuth
// @Router   /api/teams/{id}/members/{user_id} [put]
func (h *TeamHandler) UpdateMemberRole(c *gin.Context) {
	var req updateMemberRoleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.uc.UpdateMemberRole(c.Request.Context(), userActor(c),
		c.Param("id"), c.Param("user_id"), domain.TeamRole(req.Role)); err != nil {
		h.replyTeamError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// RemoveMember godoc
// @Summary  Убрать пользователя из команды (admin only).
// @Tags     teams
// @Param    id       path  string  true  "team id"
// @Param    user_id  path  string  true  "user id"
// @Success  204
// @Security CookieAuth
// @Router   /api/teams/{id}/members/{user_id} [delete]
func (h *TeamHandler) RemoveMember(c *gin.Context) {
	if err := h.uc.RemoveMember(c.Request.Context(), userActor(c),
		c.Param("id"), c.Param("user_id")); err != nil {
		h.replyTeamError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *TeamHandler) replyTeamError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, domain.ErrTeamNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "team not found"})
	case errors.Is(err, domain.ErrTeamMemberNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "team member not found"})
	case errors.Is(err, domain.ErrTeamAlreadyExists):
		c.JSON(http.StatusConflict, gin.H{"error": "team with this slug or ch_database already exists"})
	case errors.Is(err, domain.ErrTeamHasNodes):
		// П17: команду с привязанными узлами удалить нельзя (FK RESTRICT).
		c.JSON(http.StatusConflict, gin.H{"error": "team has attached nodes — move or delete them first"})
	case errors.Is(err, domain.ErrTeamSlugFormat),
		errors.Is(err, domain.ErrTeamNameLength),
		errors.Is(err, domain.ErrTeamCHDatabaseFormat),
		errors.Is(err, domain.ErrTeamInvalidRole):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, usecase.ErrCHUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "ClickHouse unavailable, cannot provision team database"})
	case errors.Is(err, domain.ErrPermissionDenied):
		c.JSON(http.StatusForbidden, gin.H{"error": "operation not allowed"})
	default:
		h.logger.ErrorWithOp("team handler", err, "team.error")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
	}
}
