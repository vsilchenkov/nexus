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
// node = path-параметр после /api/v1/request/.../ для V1, либо "" для остальных.
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
		node := nodePathFromGin(c)
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
