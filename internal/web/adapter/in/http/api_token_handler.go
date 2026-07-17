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

// APITokenHandler — /api/tokens/* (доступно всем ролям, каждый видит
// только свои; §7.14).
type APITokenHandler struct {
	uc     *usecase.APITokenUsecase
	logger logging.Logger
}

func NewAPITokenHandler(uc *usecase.APITokenUsecase, logger logging.Logger) *APITokenHandler {
	return &APITokenHandler{uc: uc, logger: logger}
}

type createTokenRequest struct {
	Name   string   `json:"name" binding:"required,min=1,max=255"`
	Scopes []string `json:"scopes" binding:"required,min=1,dive,oneof=logs:read nodes:read metrics:read audit:read"`
	// TeamID — команда, в которой будет действовать токен (§18.3: токен
	// ограничен одной командой). Выбирается в форме; пусто — текущая команда
	// сессии (старый контракт). Членство проверяется в usecase.
	TeamID string `json:"team_id" binding:"omitempty,uuid"`
	// ExpiresInDays — срок жизни в днях; null/отсутствие = бессрочный. Дни, а
	// не дата: срок считает сервер по своим часам. Поле называлось expires_at
	// и не совпадало с тем, что шлёт UI (expires_in_days), поэтому срок молча
	// терялся и все токены выходили бессрочными.
	ExpiresInDays *int `json:"expires_in_days" binding:"omitempty,min=1,max=3650"`
}

type tokenResponse struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	TeamID     string     `json:"team_id"`
	Prefix     string     `json:"prefix"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

func toTokenResp(t *domain.APIToken) tokenResponse {
	return tokenResponse{
		ID: t.ID, Name: t.Name, TeamID: t.TeamID, Prefix: t.Prefix, Scopes: t.Scopes,
		CreatedAt: t.CreatedAt, LastUsedAt: t.LastUsedAt,
		ExpiresAt: t.ExpiresAt, RevokedAt: t.RevokedAt,
	}
}

// List godoc
// @Summary  Список API-токенов текущего пользователя (§7.14).
// @Description  Каждый видит только свои токены — по всем своим командам («Настройки» не скоупятся переключателем команд; команда токена приходит в team_id). Возвращает префикс, scopes, team_id, last_used_at — без полной строки токена.
// @Tags     tokens
// @Produce  json
// @Success  200  {object}  ListTokensResponse
// @Security CookieAuth
// @Router   /api/tokens [get]
func (h *APITokenHandler) List(c *gin.Context) {
	s, _ := sessionFromCtx(c)
	// Без team-скоупа: «Настройки» — личный раздел, переключатель команд в
	// шапке его не трогает. Команда каждого токена едет в team_id.
	tokens, err := h.uc.ListByUser(c.Request.Context(), s.UserID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	out := make([]tokenResponse, 0, len(tokens))
	for _, t := range tokens {
		out = append(out, toTokenResp(t))
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// Create godoc
// @Summary  Создать API-токен (§7.14).
// @Description  Plain-строка токена возвращается ровно один раз, потом доступен только префикс. Scopes — подмножество {logs:read, nodes:read, metrics:read, audit:read}.
// @Tags     tokens
// @Accept   json
// @Produce  json
// @Param    body  body  createTokenRequest  true  "name + scopes + team_id + expires_in_days"
// @Success  201   {object}  CreateTokenResponse  "token (plain) + info"
// @Failure  400   {object}  ErrorResponse
// @Failure  403   {object}  ErrorResponse  "команда не входит в членства пользователя"
// @Security CookieAuth
// @Router   /api/tokens [post]
func (h *APITokenHandler) Create(c *gin.Context) {
	var req createTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	s, _ := sessionFromCtx(c)
	// Команда выбирается в форме (§18.3: токен ограничен одной командой).
	// Пусто — текущая команда сессии (старый контракт). Членство проверяет
	// usecase: team_id пришёл от клиента.
	teamID := req.TeamID
	if teamID == "" {
		teamID = s.CurrentTeamID
	}
	created, err := h.uc.Create(c.Request.Context(), userActor(c),
		s.UserID, teamID, req.Name, req.Scopes, req.ExpiresInDays)
	if err != nil {
		if errors.Is(err, domain.ErrPermissionDenied) {
			c.JSON(http.StatusForbidden, gin.H{"error": "team membership required"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// §7.14: показываем plain ровно один раз.
	c.JSON(http.StatusCreated, gin.H{
		"token": created.Plain,
		"info":  toTokenResp(created.Token),
	})
}

// Revoke godoc
// @Summary  Отозвать API-токен.
// @Description  После revoke токен сразу теряет силу, но сама запись остаётся (для аудита).
// @Tags     tokens
// @Produce  json
// @Param    id   path  string  true  "token id"
// @Success  204
// @Failure  404  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/tokens/{id}/revoke [post]
func (h *APITokenHandler) Revoke(c *gin.Context) {
	s, _ := sessionFromCtx(c)
	if err := h.uc.Revoke(c.Request.Context(), userActor(c), c.Param("id"), s.UserID); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "token not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	c.Status(http.StatusNoContent)
}

// Delete godoc
// @Summary  Удалить API-токен.
// @Description  Удаление допустимо, только если токен уже revoked. Иначе — 404 «not deletable yet».
// @Tags     tokens
// @Produce  json
// @Param    id   path  string  true  "token id"
// @Success  204
// @Failure  404  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/tokens/{id} [delete]
func (h *APITokenHandler) Delete(c *gin.Context) {
	s, _ := sessionFromCtx(c)
	if err := h.uc.Delete(c.Request.Context(), userActor(c), c.Param("id"), s.UserID); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "token not found or not deletable yet"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	c.Status(http.StatusNoContent)
}
