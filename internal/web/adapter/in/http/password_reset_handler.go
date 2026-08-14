package http

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"nexus/internal/domain"
	"nexus/internal/platform/clientip"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
)

// PasswordResetHandler — публичные эндпоинты восстановления пароля (§88.4.2).
//
// Все три маршрута объявлены рядом со входом и вне групп, требующих
// аутентификации: пользователь, забывший пароль, войти не может по условию.
type PasswordResetHandler struct {
	uc          *usecase.PasswordResetUsecase
	limiter     rateLimiter
	limitPerMin int
	logger      logging.Logger
}

func NewPasswordResetHandler(
	uc *usecase.PasswordResetUsecase,
	limiter rateLimiter,
	limitPerMin int,
	logger logging.Logger,
) *PasswordResetHandler {
	return &PasswordResetHandler{uc: uc, limiter: limiter, limitPerMin: limitPerMin, logger: logger}
}

type passwordResetRequestBody struct {
	// Login — логин ИЛИ email. 320 — предел длины адреса по RFC.
	Login string `json:"login" binding:"required,min=1,max=320"`
}

type passwordResetConfirmBody struct {
	Token       string `json:"token" binding:"required,min=16,max=128"`
	NewPassword string `json:"new_password" binding:"required,min=8,max=128"`
}

type passwordResetRequestResponse struct {
	OK bool `json:"ok"`
	// TTLMinutes — срок жизни ссылки; интерфейс показывает его в тексте
	// «ссылка действует N минут». Значение одинаково при любом исходе и
	// ничего не раскрывает.
	TTLMinutes int `json:"ttl_minutes"`
}

type passwordResetValidateResponse struct {
	Valid bool `json:"valid"`
}

// allow — проверка лимита с общей семантикой fail-open (§9.4): при
// недоступном Redis запрос пропускается, а сам факт виден в метрике
// nexus_ratelimit_check_errors_total{scope="pwreset"}.
func (h *PasswordResetHandler) allow(c *gin.Context, key string) bool {
	if h.limiter == nil || h.limitPerMin <= 0 {
		return true
	}
	ok, err := h.limiter.Allow(c.Request.Context(), key, h.limitPerMin)
	if err != nil {
		h.logger.Warn("password reset rate limit check failed",
			h.logger.Str("key_scope", "pwreset"),
			h.logger.Err(err))
	}
	return ok
}

// Request godoc
// @Summary  Запросить ссылку для смены пароля (§88.4.2).
// @Description  Принимает логин ИЛИ email. Отвечает ОДИНАКОВО во всех содержательных исходах (письмо ушло, пользователя нет, у него нет email, он отключён, адрес неоднозначен, превышен предел активных ссылок, почта выключена) — публичная форма не должна быть справочником существующих учётных записей. Настоящая причина пишется в аудит. Письмо отправляется фоном.
// @Tags     auth
// @Accept   json
// @Produce  json
// @Param    body  body  passwordResetRequestBody  true  "логин или email"
// @Success  202  {object}  passwordResetRequestResponse
// @Failure  400  {object}  ErrorResponse
// @Failure  429  {object}  ErrorResponse  "rate limit exceeded"
// @Router   /api/auth/password-reset/request [post]
func (h *PasswordResetHandler) Request(c *gin.Context) {
	var req passwordResetRequestBody
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ip := clientip.NormalizeIPv4(c.ClientIP())
	// Два независимых ключа: по адресу — против массовой рассылки, по
	// ВВЕДЁННОЙ строке — против точечного заваливания чужого ящика. Второй
	// считается независимо от существования пользователя, иначе сам лимит
	// стал бы способом узнать ответ (§88.4.4).
	if !h.allow(c, "pwreset:ip:"+ip) || !h.allow(c, "pwreset:user:"+req.Login) {
		localizedError(c, http.StatusTooManyRequests, "auth.reset_rate_limited")
		return
	}

	ttl, err := h.uc.Request(c.Request.Context(), req.Login, ip)
	if err != nil {
		h.logger.ErrorWithOp("password reset request failed", err, "auth.password_reset_request")
		localizedError(c, http.StatusInternalServerError, "error.internal")
		return
	}
	// 202, а не 200: письмо уходит фоном, «принято к обработке» — честная
	// семантика.
	c.JSON(http.StatusAccepted, passwordResetRequestResponse{OK: true, TTLMinutes: ttl})
}

// Validate godoc
// @Summary  Проверить ссылку для смены пароля (§88.4.6).
// @Description  Проверка НЕ расходует ссылку: почтовые шлюзы с защитой от вредоносных ссылок сами открывают адреса из писем, и гашение на проверке убивало бы ссылку раньше адресата. Всегда 200; недействительная, истёкшая и уже использованная неразличимы.
// @Tags     auth
// @Produce  json
// @Param    token  query  string  true  "токен из письма"
// @Success  200  {object}  passwordResetValidateResponse
// @Failure  400  {object}  ErrorResponse
// @Router   /api/auth/password-reset/validate [get]
func (h *PasswordResetHandler) Validate(c *gin.Context) {
	token := c.Query("token")
	if token == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "token is required"})
		return
	}
	ok, err := h.uc.Validate(c.Request.Context(), token)
	if err != nil {
		h.logger.ErrorWithOp("password reset validate failed", err, "auth.password_reset_validate")
		localizedError(c, http.StatusInternalServerError, "error.internal")
		return
	}
	c.JSON(http.StatusOK, passwordResetValidateResponse{Valid: ok})
}

// Confirm godoc
// @Summary  Задать новый пароль по ссылке (§88.7).
// @Description  Гасит ссылку и меняет пароль. Все сессии пользователя завершаются, ранее выданные ссылки восстановления аннулируются. Недействительный, истёкший и уже использованный токен дают один ответ.
// @Tags     auth
// @Accept   json
// @Produce  json
// @Param    body  body  passwordResetConfirmBody  true  "токен и новый пароль"
// @Success  204  "пароль изменён"
// @Failure  400  {object}  ErrorResponse
// @Failure  429  {object}  ErrorResponse  "rate limit exceeded"
// @Router   /api/auth/password-reset/confirm [post]
func (h *PasswordResetHandler) Confirm(c *gin.Context) {
	var req passwordResetConfirmBody
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ip := clientip.NormalizeIPv4(c.ClientIP())
	// Перебор 256-битного токена нереален, но лимит стоит дёшево.
	if !h.allow(c, "pwreset:confirm:"+ip) {
		localizedError(c, http.StatusTooManyRequests, "auth.reset_rate_limited")
		return
	}

	switch err := h.uc.Confirm(c.Request.Context(), req.Token, req.NewPassword, ip); {
	case err == nil:
		c.Status(http.StatusNoContent)
	case errors.Is(err, domain.ErrOneTimeTokenInvalid):
		localizedError(c, http.StatusBadRequest, "auth.reset_token_invalid")
	case errors.Is(err, domain.ErrUserInactive):
		localizedError(c, http.StatusBadRequest, "auth.reset_not_allowed")
	case errors.Is(err, domain.ErrUserNotFound), errors.Is(err, domain.ErrNotFound):
		// Пользователя удалили между письмом и переходом.
		localizedError(c, http.StatusBadRequest, "auth.reset_token_invalid")
	case errors.Is(err, usecase.ErrPasswordPolicy):
		// Отказ политики пароля: текст про введённое значение, а не про
		// учётную запись, — показывать его безопасно и полезно.
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	default:
		// Всё остальное — внутренний сбой (БД, bcrypt). Наружу его текст
		// отдавать нельзя: эндпоинт публичный, а в ошибке хранилища бывает и
		// имя таблицы, и кусок запроса.
		h.logger.ErrorWithOp("password reset confirm failed", err, "auth.password_reset_confirm")
		localizedError(c, http.StatusInternalServerError, "error.internal")
	}
}
