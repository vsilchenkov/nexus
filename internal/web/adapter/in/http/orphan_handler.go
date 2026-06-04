package http

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
)

// OrphanHandler — GET /api/settings/clickhouse/orphans + DELETE /api/settings/clickhouse/orphans/:table
// (§7.10 / Phase 6.7). Admin-only.
type OrphanHandler struct {
	scanner *usecase.OrphanScanner
	logger  logging.Logger
}

func NewOrphanHandler(scanner *usecase.OrphanScanner, logger logging.Logger) *OrphanHandler {
	return &OrphanHandler{scanner: scanner, logger: logger}
}

type orphanItemResp struct {
	Database   string `json:"database"`
	Table      string `json:"table"`
	FullName   string `json:"full_name"`
	Engine     string `json:"engine"`
	TotalRows  uint64 `json:"total_rows"`
	TotalBytes uint64 `json:"total_bytes"`
	CreatedAt  string `json:"created_at"`
}

// List godoc
// @Summary  Поиск orphan-таблиц в ClickHouse (§7.10).
// @Description  Возвращает таблицы из текущей CH-базы, у которых нет соответствующего узла в Postgres.
// @Tags     settings
// @Produce  json
// @Success  200  {object}  ListOrphansResponse
// @Failure  500  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/settings/clickhouse/orphans [get]
func (h *OrphanHandler) List(c *gin.Context) {
	items, err := h.scanner.Scan(c.Request.Context())
	if err != nil {
		h.logger.ErrorWithOp("orphan scan failed", err, "settings.orphan_scan")
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	out := make([]orphanItemResp, 0, len(items))
	for _, it := range items {
		out = append(out, orphanItemResp{
			Database:   it.Database,
			Table:      it.Table,
			FullName:   it.FullName,
			Engine:     it.Engine,
			TotalRows:  it.TotalRows,
			TotalBytes: it.TotalBytes,
			CreatedAt:  it.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// Drop godoc
// @Summary  Удалить orphan-таблицу (§7.10).
// @Description  DROP TABLE IF EXISTS db.table. Защита: имя валидируется, повторно проверяется orphan-статус, действие пишется в audit.
// @Tags     settings
// @Param    table  path  string  true  "Полное имя db.table"
// @Produce  json
// @Success  204
// @Failure  400  {object}  ErrorResponse
// @Failure  500  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/settings/clickhouse/orphans/{table} [delete]
func (h *OrphanHandler) Drop(c *gin.Context) {
	table := c.Param("table")
	if table == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "table is required"})
		return
	}
	if err := h.scanner.Drop(c.Request.Context(), actorFromCtx(c), table); err != nil {
		h.logger.ErrorWithOp("orphan drop failed", err, "settings.orphan_drop")
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}
