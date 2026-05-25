package http

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"bus/internal/domain"
	"bus/internal/platform/logging"
	"bus/internal/web/usecase"
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
	Name      string     `json:"name" binding:"required,min=1,max=255"`
	Scopes    []string   `json:"scopes" binding:"required,min=1,dive,oneof=logs:read nodes:read metrics:read audit:read"`
	ExpiresAt *time.Time `json:"expires_at"`
}

type tokenResponse struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

func toTokenResp(t *domain.APIToken) tokenResponse {
	return tokenResponse{
		ID: t.ID, Name: t.Name, Prefix: t.Prefix, Scopes: t.Scopes,
		CreatedAt: t.CreatedAt, LastUsedAt: t.LastUsedAt,
		ExpiresAt: t.ExpiresAt, RevokedAt: t.RevokedAt,
	}
}

// List godoc
// @Summary  Список API-токенов текущего пользователя (§7.14).
// @Description  Каждый видит только свои токены. Возвращает префикс, scopes, last_used_at — без полной строки токена.
// @Tags     tokens
// @Produce  json
// @Success  200  {object}  map[string]any
// @Security CookieAuth
// @Router   /api/tokens [get]
func (h *APITokenHandler) List(c *gin.Context) {
	s, _ := sessionFromCtx(c)
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
// @Param    body  body  createTokenRequest  true  "name + scopes + expires_at"
// @Success  201   {object}  map[string]any  "token (plain) + info"
// @Failure  400   {object}  map[string]string
// @Security CookieAuth
// @Router   /api/tokens [post]
func (h *APITokenHandler) Create(c *gin.Context) {
	var req createTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	s, _ := sessionFromCtx(c)
	created, err := h.uc.Create(c.Request.Context(), userActor(c),
		s.UserID, req.Name, req.Scopes, req.ExpiresAt)
	if err != nil {
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
// @Failure  404  {object}  map[string]string
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
// @Failure  404  {object}  map[string]string
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
