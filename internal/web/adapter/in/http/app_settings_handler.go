package http

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
)

// AppSettingsHandler — GET/PUT /api/settings/app + POST /api/settings/{sentry,clickhouse}/test (§14.5 ТЗ).
//
// GET/PUT доступны admin'у (mw RequireAdmin в RegisterAPI). PUT логирует
// audit-запись с пометкой изменённых секций. Test-эндпоинты (Phase 6.3.2.6)
// проверяют доступность с заданным patch'ом без сохранения.
type AppSettingsHandler struct {
	uc     *usecase.AppSettingsUsecase
	tester *usecase.SettingsTester
	// nodeDefaultMaxBodySize — §64: значение из конфига (web.node_default_max_body_size),
	// которое форма создания узла подставляет в «Макс. размер тела».
	nodeDefaultMaxBodySize int
	// chDatabasePrefix — §70.8: "nexus_" либо "nexus_<instance>_". Диалог
	// создания команды показывает по нему предпросмотр имени БД.
	chDatabasePrefix string
	logger           logging.Logger
}

// NewAppSettingsHandler — tester опционален: nil отключает test-эндпоинты
// (например в тестах, где не нужны live-коннекты к CH/Sentry).
func NewAppSettingsHandler(uc *usecase.AppSettingsUsecase, tester *usecase.SettingsTester, nodeDefaultMaxBodySize int, chDatabasePrefix string, logger logging.Logger) *AppSettingsHandler {
	return &AppSettingsHandler{
		uc:                     uc,
		tester:                 tester,
		nodeDefaultMaxBodySize: nodeDefaultMaxBodySize,
		chDatabasePrefix:       chDatabasePrefix,
		logger:                 logger,
	}
}

// Get godoc
// @Summary  Текущие dynamic-настройки Sentry/ClickHouse (§14.5).
// @Description  Возвращает merge'нутые значения из БД. Секреты (DSN, password) маскируются.
// @Tags     settings
// @Produce  json
// @Success  200  {object}  domain.AppSettings
// @Failure  500  {object}  ErrorResponse
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

// GetPublic godoc
// @Summary  Публичный адрес приложения (§28, Пункт 1).
// @Description  Лёгкий read-only эндпоинт для любого авторизованного пользователя: возвращает {public_base_url} для сборки полного адреса узла в UI. Не требует прав admin (в отличие от /api/settings/app).
// @Tags     settings
// @Produce  json
// @Success  200  {object}  PublicSettingsResponse
// @Failure  500  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/settings/public [get]
func (h *AppSettingsHandler) GetPublic(c *gin.Context) {
	s, err := h.uc.Get(c.Request.Context())
	if err != nil {
		h.logger.ErrorWithOp("app_settings public get failed", err, "settings.get_public")
		localizedError(c, http.StatusInternalServerError, "error.internal")
		return
	}
	var url string
	if s.General.PublicBaseURL != nil {
		url = *s.General.PublicBaseURL
	}
	// §44.C: интервал автообновления метрик (резолв nil → дефолт) — нужен
	// дашборду/страницам узлов всем авторизованным, не только admin.
	refetch := domain.MetricsRefetchDefaultMs
	if s.General.MetricsRefetchMs != nil {
		refetch = *s.General.MetricsRefetchMs
	}
	c.JSON(http.StatusOK, gin.H{
		"public_base_url":    url,
		"metrics_refetch_ms": refetch,
		// §64: дефолт формы создания узла. Из конфига, а не из app_settings —
		// это параметр развёртывания, а не переключатель в UI.
		"node_default_max_body_size": h.nodeDefaultMaxBodySize,
		// §70.8: префикс имён БД ClickHouse этой ноды — по нему диалог создания
		// команды строит предпросмотр вместо литерала "nexus_" на клиенте.
		"ch_database_prefix": h.chDatabasePrefix,
	})
}

// Update godoc
// @Summary  Обновить dynamic-настройки Sentry/ClickHouse (§14.5).
// @Description  Patch-семантика: nil-поля сохраняются как есть, "***" в секретах не перезаписывает текущее значение.
// @Tags     settings
// @Accept   json
// @Produce  json
// @Param    body  body  domain.AppSettings  true  "patch"
// @Success  204
// @Failure  400  {object}  ErrorResponse
// @Failure  500  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/settings/app [put]
func (h *AppSettingsHandler) Update(c *gin.Context) {
	var patch domain.AppSettings
	if err := c.ShouldBindJSON(&patch); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.uc.Update(c.Request.Context(), actorFromCtx(c), &patch); err != nil {
		if errors.Is(err, domain.ErrPublicBaseURLInvalid) || errors.Is(err, domain.ErrTelegramCronInvalid) ||
			errors.Is(err, domain.ErrSessionTTLInvalid) || errors.Is(err, domain.ErrMetricsRefetchInvalid) ||
			errors.Is(err, domain.ErrLogLevelInvalid) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		// §34.3: попытка задать override версии в проде — 403.
		if errors.Is(err, domain.ErrVersionOverrideForbidden) {
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
			return
		}
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
// @Failure  400  {object}  ErrorResponse
// @Failure  500  {object}  ErrorResponse
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
// @Failure  400  {object}  ErrorResponse
// @Failure  500  {object}  ErrorResponse
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

// TestTelegram godoc
// @Summary  Отправить тестовое уведомление в Telegram с patch'ем настроек (§20.7).
// @Description  Шлёт тестовое сообщение в чат с merge'нутыми (current + body) настройками. Маскированный bot_token не используется. Возвращает {ok,latency_ms} или {ok:false,error}. Не сохраняет.
// @Tags     settings
// @Accept   json
// @Produce  json
// @Param    body  body  domain.TelegramSettings  true  "patch"
// @Success  200  {object}  usecase.TestResult
// @Failure  400  {object}  ErrorResponse
// @Failure  500  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/settings/notifications/test [post]
func (h *AppSettingsHandler) TestTelegram(c *gin.Context) {
	if h.tester == nil {
		localizedError(c, http.StatusServiceUnavailable, "error.internal")
		return
	}
	var patch domain.TelegramSettings
	if err := c.ShouldBindJSON(&patch); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	res, err := h.tester.TestTelegram(c.Request.Context(), &patch)
	if err != nil {
		h.logger.ErrorWithOp("telegram test failed", err, "settings.test_telegram")
		localizedError(c, http.StatusInternalServerError, "error.internal")
		return
	}
	c.JSON(http.StatusOK, res)
}

// mailTestRequest — тело проверки почтовых настроек (§88.8.4): секция целиком
// плюс получатель. Получатель отдельным полем, а не внутри настроек: у почты,
// в отличие от Telegram, адрес назначения частью конфигурации не является.
type mailTestRequest struct {
	domain.MailSettings
	To string `json:"to" binding:"required,email,max=320"`
}

// TestMail godoc
// @Summary  Отправить тестовое письмо с patch'ем настроек (§88.8.4).
// @Description  Шлёт письмо на указанный адрес с merge'нутыми (current + body) настройками. Маскированный пароль не используется — merge оставит сохранённый. Возвращает {ok,latency_ms} или {ok:false,error}. Не сохраняет настройки.
// @Tags     settings
// @Accept   json
// @Produce  json
// @Param    body  body  mailTestRequest  true  "patch + получатель"
// @Success  200  {object}  usecase.TestResult
// @Failure  400  {object}  ErrorResponse
// @Failure  500  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/settings/mail/test [post]
func (h *AppSettingsHandler) TestMail(c *gin.Context) {
	if h.tester == nil {
		localizedError(c, http.StatusServiceUnavailable, "error.internal")
		return
	}
	var req mailTestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// Негодные значения самой секции отсекаем до похода в сеть — иначе
	// оператор увидит невнятную ошибку резолвера вместо «порт вне диапазона».
	if err := domain.ValidateMailSettings(req.MailSettings); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	res, err := h.tester.TestMail(c.Request.Context(), &req.MailSettings, req.To)
	if err != nil {
		h.logger.ErrorWithOp("mail test failed", err, "settings.test_mail")
		localizedError(c, http.StatusInternalServerError, "error.internal")
		return
	}
	c.JSON(http.StatusOK, res)
}
