package http

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"nexus/internal/domain"
	"nexus/internal/platform/clientip"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
	"nexus/internal/web/usecase/port"
)

// UserHandler — /api/users/* (admin only).
type UserHandler struct {
	uc     *usecase.UserUsecase
	auth   *usecase.AuthUsecase
	logger logging.Logger
}

func NewUserHandler(uc *usecase.UserUsecase, auth *usecase.AuthUsecase, logger logging.Logger) *UserHandler {
	return &UserHandler{uc: uc, auth: auth, logger: logger}
}

type createUserRequest struct {
	Login              string `json:"login" binding:"required,min=1,max=255"`
	Email              string `json:"email" binding:"omitempty,email,max=255"`
	Password           string `json:"password" binding:"omitempty,min=8,max=128"`
	Role               string `json:"role" binding:"required,oneof=admin viewer manager"`
	Active             bool   `json:"active"`
	Lang               string `json:"lang" binding:"omitempty,oneof=en ru"`
	MustChangePassword bool   `json:"must_change_password"`
}

type updateUserRequest struct {
	Email              string `json:"email" binding:"omitempty,email,max=255"`
	Role               string `json:"role" binding:"required,oneof=admin viewer manager"`
	Active             bool   `json:"active"`
	Lang               string `json:"lang" binding:"omitempty,oneof=en ru"`
	MustChangePassword bool   `json:"must_change_password"`
}

type changePasswordRequest struct {
	NewPassword        string `json:"new_password" binding:"required,min=8,max=128"`
	MustChangePassword bool   `json:"must_change_password"`
}

// setDefaultTeamRequest — §45: смена команды по умолчанию пользователя.
type setDefaultTeamRequest struct {
	TeamID string `json:"team_id" binding:"required"`
}

// userTeamBrief — команда пользователя для колонки «Команды» (§44.G).
type userTeamBrief struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
	Role string `json:"role"`
}

type userResponse struct {
	ID                 string          `json:"id"`
	Login              string          `json:"login"`
	Email              string          `json:"email"`
	Role               string          `json:"role"`
	Active             bool            `json:"active"`
	Lang               string          `json:"lang"`
	MustChangePassword bool            `json:"must_change_password"`
	DefaultTeamID      string          `json:"default_team_id"`
	Teams              []userTeamBrief `json:"teams,omitempty"`
	CreatedAt          time.Time       `json:"created_at"`
	LastLoginAt        *time.Time      `json:"last_login_at,omitempty"`
}

func toUserResp(u *domain.User) userResponse {
	return userResponse{
		ID: u.ID, Login: u.Login, Email: u.Email,
		Role: string(u.Role), Active: u.Active, Lang: string(u.Lang),
		MustChangePassword: u.MustChangePassword, DefaultTeamID: u.DefaultTeamID,
		CreatedAt: u.CreatedAt, LastLoginAt: u.LastLoginAt,
	}
}

// List godoc
// @Summary  Список пользователей (admin only).
// @Tags     users
// @Produce  json
// @Param    search  query  string  false  "поиск по login или email"
// @Success  200     {object}  ListUsersResponse
// @Failure  500     {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/users [get]
func (h *UserHandler) List(c *gin.Context) {
	// §18: список пользователей — глобальный (admin-only), без team-scope.
	// Пользователь — глобальная сущность; членство в командах — отдельная ось
	// (управляется в Teams → Members). Раньше скоупился по currentTeamID(c)
	// (Phase 11.A) — из-за чего в одной команде не было видно юзеров другой.
	users, err := h.uc.ListWithTeams(c.Request.Context(), port.ListUsersFilter{
		Search: c.Query("search"),
	})
	if err != nil {
		h.logger.ErrorWithOp("list users", err, "user.list")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	out := make([]userResponse, 0, len(users))
	for _, uw := range users {
		r := toUserResp(uw.User)
		r.Teams = make([]userTeamBrief, 0, len(uw.Teams))
		for _, ut := range uw.Teams {
			r.Teams = append(r.Teams, userTeamBrief{
				ID: ut.Team.ID, Slug: ut.Team.Slug, Name: ut.Team.Name, Role: string(ut.Role),
			})
		}
		out = append(out, r)
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// Get godoc
// @Summary  Один пользователь по id (admin only).
// @Tags     users
// @Produce  json
// @Param    id   path  string  true  "user id"
// @Success  200  {object}  userResponse
// @Failure  404  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/users/{id} [get]
func (h *UserHandler) Get(c *gin.Context) {
	u, err := h.uc.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		if errors.Is(err, domain.ErrUserNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	c.JSON(http.StatusOK, toUserResp(u))
}

// Create godoc
// @Summary  Создать пользователя (admin only).
// @Tags     users
// @Accept   json
// @Produce  json
// @Param    body  body  createUserRequest  true  "user fields"
// @Success  201   {object}  userResponse
// @Failure  400   {object}  ErrorResponse
// @Failure  409   {object}  ErrorResponse  "login already exists"
// @Security CookieAuth
// @Router   /api/users [post]
func (h *UserHandler) Create(c *gin.Context) {
	var req createUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	u := &domain.User{
		Login: req.Login, Email: req.Email,
		Role: domain.UserRole(req.Role), Active: req.Active,
		Lang: domain.UserLang(req.Lang), MustChangePassword: req.MustChangePassword,
	}
	if u.Lang == "" {
		u.Lang = domain.UserLangEN
	}
	if err := h.uc.Create(c.Request.Context(), userActor(c), currentTeamID(c), u, req.Password); err != nil {
		if errors.Is(err, domain.ErrUserAlreadyExists) {
			c.JSON(http.StatusConflict, gin.H{"error": "login already exists"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, toUserResp(u))
}

// Update godoc
// @Summary  Обновить пользователя (admin only).
// @Description  Нельзя демоутить себя в viewer и нельзя оставлять <1 активного admin (§7.9).
// @Tags     users
// @Accept   json
// @Produce  json
// @Param    id    path  string             true  "user id"
// @Param    body  body  updateUserRequest  true  "user fields"
// @Success  200   {object}  userResponse
// @Failure  400   {object}  ErrorResponse
// @Failure  403   {object}  ErrorResponse  "cannot demote yourself"
// @Failure  404   {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/users/{id} [put]
func (h *UserHandler) Update(c *gin.Context) {
	id := c.Param("id")
	old, err := h.uc.Get(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, domain.ErrUserNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	var req updateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// Запрет самому себе менять свою роль на viewer (§7.9).
	if actor := userActor(c); actor.UserID == id && domain.UserRole(req.Role) != domain.UserRoleAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "cannot demote yourself"})
		return
	}
	updated := *old
	updated.Email = req.Email
	updated.Role = domain.UserRole(req.Role)
	updated.Active = req.Active
	updated.Lang = domain.UserLang(req.Lang)
	updated.MustChangePassword = req.MustChangePassword
	if updated.Lang == "" {
		updated.Lang = domain.UserLangEN
	}
	if err := h.uc.Update(c.Request.Context(), userActor(c), &updated); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, toUserResp(&updated))
}

// SetDefaultTeam godoc
// @Summary  Сменить команду по умолчанию пользователя (admin only, §45).
// @Description  Назначает default_team_id. team_id должен быть среди команд, в
// @Description  которых пользователь состоит (иначе 400) — сделать дефолтной чужую
// @Description  команду нельзя (§18.9 при входе перекинул бы по членству).
// @Tags     users
// @Accept   json
// @Produce  json
// @Param    id    path  string                 true  "user id"
// @Param    body  body  setDefaultTeamRequest  true  "team id"
// @Success  204
// @Failure  400  {object}  ErrorResponse  "не член команды / битый запрос"
// @Failure  404  {object}  ErrorResponse  "пользователь не найден"
// @Security CookieAuth
// @Router   /api/users/{id}/default-team [put]
func (h *UserHandler) SetDefaultTeam(c *gin.Context) {
	var req setDefaultTeamRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	err := h.uc.SetDefaultTeam(c.Request.Context(), userActor(c), c.Param("id"), req.TeamID)
	if err != nil {
		if errors.Is(err, domain.ErrUserNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
			return
		}
		if errors.Is(err, domain.ErrUserNotTeamMember) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "user is not a member of the team"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	c.Status(http.StatusNoContent)
}

// Delete godoc
// @Summary  Удалить пользователя (admin only).
// @Description  Нельзя удалить себя и нельзя оставить <1 активного admin (§7.9).
// @Tags     users
// @Produce  json
// @Param    id   path  string  true  "user id"
// @Success  204
// @Failure  400  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/users/{id} [delete]
func (h *UserHandler) Delete(c *gin.Context) {
	if err := h.uc.Delete(c.Request.Context(), userActor(c), c.Param("id")); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}

// ChangePassword godoc
// @Summary  Сменить пароль пользователя (admin only).
// @Description  Если must_change_password=true, при следующем логине пользователь будет обязан задать новый пароль.
// @Tags     users
// @Accept   json
// @Produce  json
// @Param    id    path  string                  true  "user id"
// @Param    body  body  changePasswordRequest   true  "new password"
// @Success  204
// @Failure  400   {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/users/{id}/password [post]
func (h *UserHandler) ChangePassword(c *gin.Context) {
	var req changePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.auth.ChangePassword(c.Request.Context(), userActor(c),
		c.Param("id"), req.NewPassword, req.MustChangePassword); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}

// userActor — отличается от actorFromCtx тем, что заполняет UserID/TeamID
// из проверенной сессии (если она есть). До auth-middleware — system.
//
// TeamID = s.CurrentTeamID (multi-tenancy v2, Phase 10.F.1): audit-журнал
// получает scope актёра.
func userActor(c *gin.Context) usecase.Actor {
	a := usecase.SystemActor()
	a.IPAddress = clientip.NormalizeIPv4(c.ClientIP())
	if s, ok := sessionFromCtx(c); ok {
		a.UserID = s.UserID
		a.TeamID = s.CurrentTeamID
	}
	return a
}
