package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func (h *Handler) swagger(c *gin.Context) {

	c.Redirect(http.StatusMovedPermanently, "/swagger/index.html")

}
