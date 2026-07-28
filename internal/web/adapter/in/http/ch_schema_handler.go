package http

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
)

// CHSchemaHandler — синхронизация схемы CH-таблицы узла (§56): предпросмотр DDL
// и явное применение ALTER'ов. manager+ (регистрируется в authedManager).
type CHSchemaHandler struct {
	uc     *usecase.CHSchemaSyncUsecase
	logger logging.Logger
}

func NewCHSchemaHandler(uc *usecase.CHSchemaSyncUsecase, logger logging.Logger) *CHSchemaHandler {
	return &CHSchemaHandler{uc: uc, logger: logger}
}

// Plan godoc
// @Summary  Предпросмотр синхронизации схемы CH-таблицы узла (§56).
// @Description  Read-only: читает схему существующей таблицы из ClickHouse, диффит против шаблона узла и возвращает ALTER-операторы + список неприменимого (движок/ORDER BY/PARTITION BY — нужна пересоздача). ClickHouse не меняет.
// @Tags     nodes
// @Produce  json
// @Param    id   path  string  true  "node id"
// @Success  200  {object}  usecase.SchemaSyncResult
// @Failure  404  {object}  ErrorResponse
// @Failure  503  {object}  ErrorResponse  "ClickHouse недоступен"
// @Security CookieAuth
// @Router   /api/nodes/{id}/ch-schema/plan [post]
func (h *CHSchemaHandler) Plan(c *gin.Context) {
	res, err := h.uc.Plan(c.Request.Context(), c.Param("id"), currentTeamID(c))
	if err != nil {
		h.replyErr(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

// Apply godoc
// @Summary  Применить синхронизацию схемы CH-таблицы узла (§56).
// @Description  Исполняет применимые ALTER'ы по порядку (CODEC / ADD·DROP INDEX / MODIFY·REMOVE TTL). Неприменимое возвращается, но не блокирует остальное. Пишет аудит node.ch_schema_sync. Операции ClickHouse могут быть тяжёлыми/частично необратимыми.
// @Tags     nodes
// @Produce  json
// @Param    id   path  string  true  "node id"
// @Success  200  {object}  usecase.SchemaSyncResult
// @Failure  404  {object}  ErrorResponse
// @Failure  503  {object}  ErrorResponse  "ClickHouse недоступен"
// @Security CookieAuth
// @Router   /api/nodes/{id}/ch-schema/apply [post]
func (h *CHSchemaHandler) Apply(c *gin.Context) {
	res, err := h.uc.Apply(c.Request.Context(), actorFromCtx(c), c.Param("id"), currentTeamID(c))
	if err != nil {
		h.replyErr(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

func (h *CHSchemaHandler) replyErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, domain.ErrNodeNotFound):
		localizedError(c, http.StatusNotFound, "node.not_found")
	case errors.Is(err, usecase.ErrCHUnavailable):
		localizedError(c, http.StatusServiceUnavailable, "ch_template.unavailable")
	case errors.Is(err, domain.ErrNodeExternalTable):
		// §64: не 400 — запрос корректен, но состояние узла делает операцию
		// недопустимой (внешней таблицей Nexus не управляет).
		localizedError(c, http.StatusConflict, "ch_sync.external_table_forbidden")
	case errors.Is(err, domain.ErrCHForeignDatabase):
		// §70.4: таблица принадлежит другой ноде — ALTER'ы по ней запрещены.
		// Тоже 409: запрос корректен, недопустимо состояние.
		localizedError(c, http.StatusConflict, "ch_sync.foreign_database")
	default:
		h.logger.ErrorWithOp("ch schema sync failed", err, "node.ch_schema_sync")
		localizedError(c, http.StatusInternalServerError, "error.internal")
	}
}
