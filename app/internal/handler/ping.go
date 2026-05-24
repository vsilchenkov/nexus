package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// @Summary Ping
// @Tags Health
// @Description Healthcheck endpoint
// @ID ping
// @Produce json
// @Success 200 {object} map[string]string
// @Router /api/ping [get]
func (h *Handler) ping(c *gin.Context) {

	const op = "handler.ping"

	msg, err := h.services.Ping(c.Request.Context())
	if err != nil {
		newErrorResponse(c, http.StatusInternalServerError, op, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": msg})
}
