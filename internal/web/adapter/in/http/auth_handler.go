package http

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"bus/internal/domain"
	"bus/internal/platform/config"
	"bus/internal/platform/i18n"
	"bus/internal/platform/logging"
	"bus/internal/web/usecase"
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
	Role               string `json:"role"`
	Lang               string `json:"lang"`
	MustChangePassword bool   `json:"must_change_password"`
}

// Login godoc
// @Summary  Логин по логину/паролю.
// @Description  При успехе ставит HttpOnly cookie databus_session (§7.1 ТЗ).
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
		},
	})
}

func (h *AuthHandler) Logout(c *gin.Context) {
	token, err := c.Cookie(h.cfg.SessionCookieName)
	if err == nil && token != "" {
		_ = h.uc.Logout(c.Request.Context(), token)
	}
	c.SetCookie(h.cfg.SessionCookieName, "", -1, "/", "",
		h.cfg.SessionCookieSecure, true)
	c.Status(http.StatusNoContent)
}

func (h *AuthHandler) Me(c *gin.Context) {
	s, ok := sessionFromCtx(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"user_id": s.UserID,
		"role":    string(s.Role),
		"lang":    string(s.Lang),
	})
}
