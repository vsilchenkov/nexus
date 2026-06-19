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
}

// NewVersionHandler — version/commit/buildDate обычно из cfg.Build; overrideAllowed
// = cfg.Web.AllowVersionOverride; override — провайдер из app_settings (может быть nil).
func NewVersionHandler(version, commit, buildDate string, overrideAllowed bool, override func(ctx context.Context) string) *VersionHandler {
	return &VersionHandler{
		version:         version,
		commit:          commit,
		buildDate:       buildDate,
		overrideAllowed: overrideAllowed,
		override:        override,
	}
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
	c.JSON(http.StatusOK, VersionResponse{
		Version:         version,
		Commit:          h.commit,
		BuildDate:       h.buildDate,
		OverrideAllowed: h.overrideAllowed,
	})
}
