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
