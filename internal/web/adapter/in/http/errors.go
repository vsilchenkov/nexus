package http

import (
	"github.com/gin-gonic/gin"

	"nexus/internal/platform/i18n"
)

// localizedError отвечает HTTP-статусом и JSON-телом {"error": <переведённое сообщение>}.
// key — i18n-ключ (например, "node.not_found"). Если ключ не найден — вернётся сам key.
// Используется для всех типовых "domain → HTTP" ответов в Web API.
func localizedError(c *gin.Context, status int, key string) {
	c.JSON(status, gin.H{"error": i18n.Translate(i18n.FromGin(c), key)})
}
