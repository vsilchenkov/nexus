package http

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"nexus/internal/domain"
	"nexus/internal/platform/clientip"
	"nexus/internal/platform/config"
	"nexus/internal/platform/i18n"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
)

// AuthHandler — /api/auth/* (login/logout/me).
type AuthHandler struct {
	uc  *usecase.AuthUsecase
	cfg *config.WebSection
	// ttl — провайдер длительности сессии (§34.2): cookie MaxAge берёт live-TTL,
	// настраиваемый без рестарта.
	ttl    func() time.Duration
	logger logging.Logger
}

func NewAuthHandler(
	uc *usecase.AuthUsecase,
	cfg *config.WebSection,
	ttl func() time.Duration,
	logger logging.Logger,
) *AuthHandler {
	return &AuthHandler{uc: uc, cfg: cfg, ttl: ttl, logger: logger}
}

type loginRequest struct {
	Login    string `json:"login" binding:"required,min=1,max=255"`
	Password string `json:"password" binding:"required,min=1,max=128"`
}

type meResponse struct {
	UserID string `json:"user_id"`
	Login  string `json:"login"`
	// Name — отображаемое имя (§66): UI показывает его вместо логина.
	Name               string `json:"name"`
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
	// ExternalURL — §89.4: внешняя ссылка команды. Нужна форме СОЗДАНИЯ узла,
	// где команда выбирается явно (§86.6) и её ещё нет ни в одном резолвере.
	ExternalURL string `json:"external_url"`
	Role        string `json:"role"`
}

type switchTeamRequest struct {
	TeamID string `json:"team_id" binding:"required,uuid"`
}

// setFavoriteTeamsRequest — PUT /api/me/favorite-teams (§49). Без
// binding:"required" на слайсе: пустой массив легален (очистить избранное),
// а required у gin режет и его.
type setFavoriteTeamsRequest struct {
	TeamIDs []string `json:"team_ids" binding:"omitempty,dive,uuid"`
}

// recordSearchRequest — POST /api/me/search-history (§62). Нормализацию (trim,
// границы длины) и отсев мусора делает usecase; здесь только формальный лимит,
// чтобы не принимать гигантские тела.
type recordSearchRequest struct {
	Q string `json:"q" binding:"required,max=1000"`
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
// @Success  200   {object}  UserEnvelope
// @Failure  400   {object}  ErrorResponse
// @Failure  401   {object}  ErrorResponse  "invalid credentials"
// @Failure  403   {object}  ErrorResponse  "user inactive"
// @Failure  429   {object}  ErrorResponse  "too many login attempts"
// @Router   /api/auth/login [post]
func (h *AuthHandler) Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	token, user, err := h.uc.Login(c.Request.Context(), req.Login, req.Password, clientip.NormalizeIPv4(c.ClientIP()))
	if err != nil {
		lang := i18n.FromGin(c)
		switch {
		case errors.Is(err, usecase.ErrLoginRateLimited):
			c.JSON(http.StatusTooManyRequests, gin.H{"error": i18n.Translate(lang, "auth.rate_limited")})
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

	h.setSessionCookie(c, token, int(h.ttl().Seconds()))

	c.JSON(http.StatusOK, gin.H{
		"user": meResponse{
			UserID: user.ID, Login: user.Login, Name: user.Name, Role: string(user.Role),
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
		// §91.2: actor нужен для записи user.logout в журнал; сессия здесь уже
		// проверена middleware'ом.
		_ = h.uc.Logout(c.Request.Context(), userActor(c), token)
	}
	// maxAge<0 — команда браузеру удалить куку. Атрибуты (SameSite, Secure)
	// считаются те же, что при установке: браузер сопоставляет куку по
	// name/domain/path, но несимметричные атрибуты — источник трудноуловимых
	// расхождений между схемами.
	h.setSessionCookie(c, "", -1)
	c.Status(http.StatusNoContent)
}

// setSessionCookie ставит (или удаляет при maxAge<0) session-cookie с
// атрибутами SameSite и Secure, согласованными между Login и Logout.
func (h *AuthHandler) setSessionCookie(c *gin.Context, token string, maxAge int) {
	sameSite := http.SameSiteStrictMode
	switch h.cfg.SessionCookieSamesite {
	case "lax":
		sameSite = http.SameSiteLaxMode
	case "none":
		sameSite = http.SameSiteNoneMode
	}
	c.SetSameSite(sameSite)
	c.SetCookie(h.cfg.SessionCookieName, token, maxAge, "/", "",
		requestIsHTTPS(c) && h.cfg.SessionCookieSecure, true)
}

// requestIsHTTPS — пришёл ли запрос по защищённому каналу (§90.2).
//
// Web всегда слушает plain HTTP: TLS терминирует внешний nginx, поэтому
// c.Request.TLS за прокси всегда nil и единственный признак схемы —
// X-Forwarded-Proto. Заголовку можно верить: атрибут Secure только сужает
// круг соединений, по которым браузер отправит куку, так что подделавший
// заголовок клиент навредит лишь себе.
//
// Зачем: cookie с Secure, отданная по http://, молча отбрасывается браузером
// (RFC 6265bis §5.5) — логин отвечал 200, а следующий /api/auth/me получал
// 401 и SPA возвращала на форму входа. Статический флаг из конфига заставлял
// выбирать между входом по DNS (HTTPS) и по IP (HTTP); теперь Secure ставится
// ровно тогда, когда канал это позволяет.
func requestIsHTTPS(c *gin.Context) bool {
	if c.Request.TLS != nil {
		return true
	}
	// Прокси может прислать список ("https, http") — значима первая запись.
	proto, _, _ := strings.Cut(c.GetHeader("X-Forwarded-Proto"), ",")
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}

// Me godoc
// @Summary  Профиль текущей сессии.
// @Description  Возвращает user_id, login, email, role, lang и must_change_password.
// @Tags     auth
// @Produce  json
// @Success  200  {object}  UserEnvelope
// @Failure  401  {object}  ErrorResponse
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
			Name:               user.Name,
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
// @Failure  400   {object}  ErrorResponse
// @Failure  401   {object}  ErrorResponse  "current password incorrect"
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
// @Success  200  {object}  MyTeamsResponse
// @Failure  401  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/me/teams [get]
func (h *AuthHandler) MyTeams(c *gin.Context) {
	s, ok := sessionFromCtx(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	// §44.H: вместе со списком — самолечение current_team в сессии, если он
	// больше не входит в членства (иначе пользователь застревал на чужой команде).
	memberships, current, healed, err := h.uc.MyTeamsAndCurrent(c.Request.Context(), s)
	if err != nil {
		h.logger.ErrorWithOp("list user teams", err, "auth.my_teams")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	items := make([]teamMembershipResponse, 0, len(memberships))
	for _, m := range memberships {
		items = append(items, teamMembershipResponse{
			ID: m.Team.ID, Slug: m.Team.Slug, Name: m.Team.Name,
			CHDatabase: m.Team.CHDatabase, ExternalURL: m.Team.ExternalURL,
			Role: string(m.Role),
		})
	}
	// §49: избранное едет вместе с членствами — один источник истины для UI
	// (свитчер + сайдбар), без второго запроса. При сбое — пустой список.
	favorites := h.uc.FavoriteTeamIDs(c.Request.Context(), s.UserID)
	c.JSON(http.StatusOK, gin.H{
		"items": items, "current_team_id": current, "healed": healed,
		"favorites": favorites,
	})
}

// SetFavoriteTeams godoc
// @Summary  Заменить список избранных команд текущего пользователя.
// @Description  §49: полная замена упорядоченного списка (позиция = индекс в team_ids). Пустой массив очищает избранное. Каждый team_id должен входить в членства пользователя (GET /api/me/teams).
// @Tags     auth
// @Accept   json
// @Produce  json
// @Param    body  body  setFavoriteTeamsRequest  true  "ordered team_ids"
// @Success  200   {object}  FavoriteTeamsResponse
// @Failure  400   {object}  ErrorResponse  "duplicates / too many / not a member"
// @Failure  401   {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/me/favorite-teams [put]
func (h *AuthHandler) SetFavoriteTeams(c *gin.Context) {
	s, ok := sessionFromCtx(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req setFavoriteTeamsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.TeamIDs == nil {
		req.TeamIDs = []string{}
	}
	err := h.uc.SetFavoriteTeams(c.Request.Context(), userActor(c), s.UserID, req.TeamIDs)
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrUserNotTeamMember):
			c.JSON(http.StatusBadRequest, gin.H{"error": "team is not among user memberships"})
		case errors.Is(err, domain.ErrFavoriteTeamsInvalid):
			c.JSON(http.StatusBadRequest, gin.H{"error": "favorite teams list invalid (duplicates or too many)"})
		default:
			h.logger.ErrorWithOp("set favorite teams", err, "auth.set_favorite_teams")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"team_ids": req.TeamIDs})
}

// SwitchTeam godoc
// @Summary  Сменить текущую команду в сессии.
// @Description  Multi-tenancy v2. team_id должен быть из списка GET /api/me/teams.
// @Tags     auth
// @Accept   json
// @Produce  json
// @Param    body  body  switchTeamRequest  true  "team_id"
// @Success  200   {object}  SwitchTeamResponse
// @Failure  400   {object}  ErrorResponse
// @Failure  401   {object}  ErrorResponse
// @Failure  403   {object}  ErrorResponse  "user is not a member of this team"
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

// SearchHistory godoc
// @Summary  История поиска узлов текущего пользователя.
// @Description  §62: последние сохранённые строки поиска (не более 10), от свежих к старым. Общая для глобального поиска в шапке и поля «Поиск» на странице узлов. Строго per-user.
// @Tags     auth
// @Produce  json
// @Success  200  {object}  SearchHistoryResponse
// @Failure  401  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/me/search-history [get]
func (h *AuthHandler) SearchHistory(c *gin.Context) {
	s, ok := sessionFromCtx(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	// Никогда не ошибка (см. usecase): при сбое — пустой список.
	items := h.uc.SearchHistory(c.Request.Context(), s.UserID)
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// RecordSearch godoc
// @Summary  Сохранить строку поиска в историю текущего пользователя.
// @Description  §62: upsert строки в персональную историю (повтор всплывает наверх), обрезка до 10 свежих. Мусор (пустая/короткая/слишком длинная строка) молча игнорируется — тоже 204. Строго per-user.
// @Tags     auth
// @Accept   json
// @Param    body  body  recordSearchRequest  true  "поисковая строка"
// @Success  204   "записано (или проигнорировано как мусор)"
// @Failure  400   {object}  ErrorResponse
// @Failure  401   {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/me/search-history [post]
func (h *AuthHandler) RecordSearch(c *gin.Context) {
	s, ok := sessionFromCtx(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req recordSearchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.uc.RecordSearch(c.Request.Context(), s.UserID, req.Q); err != nil {
		h.logger.ErrorWithOp("record search", err, "auth.record_search")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	c.Status(http.StatusNoContent)
}

// ClearSearchHistory godoc
// @Summary  Очистить историю поиска текущего пользователя.
// @Description  §62: удалить все сохранённые строки поиска. Строго per-user.
// @Tags     auth
// @Success  204  "очищено"
// @Failure  401  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/me/search-history [delete]
func (h *AuthHandler) ClearSearchHistory(c *gin.Context) {
	s, ok := sessionFromCtx(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	if err := h.uc.ClearSearchHistory(c.Request.Context(), s.UserID); err != nil {
		h.logger.ErrorWithOp("clear search history", err, "auth.clear_search_history")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	c.Status(http.StatusNoContent)
}
