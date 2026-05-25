package http

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"bus/internal/domain"
	"bus/internal/platform/logging"
	"bus/internal/web/usecase"
)

// AppSettingsHandler — GET/PUT /api/settings/app (§14.5 ТЗ).
//
// GET доступен любому аутентифицированному пользователю с правом admin
// (mw RequireAdmin в RegisterAPI). PUT логирует audit-запись с пометкой
// изменённых секций.
type AppSettingsHandler struct {
	uc     *usecase.AppSettingsUsecase
	logger logging.Logger
}

func NewAppSettingsHandler(uc *usecase.AppSettingsUsecase, logger logging.Logger) *AppSettingsHandler {
	return &AppSettingsHandler{uc: uc, logger: logger}
}

// Get godoc
// @Summary  Текущие dynamic-настройки Sentry/ClickHouse (§14.5).
// @Description  Возвращает merge'нутые значения из БД. Секреты (DSN, password) маскируются.
// @Tags     settings
// @Produce  json
// @Success  200  {object}  domain.AppSettings
// @Failure  500  {object}  map[string]string
// @Security CookieAuth
// @Router   /api/settings/app [get]
func (h *AppSettingsHandler) Get(c *gin.Context) {
	s, err := h.uc.Get(c.Request.Context())
	if err != nil {
		h.logger.ErrorWithOp("app_settings get failed", err, "settings.get")
		localizedError(c, http.StatusInternalServerError, "error.internal")
		return
	}
	c.JSON(http.StatusOK, s)
}

// Update godoc
// @Summary  Обновить dynamic-настройки Sentry/ClickHouse (§14.5).
// @Description  Patch-семантика: nil-поля сохраняются как есть, "***" в секретах не перезаписывает текущее значение.
// @Tags     settings
// @Accept   json
// @Produce  json
// @Param    body  body  domain.AppSettings  true  "patch"
// @Success  204
// @Failure  400  {object}  map[string]string
// @Failure  500  {object}  map[string]string
// @Security CookieAuth
// @Router   /api/settings/app [put]
func (h *AppSettingsHandler) Update(c *gin.Context) {
	var patch domain.AppSettings
	if err := c.ShouldBindJSON(&patch); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.uc.Update(c.Request.Context(), actorFromCtx(c), &patch); err != nil {
		h.logger.ErrorWithOp("app_settings update failed", err, "settings.update")
		localizedError(c, http.StatusInternalServerError, "error.internal")
		return
	}
	c.Status(http.StatusNoContent)
}
