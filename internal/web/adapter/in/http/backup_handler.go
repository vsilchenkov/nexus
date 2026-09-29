package http

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"nexus/internal/domain/backup"
	"nexus/internal/platform/backupfile"
	"nexus/internal/platform/i18n"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
)

// backupService — операции раздела «Резервная копия», нужные handler'у.
type backupService interface {
	Status(ctx context.Context) (*usecase.BackupStatus, error)
	Export(ctx context.Context, actor usecase.Actor, passphrase string) (*usecase.BackupFile, error)
}

// BackupHandler — резервная копия конфигурации (§110). Все маршруты
// admin-only и только из браузерной сессии: файл несёт креды всех узлов и
// хеши паролей, API-токену с командной областью он недоступен.
type BackupHandler struct {
	uc     backupService
	logger logging.Logger
}

func NewBackupHandler(uc backupService, logger logging.Logger) *BackupHandler {
	return &BackupHandler{uc: uc, logger: logger}
}

// exportRequest — тело POST /api/backup/export. Пароль передаётся телом, а не
// в query: адрес запроса оседает в логах прокси.
type exportRequest struct {
	Passphrase string `json:"passphrase" binding:"required,max=1024"`
}

// BackupStatusResponse — состояние экземпляра для карточки «Резервная копия».
type BackupStatusResponse struct {
	Nodes     int `json:"nodes"`
	Teams     int `json:"teams"`
	Users     int `json:"users"`
	APITokens int `json:"api_tokens"`
	// Clean — чистый экземпляр (нет узлов, команд сверх сидированной):
	// единственное состояние, в котором доступно восстановление.
	Clean               bool   `json:"clean"`
	ClickHouseAvailable bool   `json:"clickhouse_available"`
	InstanceID          string `json:"instance_id"`
	NexusVersion        string `json:"nexus_version"`
	SchemaVersion       uint   `json:"schema_version"`
	// MinPassphraseLen — нижняя граница пароля файла, чтобы форма не держала
	// своё число.
	MinPassphraseLen int `json:"min_passphrase_len"`
}

// Status godoc
// @Summary  Состояние резервной копии (§110).
// @Description  Счётчики конфигурации для карточки выгрузки и признаки, от которых зависит доступность восстановления: чистый экземпляр и подключённый ClickHouse. Admin-only, только session-cookie.
// @Tags     backup
// @Produce  json
// @Success  200  {object}  BackupStatusResponse
// @Failure  500  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/backup/status [get]
func (h *BackupHandler) Status(c *gin.Context) {
	st, err := h.uc.Status(c.Request.Context())
	if err != nil {
		h.replyError(c, err, "backup.status")
		return
	}
	c.JSON(http.StatusOK, BackupStatusResponse{
		Nodes: st.Nodes, Teams: st.Teams, Users: st.Users, APITokens: st.APITokens,
		Clean: st.Fresh, ClickHouseAvailable: st.CHAvailable,
		InstanceID: st.InstanceID, NexusVersion: st.NexusVersion, SchemaVersion: st.SchemaVersion,
		MinPassphraseLen: backupfile.MinPassphraseLen,
	})
}

// Export godoc
// @Summary  Выгрузить конфигурацию в файл (§110).
// @Description  Команды, узлы с кредами, пользователи, справочники, шаблоны ClickHouse, маски и глобальные настройки — одним файлом, зашифрованным паролем (Argon2id + AES-256-GCM). Логи обмена и журнал аудита не выгружаются. Admin-only, только session-cookie.
// @Tags     backup
// @Accept   json
// @Produce  application/octet-stream
// @Param    body  body  exportRequest  true  "Пароль файла"
// @Success  200  {file}    file
// @Failure  400  {object}  ErrorResponse  "пароль короче допустимого"
// @Failure  500  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/backup/export [post]
func (h *BackupHandler) Export(c *gin.Context) {
	var req exportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.replyCode(c, http.StatusBadRequest, "backup.passphrase_required")
		return
	}
	f, err := h.uc.Export(c.Request.Context(), actorFromCtx(c), req.Passphrase)
	if err != nil {
		h.replyError(c, err, "backup.export")
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, f.Name))
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Length", strconv.Itoa(len(f.Data)))
	c.Data(http.StatusOK, "application/octet-stream", f.Data)
}

func (h *BackupHandler) replyError(c *gin.Context, err error, op string) {
	switch {
	case errors.Is(err, backupfile.ErrPassphraseTooShort):
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf(i18n.Translate(i18n.FromGin(c), "backup.passphrase_too_short"),
				backupfile.MinPassphraseLen),
			"code": "backup.passphrase_too_short",
		})
	case errors.Is(err, backup.ErrInvalidSnapshot):
		// Своя база не проходит проверку восстановления — это дефект, который
		// надо разбирать по журналу сервиса; подробности (id строк) — там.
		logServerError(c, h.logger, "backup handler: snapshot invalid", err, op)
		h.replyCode(c, http.StatusInternalServerError, "backup.snapshot_invalid")
	default:
		logServerError(c, h.logger, "backup handler error", err, op,
			h.logger.Str("path", c.Request.URL.Path))
		localizedError(c, http.StatusInternalServerError, "error.internal")
	}
}

func (h *BackupHandler) replyCode(c *gin.Context, status int, code string) {
	c.JSON(status, gin.H{"error": i18n.Translate(i18n.FromGin(c), code), "code": code})
}
