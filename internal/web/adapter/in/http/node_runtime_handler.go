package http

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
)

// NodeRuntimeHandler — исход последнего исходящего вызова узла (§84.7).
type NodeRuntimeHandler struct {
	uc     *usecase.NodeRuntimeUsecase
	logger logging.Logger
}

func NewNodeRuntimeHandler(uc *usecase.NodeRuntimeUsecase, logger logging.Logger) *NodeRuntimeHandler {
	return &NodeRuntimeHandler{uc: uc, logger: logger}
}

// nodeRuntimeDTO — runtime-состояние узла.
//
// available=false означает «неизвестно»: ни Redis, ни Prometheus не дали
// значения. Интерфейс в этом случае не рисует бейдж вовсе — «неизвестно» НЕ
// красится в ok, потому что зелёная плашка при неизвестном состоянии
// успокаивает ровно тогда, когда этого делать нельзя.
type nodeRuntimeDTO struct {
	Available   bool   `json:"available"`
	LastOutcome string `json:"last_outcome"` // ok | degraded | down
	Source      string `json:"source"`       // redis | prometheus | none
}

// State godoc
// @Summary  Исход последнего исходящего вызова узла (§84.7).
// @Description  Runtime-состояние узла для шапки страницы: ok / degraded / down по модели §52. Источник — персистентный Redis (переживает рестарт Sender), fallback — instant-gauge Prometheus. available=false означает «неизвестно» (ни один источник не ответил), и это НЕ то же самое, что ok. Доступно всем ролям: понимать, почему узел молчит, нужно и наблюдателю.
// @Tags     nodes
// @Produce  json
// @Param    id  path  string  true  "node id"
// @Success  200  {object}  nodeRuntimeDTO
// @Failure  404  {object}  ErrorResponse
// @Failure  500  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/nodes/{id}/runtime [get]
func (h *NodeRuntimeHandler) State(c *gin.Context) {
	st, err := h.uc.State(c.Request.Context(), c.Param("id"), currentTeamID(c))
	if err != nil {
		if errors.Is(err, domain.ErrNodeNotFound) {
			localizedError(c, http.StatusNotFound, "node.not_found")
			return
		}
		h.logger.ErrorWithOp("node runtime failed", err, "node.runtime",
			h.logger.Str("node_id", c.Param("id")))
		localizedError(c, http.StatusInternalServerError, "error.internal")
		return
	}
	c.JSON(http.StatusOK, nodeRuntimeDTO{
		Available:   st.Available,
		LastOutcome: string(st.LastOutcome),
		Source:      st.Source,
	})
}
