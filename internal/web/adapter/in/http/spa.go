package http

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// SPAFallback регистрирует handler для статики SPA + fallback на index.html
// для всех путей, не начинающихся с /api/, /swagger/, /metrics, /health, /ready.
//
// Использует embed.FS из internal/web/static.
func SPAFallback(r *gin.Engine, embedFS fs.FS) {
	indexBytes, err := fs.ReadFile(embedFS, "index.html")
	if err != nil {
		// В режиме без embedded UI просто отдаём короткое сообщение.
		indexBytes = []byte("<html><body>DataBus UI not embedded</body></html>")
	}

	// NoRoute уже определён в Handler.Register; перепишем его — теперь
	// fallback не на 404, а на index.html (с теми же исключениями).
	r.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		if isAPIOrInfra(p) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", indexBytes)
	})

	// Прямой / тоже отдаёт SPA index.
	r.GET("/", func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", indexBytes)
	})
}

func isAPIOrInfra(p string) bool {
	switch {
	case strings.HasPrefix(p, "/api/"),
		strings.HasPrefix(p, "/swagger/"),
		p == "/metrics", p == "/health", p == "/ready":
		return true
	}
	return false
}
