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
	"nexus/internal/web/usecase/port"
)

// backupService — операции раздела «Резервная копия», нужные handler'у.
type backupService interface {
	Status(ctx context.Context) (*usecase.BackupStatus, error)
	Export(ctx context.Context, actor usecase.Actor, passphrase string) (*usecase.BackupFile, error)
	PreviewImport(ctx context.Context, actor usecase.Actor, req usecase.BackupImportRequest) (*usecase.BackupImportPreview, error)
	ApplyImport(ctx context.Context, actor usecase.Actor, req usecase.BackupApplyRequest) (*usecase.BackupImportResult, error)
}

// backupImportMaxBody — потолок тела импорта. Файл конфигурации — сотни КиБ
// даже на сотнях узлов; распакованное содержимое отдельно ограничено
// backupfile.MaxPayloadBytes.
const backupImportMaxBody = 64 << 20

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

// importPreviewRequest — тело POST /api/backup/import/preview. Файл — его
// текст как есть (строкой): тот же текст приходит в apply, и дайджест
// предпросмотра совпадает байт в байт.
type importPreviewRequest struct {
	File       string                      `json:"file" binding:"required"`
	Passphrase string                      `json:"passphrase" binding:"required,max=1024"`
	Options    usecase.BackupImportOptions `json:"options"`
}

// PreviewImport godoc
// @Summary  Предпросмотр восстановления конфигурации (§110).
// @Description  Только на чистом экземпляре (нет узлов и команд, кроме Default) — иначе 409 без расшифровки файла. Расшифровывает файл, сопоставляет его с тем, что есть на экземпляре, опрашивает ClickHouse (базы и таблицы команд) и выполняет всю запись в PostgreSQL с откатом. Ответ — план, предупреждения, блокирующие проблемы и digest для применения. Admin-only, только session-cookie.
// @Tags     backup
// @Accept   json
// @Produce  json
// @Param    body  body  importPreviewRequest  true  "Файл, пароль, опции"
// @Success  200  {object}  usecase.BackupImportPreview
// @Failure  400  {object}  ErrorResponse  "неверный пароль, не файл резервной копии, неверные опции"
// @Failure  409  {object}  ErrorResponse  "экземпляр не чистый, ClickHouse не подключён, импорт уже идёт"
// @Failure  413  {object}  ErrorResponse  "файл слишком большой"
// @Failure  422  {object}  ErrorResponse  "файл новее этой версии или не проходит проверку"
// @Security CookieAuth
// @Router   /api/backup/import/preview [post]
func (h *BackupHandler) PreviewImport(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, backupImportMaxBody)
	var req importPreviewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.replyBindError(c, err)
		return
	}
	p, err := h.uc.PreviewImport(c.Request.Context(), actorFromCtx(c), usecase.BackupImportRequest{
		File: []byte(req.File), Passphrase: req.Passphrase, Options: req.Options, SelfHost: c.Request.Host,
	})
	if err != nil {
		h.replyImportError(c, err, "backup.import_preview")
		return
	}
	c.JSON(http.StatusOK, p)
}

// importApplyRequest — тело POST /api/backup/import/apply: то же, что у
// предпросмотра, плюс его дайджест и адрес сервера, введённый в диалоге.
type importApplyRequest struct {
	importPreviewRequest
	PreviewDigest string `json:"preview_digest" binding:"required"`
	ConfirmHost   string `json:"confirm_host" binding:"required,max=255"`
}

// ApplyImport godoc
// @Summary  Восстановить конфигурацию из файла (§110).
// @Description  Пересобирает план и сверяет его с preview_digest (иначе 409), сверяет confirm_host с адресом сервера, создаёт базы и таблицы ClickHouse и одной транзакцией записывает PostgreSQL (под блокировкой teams/nodes с повторной проверкой чистоты). Выполняется до конца даже при закрытии вкладки. Admin-only, только session-cookie.
// @Tags     backup
// @Accept   json
// @Produce  json
// @Param    body  body  importApplyRequest  true  "Файл, пароль, опции, дайджест, адрес"
// @Success  200  {object}  usecase.BackupImportResult
// @Failure  400  {object}  ErrorResponse  "неверный пароль, адрес не совпал"
// @Failure  409  {object}  ErrorResponse  "не тот предпросмотр, экземпляр не чистый, блокирующие проблемы, импорт уже идёт"
// @Failure  413  {object}  ErrorResponse
// @Failure  422  {object}  ErrorResponse
// @Failure  500  {object}  ErrorResponse  "сбой ClickHouse или записи"
// @Security CookieAuth
// @Router   /api/backup/import/apply [post]
func (h *BackupHandler) ApplyImport(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, backupImportMaxBody)
	var req importApplyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.replyBindError(c, err)
		return
	}
	res, err := h.uc.ApplyImport(c.Request.Context(), actorFromCtx(c), usecase.BackupApplyRequest{
		File: []byte(req.File), Passphrase: req.Passphrase, Options: req.Options, SelfHost: c.Request.Host,
		Digest:      req.PreviewDigest,
		ConfirmHost: req.ConfirmHost,
	})
	if err != nil {
		h.replyImportError(c, err, "backup.import_apply")
		return
	}
	c.JSON(http.StatusOK, res)
}

func (h *BackupHandler) replyBindError(c *gin.Context, err error) {
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		h.replyCode(c, http.StatusRequestEntityTooLarge, "backup.file_too_large")
		return
	}
	h.replyCode(c, http.StatusBadRequest, "backup.import_request_invalid")
}

// importErrorCodes — отказы восстановления: статус и ключ перевода.
var importErrorCodes = []struct {
	err    error
	status int
	code   string
}{
	{port.ErrTargetNotClean, http.StatusConflict, "backup.target_not_clean"},
	{port.ErrRestoreInProgress, http.StatusConflict, "backup.restore_in_progress"},
	{usecase.ErrBackupCHUnavailable, http.StatusConflict, "backup.clickhouse_unavailable"},
	{usecase.ErrBackupOptionsInvalid, http.StatusBadRequest, "backup.options_invalid"},
	{usecase.ErrBackupSchemaNewer, http.StatusUnprocessableEntity, "backup.schema_newer"},
	{backupfile.ErrDecrypt, http.StatusBadRequest, "backup.decrypt_failed"},
	{backupfile.ErrNotBackupFile, http.StatusBadRequest, "backup.not_backup_file"},
	{backupfile.ErrBadKDFParams, http.StatusBadRequest, "backup.not_backup_file"},
	{backupfile.ErrUnsupportedVersion, http.StatusUnprocessableEntity, "backup.unsupported_version"},
	{backupfile.ErrPayloadTooLarge, http.StatusRequestEntityTooLarge, "backup.file_too_large"},
	{usecase.ErrBackupConfirmHost, http.StatusBadRequest, "backup.confirm_host_mismatch"},
	{usecase.ErrBackupDigestMismatch, http.StatusConflict, "backup.digest_mismatch"},
	{usecase.ErrBackupBlocked, http.StatusConflict, "backup.import_blocked"},
}

func (h *BackupHandler) replyImportError(c *gin.Context, err error, op string) {
	for _, e := range importErrorCodes {
		if errors.Is(err, e.err) {
			h.replyCode(c, e.status, e.code)
			return
		}
	}
	if errors.Is(err, usecase.ErrBackupCHProvision) {
		// PostgreSQL не тронут; причина (DDL, недоступность) нужна
		// администратору, чтобы исправить и повторить.
		logServerError(c, h.logger, "backup handler: clickhouse provisioning failed", err, op)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":  i18n.Translate(i18n.FromGin(c), "backup.clickhouse_failed"),
			"code":   "backup.clickhouse_failed",
			"detail": err.Error(),
		})
		return
	}
	if errors.Is(err, backup.ErrInvalidSnapshot) {
		// Подробности (какие строки файла ссылаются в никуда) нужны
		// администратору здесь же: файл чужой, журнала его сервиса может не быть.
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error":  i18n.Translate(i18n.FromGin(c), "backup.file_invalid"),
			"code":   "backup.file_invalid",
			"detail": err.Error(),
		})
		return
	}
	h.replyError(c, err, op)
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
