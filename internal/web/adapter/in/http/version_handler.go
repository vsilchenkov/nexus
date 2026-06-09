package http

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// VersionHandler — публичный эндпоинт версии приложения (§30 ТЗ).
//
// Версия берётся из build-конфига (cfg.Build.Version): ldflags при сборке из git
// (make / docker compose build) или fallback versioninfo.json. Не требует авторизации —
// SPA показывает версию в футере в т.ч. до входа.
type VersionHandler struct {
	version string
}

// NewVersionHandler — version обычно равен cfg.Build.Version.
func NewVersionHandler(version string) *VersionHandler {
	return &VersionHandler{version: version}
}

// Get godoc
// @Summary  Версия приложения (§30).
// @Description  Публичный read-only эндпоинт: текущая версия Web Service. Не требует авторизации.
// @Tags     meta
// @Produce  json
// @Success  200  {object}  VersionResponse
// @Router   /api/version [get]
func (h *VersionHandler) Get(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"version": h.version})
}
