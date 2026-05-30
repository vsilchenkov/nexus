package http

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"nexus/internal/domain"
	"nexus/internal/platform/config"
	"nexus/internal/platform/i18n"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
)

// AuthHandler — /api/auth/* (login/logout/me).
type AuthHandler struct {
	uc     *usecase.AuthUsecase
	cfg    *config.WebSection
	ttl    time.Duration
	logger logging.Logger
}

func NewAuthHandler(
	uc *usecase.AuthUsecase,
	cfg *config.WebSection,
	ttl time.Duration,
	logger logging.Logger,
) *AuthHandler {
	return &AuthHandler{uc: uc, cfg: cfg, ttl: ttl, logger: logger}
}

type loginRequest struct {
	Login    string `json:"login" binding:"required,min=1,max=255"`
	Password string `json:"password" binding:"required,min=1,max=128"`
}

type meResponse struct {
	UserID             string `json:"user_id"`
	Login              string `json:"login"`
	Email              string `json:"email,omitempty"`
	Role               string `json:"role"`
	Lang               string `json:"lang"`
	MustChangePassword bool   `json:"must_change_password"`
	DefaultTeamID      string `json:"default_team_id"`
	CurrentTeamID      string `json:"current_team_id"`
}

// teamMembershipResponse — элемент списка GET /api/me/teams.
type teamMembershipResponse struct {
	ID         string `json:"id"`
	Slug       string `json:"slug"`
	Name       string `json:"name"`
	CHDatabase string `json:"ch_database"`
	Role       string `json:"role"`
}

type switchTeamRequest struct {
	TeamID string `json:"team_id" binding:"required,uuid"`
}

type changeOwnPasswordRequest struct {
	CurrentPassword string `json:"current_password" binding:"required,min=1,max=128"`
	NewPassword     string `json:"new_password" binding:"required,min=8,max=128"`
}

// Login godoc
// @Summary  Логин по логину/паролю.
// @Description  При успехе ставит HttpOnly cookie nexus_session (§7.1 ТЗ).
// @Tags     auth
// @Accept   json
// @Produce  json
// @Param    body  body  loginRequest  true  "credentials"
// @Success  200   {object}  map[string]any
// @Failure  400   {object}  map[string]string
// @Failure  401   {object}  map[string]string  "invalid credentials"
// @Failure  403   {object}  map[string]string  "user inactive"
// @Router   /api/auth/login [post]
func (h *AuthHandler) Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	token, user, err := h.uc.Login(c.Request.Context(), req.Login, req.Password, c.ClientIP())
	if err != nil {
		lang := i18n.FromGin(c)
		switch {
		case errors.Is(err, domain.ErrUnauthorized):
			c.JSON(http.StatusUnauthorized, gin.H{"error": i18n.Translate(lang, "auth.invalid_credentials")})
		case errors.Is(err, domain.ErrUserInactive):
			c.JSON(http.StatusForbidden, gin.H{"error": i18n.Translate(lang, "auth.user_inactive")})
		default:
			h.logger.ErrorWithOp("login failed", err, "auth.login")
			c.JSON(http.StatusInternalServerError, gin.H{"error": i18n.Translate(lang, "error.internal")})
		}
		return
	}

	sameSite := http.SameSiteStrictMode
	switch h.cfg.SessionCookieSamesite {
	case "lax":
		sameSite = http.SameSiteLaxMode
	case "none":
		sameSite = http.SameSiteNoneMode
	}
	c.SetSameSite(sameSite)
	c.SetCookie(h.cfg.SessionCookieName, token, int(h.ttl.Seconds()), "/", "",
		h.cfg.SessionCookieSecure, true)

	c.JSON(http.StatusOK, gin.H{
		"user": meResponse{
			UserID: user.ID, Login: user.Login, Role: string(user.Role),
			Lang: string(user.Lang), MustChangePassword: user.MustChangePassword,
			DefaultTeamID: user.DefaultTeamID,
			// CurrentTeamID на момент логина = DefaultTeamID.
			CurrentTeamID: user.DefaultTeamID,
		},
	})
}

// Logout godoc
// @Summary  Завершить сессию.
// @Description  Удаляет session-cookie nexus_session и инвалидирует токен в Redis (§7.1 ТЗ).
// @Tags     auth
// @Produce  json
// @Success  204
// @Security CookieAuth
// @Router   /api/auth/logout [post]
func (h *AuthHandler) Logout(c *gin.Context) {
	token, err := c.Cookie(h.cfg.SessionCookieName)
	if err == nil && token != "" {
		_ = h.uc.Logout(c.Request.Context(), token)
	}
	c.SetCookie(h.cfg.SessionCookieName, "", -1, "/", "",
		h.cfg.SessionCookieSecure, true)
	c.Status(http.StatusNoContent)
}

// Me godoc
// @Summary  Профиль текущей сессии.
// @Description  Возвращает user_id, login, email, role, lang и must_change_password.
// @Tags     auth
// @Produce  json
// @Success  200  {object}  map[string]any
// @Failure  401  {object}  map[string]string
// @Security CookieAuth
// @Security ApiTokenAuth
// @Router   /api/auth/me [get]
func (h *AuthHandler) Me(c *gin.Context) {
	s, ok := sessionFromCtx(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	user, err := h.uc.Me(c.Request.Context(), s.UserID)
	if err != nil {
		h.logger.ErrorWithOp("load user", err, "auth.me")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"user": meResponse{
			UserID:             user.ID,
			Login:              user.Login,
			Email:              user.Email,
			Role:               string(user.Role),
			Lang:               string(user.Lang),
			MustChangePassword: user.MustChangePassword,
			DefaultTeamID:      user.DefaultTeamID,
			CurrentTeamID:      s.CurrentTeamID,
		},
	})
}

// ChangeOwnPassword godoc
// @Summary  Сменить собственный пароль (self-service).
// @Description  Требует подтверждения текущего пароля; userID берётся из сессии (§26). Все сессии пользователя завершаются (forced re-login).
// @Tags     auth
// @Accept   json
// @Produce  json
// @Param    body  body  changeOwnPasswordRequest  true  "current + new password"
// @Success  204
// @Failure  400   {object}  map[string]string
// @Failure  401   {object}  map[string]string  "current password incorrect"
// @Security CookieAuth
// @Router   /api/me/password [post]
func (h *AuthHandler) ChangeOwnPassword(c *gin.Context) {
	s, ok := sessionFromCtx(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req changeOwnPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// userID — строго из проверенной сессии: чужой пароль так не сменить.
	err := h.uc.ChangeOwnPassword(c.Request.Context(), userActor(c), s.UserID, req.CurrentPassword, req.NewPassword)
	if err != nil {
		if errors.Is(err, domain.ErrUnauthorized) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": i18n.Translate(i18n.FromGin(c), "auth.invalid_credentials")})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}

// MyTeams godoc
// @Summary  Список команд, в которых состоит текущий пользователь.
// @Description  Multi-tenancy v2 (§16 ТЗ). Используется UI team-switcher'ом.
// @Tags     auth
// @Produce  json
// @Success  200  {object}  map[string]any
// @Failure  401  {object}  map[string]string
// @Security CookieAuth
// @Router   /api/me/teams [get]
func (h *AuthHandler) MyTeams(c *gin.Context) {
	s, ok := sessionFromCtx(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	memberships, err := h.uc.MyTeams(c.Request.Context(), s.UserID)
	if err != nil {
		h.logger.ErrorWithOp("list user teams", err, "auth.my_teams")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	items := make([]teamMembershipResponse, 0, len(memberships))
	for _, m := range memberships {
		items = append(items, teamMembershipResponse{
			ID: m.Team.ID, Slug: m.Team.Slug, Name: m.Team.Name,
			CHDatabase: m.Team.CHDatabase, Role: string(m.Role),
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "current_team_id": s.CurrentTeamID})
}

// SwitchTeam godoc
// @Summary  Сменить текущую команду в сессии.
// @Description  Multi-tenancy v2. team_id должен быть из списка GET /api/me/teams.
// @Tags     auth
// @Accept   json
// @Produce  json
// @Param    body  body  switchTeamRequest  true  "team_id"
// @Success  200   {object}  map[string]any
// @Failure  400   {object}  map[string]string
// @Failure  401   {object}  map[string]string
// @Failure  403   {object}  map[string]string  "user is not a member of this team"
// @Security CookieAuth
// @Router   /api/me/switch-team [post]
func (h *AuthHandler) SwitchTeam(c *gin.Context) {
	s, ok := sessionFromCtx(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	if s.Token == "" {
		// Псевдо-сессия API-токена — у него team фиксирована, переключать нельзя.
		c.JSON(http.StatusForbidden, gin.H{"error": "switch-team not allowed for API tokens"})
		return
	}
	var req switchTeamRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	updated, err := h.uc.SwitchTeam(c.Request.Context(), userActor(c), s.Token, req.TeamID)
	if err != nil {
		if errors.Is(err, domain.ErrPermissionDenied) {
			c.JSON(http.StatusForbidden, gin.H{"error": "user is not a member of this team"})
			return
		}
		if errors.Is(err, domain.ErrSessionNotFound) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "session expired"})
			return
		}
		h.logger.ErrorWithOp("switch team", err, "auth.switch_team")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	c.Set(ctxSessionKey, updated)
	c.JSON(http.StatusOK, gin.H{"current_team_id": updated.CurrentTeamID})
}
