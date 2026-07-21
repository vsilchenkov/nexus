package http

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
)

// CHTableVerifyHandler — проверка структуры внешней таблицы логов (§64).
// manager+ (регистрируется в authedManager).
type CHTableVerifyHandler struct {
	uc     *usecase.CHTableVerifyUsecase
	logger logging.Logger
}

func NewCHTableVerifyHandler(uc *usecase.CHTableVerifyUsecase, logger logging.Logger) *CHTableVerifyHandler {
	return &CHTableVerifyHandler{uc: uc, logger: logger}
}

// CHTableVerifyRequest — тело POST /api/ch-tables/verify.
//
// Проверяется имя таблицы, а не id узла: кнопка «Проверить» должна работать в
// форме ещё не сохранённого узла.
type CHTableVerifyRequest struct {
	Table string `json:"table" binding:"required,max=129"`
}

// Verify godoc
// @Summary  Проверить структуру внешней таблицы логов (§64).
// @Description  Read-only: сверяет колонки и типы указанной ClickHouse-таблицы с обязательной схемой логов. Лишние колонки допускаются. Расхождения возвращаются в теле с ok=false (200), а не как ошибка запроса. ClickHouse не меняет.
// @Tags     nodes
// @Accept   json
// @Produce  json
// @Param    body  body  CHTableVerifyRequest  true  "имя таблицы db.table"
// @Success  200  {object}  usecase.CHTableVerifyResult
// @Failure  400  {object}  ErrorResponse  "некорректное имя таблицы"
// @Failure  503  {object}  ErrorResponse  "ClickHouse недоступен"
// @Security CookieAuth
// @Router   /api/ch-tables/verify [post]
func (h *CHTableVerifyHandler) Verify(c *gin.Context) {
	var req CHTableVerifyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	res, err := h.uc.Verify(c.Request.Context(), req.Table)
	switch {
	case err == nil:
		c.JSON(http.StatusOK, res)
	case errors.Is(err, domain.ErrNodeClickHouseTableInvalid):
		localizedError(c, http.StatusBadRequest, "node.validation.clickhouse_table_format")
	case errors.Is(err, usecase.ErrCHUnavailable):
		localizedError(c, http.StatusServiceUnavailable, "ch_template.unavailable")
	default:
		h.logger.ErrorWithOp("ch table verify failed", err, "node.ch_table_verify")
		localizedError(c, http.StatusInternalServerError, "error.internal")
	}
}
