package http

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"bus/internal/domain"
	"bus/internal/platform/logging"
	"bus/internal/web/usecase"
)

// AppSettingsHandler — GET/PUT /api/settings/app + POST /api/settings/{sentry,clickhouse}/test (§14.5 ТЗ).
//
// GET/PUT доступны admin'у (mw RequireAdmin в RegisterAPI). PUT логирует
// audit-запись с пометкой изменённых секций. Test-эндпоинты (Phase 6.3.2.6)
// проверяют доступность с заданным patch'ом без сохранения.
type AppSettingsHandler struct {
	uc     *usecase.AppSettingsUsecase
	tester *usecase.SettingsTester
	logger logging.Logger
}

// NewAppSettingsHandler — tester опционален: nil отключает test-эндпоинты
// (например в тестах, где не нужны live-коннекты к CH/Sentry).
func NewAppSettingsHandler(uc *usecase.AppSettingsUsecase, tester *usecase.SettingsTester, logger logging.Logger) *AppSettingsHandler {
	return &AppSettingsHandler{uc: uc, tester: tester, logger: logger}
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

// TestClickHouse godoc
// @Summary  Проверить соединение с ClickHouse по patch'у настроек (§7.10).
// @Description  Открывает временный conn с merge'нутыми (current + body) настройками, делает Ping, возвращает {ok,latency_ms} или {ok:false,error}. Не сохраняет.
// @Tags     settings
// @Accept   json
// @Produce  json
// @Param    body  body  domain.ClickHouseSettings  true  "patch"
// @Success  200  {object}  usecase.TestResult
// @Failure  400  {object}  map[string]string
// @Failure  500  {object}  map[string]string
// @Security CookieAuth
// @Router   /api/settings/clickhouse/test [post]
func (h *AppSettingsHandler) TestClickHouse(c *gin.Context) {
	if h.tester == nil {
		localizedError(c, http.StatusServiceUnavailable, "error.internal")
		return
	}
	var patch domain.ClickHouseSettings
	if err := c.ShouldBindJSON(&patch); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	res, err := h.tester.TestClickHouse(c.Request.Context(), &patch)
	if err != nil {
		h.logger.ErrorWithOp("clickhouse test failed", err, "settings.test_clickhouse")
		localizedError(c, http.StatusInternalServerError, "error.internal")
		return
	}
	c.JSON(http.StatusOK, res)
}

// TestSentry godoc
// @Summary  Отправить тестовое событие в Sentry с patch'ем настроек (§7.10).
// @Description  Создаёт изолированный sentry.Client, отправляет CaptureMessage, ждёт Flush. Глобальный hub не затрагивается. Возвращает {ok,latency_ms} или {ok:false,error}.
// @Tags     settings
// @Accept   json
// @Produce  json
// @Param    body  body  domain.SentrySettings  true  "patch"
// @Success  200  {object}  usecase.TestResult
// @Failure  400  {object}  map[string]string
// @Failure  500  {object}  map[string]string
// @Security CookieAuth
// @Router   /api/settings/sentry/test [post]
func (h *AppSettingsHandler) TestSentry(c *gin.Context) {
	if h.tester == nil {
		localizedError(c, http.StatusServiceUnavailable, "error.internal")
		return
	}
	var patch domain.SentrySettings
	if err := c.ShouldBindJSON(&patch); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	res, err := h.tester.TestSentry(c.Request.Context(), &patch)
	if err != nil {
		h.logger.ErrorWithOp("sentry test failed", err, "settings.test_sentry")
		localizedError(c, http.StatusInternalServerError, "error.internal")
		return
	}
	c.JSON(http.StatusOK, res)
}
