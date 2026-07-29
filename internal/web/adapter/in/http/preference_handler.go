package http

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
)

// PreferenceHandler — персональные предпочтения пользователя (§71).
// Отдельный handler, а не методы на AuthHandler — симметрично отдельному
// PreferenceUsecase.
type PreferenceHandler struct {
	uc     *usecase.PreferenceUsecase
	logger logging.Logger
}

func NewPreferenceHandler(uc *usecase.PreferenceUsecase, logger logging.Logger) *PreferenceHandler {
	return &PreferenceHandler{uc: uc, logger: logger}
}

// setPreferenceRequest — PUT /api/me/prefs (§71). team_id опционален (пусто =
// глобальный преф). value — произвольный JSON, сервер его не интерпретирует.
//
// max=64 на ключе дублирует доменную проверку намеренно: без него ключ
// произвольной длины доезжал бы до usecase и попадал в Debug-лог отказа целиком
// (раздувание журнала на мусорном запросе). Граница совпадает с VARCHAR(64) и
// domain.maxPreferenceKeyLen.
type setPreferenceRequest struct {
	TeamID string          `json:"team_id"`
	Key    string          `json:"key" binding:"required,max=64"`
	Value  json.RawMessage `json:"value" binding:"required" swaggertype:"object"`
}

// Prefs godoc
// @Summary  Персональные предпочтения текущего пользователя.
// @Description  §71: все префы пользователя одним ответом — и глобальные (team_id пустой), и привязанные к командам. Строго per-user. При сбое хранилища деградирует в пустой список (200, не 500): преф — настройка UI, и её недоступность не должна валить страницу, которая её читает.
// @Tags     auth
// @Produce  json
// @Success  200  {object}  UserPrefsResponse
// @Failure  401  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/me/prefs [get]
func (h *PreferenceHandler) Prefs(c *gin.Context) {
	s, ok := sessionFromCtx(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	// Никогда не ошибка (см. usecase): при сбое — пустой список.
	prefs := h.uc.Preferences(c.Request.Context(), s.UserID)
	items := make([]userPrefDTO, 0, len(prefs))
	for _, p := range prefs {
		items = append(items, userPrefDTO{
			TeamID:    p.TeamID,
			Key:       p.Key,
			Value:     p.Value,
			UpdatedAt: p.UpdatedAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// SetPref godoc
// @Summary  Сохранить (upsert) один преф текущего пользователя.
// @Description  §71: upsert по (user_id, team_id, key). team_id пустой — глобальный преф; непустой обязан входить в членства пользователя (иначе 400, одинаково для чужой и несуществующей команды). value — произвольный валидный JSON (не null) до 4096 байт, сервер его не интерпретирует. Ключ — ^[a-z][a-z0-9_]*(\.[a-z0-9_]+)*$, до 64 символов.
// @Tags     auth
// @Accept   json
// @Produce  json
// @Param    body  body  setPreferenceRequest  true  "преф"
// @Success  204   "сохранено"
// @Failure  400   {object}  ErrorResponse  "invalid key/value, not a member, limit exceeded"
// @Failure  401   {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/me/prefs [put]
func (h *PreferenceHandler) SetPref(c *gin.Context) {
	s, ok := sessionFromCtx(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req setPreferenceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	err := h.uc.SetPreference(c.Request.Context(), s.UserID, req.TeamID, req.Key, req.Value)
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrPreferenceKeyInvalid):
			c.JSON(http.StatusBadRequest, gin.H{"error": "preference key invalid"})
		case errors.Is(err, domain.ErrPreferenceValueInvalid):
			c.JSON(http.StatusBadRequest, gin.H{"error": "preference value invalid"})
		case errors.Is(err, domain.ErrPreferencesLimit):
			c.JSON(http.StatusBadRequest, gin.H{"error": "preferences limit exceeded"})
		case errors.Is(err, domain.ErrUserNotTeamMember):
			c.JSON(http.StatusBadRequest, gin.H{"error": "team is not among user memberships"})
		default:
			h.logger.ErrorWithOp("set preference", err, "prefs.set")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		}
		return
	}
	c.Status(http.StatusNoContent)
}
