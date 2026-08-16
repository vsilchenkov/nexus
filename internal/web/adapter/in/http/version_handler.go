package http

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
)

// VersionHandler — публичный эндпоинт версии приложения (§30, §34.3 ТЗ).
//
// Версия берётся из build-конфига (cfg.Build.Version): ldflags при сборке из git
// (make / docker compose build) или fallback versioninfo.json. Не требует авторизации —
// SPA показывает версию в футере (+ commit/build_date в tooltip) в т.ч. до входа.
//
// §34.3: в dev (overrideAllowed=true) отображаемую версию можно переопределить
// вручную через app_settings.general.version_override (override != "" имеет
// приоритет над git-версией). В проде overrideAllowed=false → всегда git-версия.
type VersionHandler struct {
	version         string
	commit          string
	buildDate       string
	overrideAllowed bool
	// override возвращает текущий ручной override версии ("" = не задан).
	// Читается из app_settings; вызывается только при overrideAllowed.
	override func(ctx context.Context) string
	// instance — §70.8: идентификатор ноды для бейджа в шапке. Пустой у ноды
	// без идентификатора: бейдж тогда не рендерится вовсе.
	instance string
	// devMode — §85.8: установка тестовая. Форме входа этого достаточно, чтобы
	// решить, подставлять ли логин `admin`; больше признаков среды наружу не
	// отдаётся.
	devMode bool
	// passwordResetReady — §88.4.5: восстановление пароля работоспособно
	// (почта включена и настроена, функция разрешена, задан публичный адрес).
	// Форма входа по нему решает, показывать ли ссылку «Забыли пароль?».
	//
	// Провайдер, а не чтение из БД: этот эндпоинт публичный, его опрашивают
	// соседние инстансы (§73), и на боевой установке он не делает ни одного
	// запроса в базу. Значение живёт в памяти процесса и обновляется по
	// reload-событию секции mail. nil → false (fail-closed).
	passwordResetReady func() bool
}

// NewVersionHandler — version/commit/buildDate обычно из cfg.Build; overrideAllowed
// = cfg.Web.AllowVersionOverride; override — провайдер из app_settings (может быть nil).
func NewVersionHandler(version, commit, buildDate string, overrideAllowed bool, override func(ctx context.Context) string, instance string, devMode bool) *VersionHandler {
	return &VersionHandler{
		version:         version,
		commit:          commit,
		buildDate:       buildDate,
		overrideAllowed: overrideAllowed,
		override:        override,
		instance:        instance,
		devMode:         devMode,
	}
}

// WithPasswordResetReady подключает провайдер признака §88.4.5.
// Builder-паттерн: у конструктора и так семь позиционных аргументов, восьмой
// сделал бы вызов нечитаемым.
func (h *VersionHandler) WithPasswordResetReady(ready func() bool) *VersionHandler {
	h.passwordResetReady = ready
	return h
}

// Get godoc
// @Summary  Версия приложения (§30, §34.3).
// @Description  Публичный read-only эндпоинт: версия Web Service + commit/build_date. Не требует авторизации. В dev (override_allowed) version может быть переопределена настройкой.
// @Tags     meta
// @Produce  json
// @Success  200  {object}  VersionResponse
// @Router   /api/version [get]
func (h *VersionHandler) Get(c *gin.Context) {
	version := h.version
	if h.overrideAllowed && h.override != nil {
		if ov := h.override(c.Request.Context()); ov != "" {
			version = ov
		}
	}
	resetReady := false
	if h.passwordResetReady != nil {
		resetReady = h.passwordResetReady()
	}
	c.JSON(http.StatusOK, VersionResponse{
		Version:            version,
		Commit:             h.commit,
		BuildDate:          h.buildDate,
		OverrideAllowed:    h.overrideAllowed,
		Instance:           h.instance,
		DevMode:            h.devMode,
		PasswordResetReady: resetReady,
	})
}
