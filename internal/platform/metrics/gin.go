package metrics

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// GinMiddleware считает nexus_requests_total и nexus_request_duration_seconds
// для каждого HTTP-запроса.
//
// method = "request" | "requestAsync" для V1-маршрутов проксирования,
// либо имя Gin-route'а (FullPath) для всех остальных endpoint'ов — это
// сразу группирует /api/nodes/:id и подобные в один ряд, а не плодит
// кардинальность по id.
//
// node = резолвнутый путь узла. Receiver кладёт его в контекст под ключом
// NodeLabelKey (без слога команды — совпадает с меткой node у Sender, иначе
// per-node merge in/out на дашборде разъезжается); fallback — path-параметр
// после /api/v1/request/.../ для V1, либо "" для остальных.
//
// /metrics и /health не учитываются, чтобы не зашумлять данные.
func GinMiddleware(m *Metrics) gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		c.Next()

		path := c.FullPath()
		switch path {
		case "", "/metrics", "/health", "/ready":
			return
		}

		method := rootMethodFromPath(path)
		if method == "" {
			method = c.Request.Method + " " + path
		}
		node := nodeLabel(c)
		status := strconv.Itoa(c.Writer.Status())

		m.RequestsTotal.WithLabelValues(method, node, status).Inc()
		m.RequestDuration.WithLabelValues(method, node).Observe(time.Since(started).Seconds())
	}
}

// rootMethodFromPath — short-name для двух главных V1-маршрутов Receiver'а.
// Для всех остальных возвращает "" (caller подставит свой fallback).
func rootMethodFromPath(fullPath string) string {
	switch fullPath {
	case "/api/v1/request/*path":
		return "request"
	case "/api/v1/requestAsync/*path":
		return "requestAsync"
	}
	return ""
}

// NodeLabelKey — ключ gin-контекста, под которым обработчик может положить
// канонический путь узла (node.Path, без слога команды). Если задан —
// GinMiddleware пишет его в метку node вместо сырого path-параметра.
const NodeLabelKey = "nexus_node"

// nodeLabel — канонический путь узла из контекста (если обработчик его положил),
// иначе fallback на сырой path-параметр URL.
func nodeLabel(c *gin.Context) string {
	if v := c.GetString(NodeLabelKey); v != "" {
		return v
	}
	return nodePathFromGin(c)
}

// nodePathFromGin — значение path-параметра без ведущего "/".
func nodePathFromGin(c *gin.Context) string {
	p := c.Param("path")
	if p == "" {
		return ""
	}
	if p[0] == '/' {
		return p[1:]
	}
	return p
}
