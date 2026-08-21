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
// §78.2: у боевого трафика Receiver один общий маршрут /api/v1/*path (короткая
// форма адреса узла несовместима с отдельными маршрутами — см. Handler.Register),
// поэтому обработчик кладёт метку в контекст под RootMethodLabelKey, и она
// имеет приоритет над выводом из FullPath. Значения прежние: смена формы адреса
// не должна разводить один и тот же трафик по разным рядам Prometheus (на
// method="requestAsync" стоит дашборд Kafka, на "request" — алерт латентности).
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

		method := c.GetString(RootMethodLabelKey)
		if method == "" {
			method = rootMethodFromPath(path)
		}
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

// NodeUnresolved — метка node для запросов к НЕСУЩЕСТВУЮЩЕМУ узлу (§94.8).
//
// Путь в таком запросе задаёт клиент, а метка входит в идентичность ряда:
// сканер по случайным адресам плодил бы ряд на каждую попытку, и они остаются
// в Prometheus навсегда. Обработчик подменяет метку на этот маркер, а
// детализацию по путям даёт журнал отказов §94.
const NodeUnresolved = "<unresolved>"

// RootMethodLabelKey — ключ gin-контекста для метки method (§78.2). Обработчик
// боевого маршрута кладёт туда "request" | "requestAsync" | "callback" |
// RootMethodShortURL; выводить метку из имени маршрута нельзя — он один на все
// формы адреса узла.
const RootMethodLabelKey = "nexus_root_method"

const (
	// RootMethodCallback — метка webhook-callback'ов (§16). До §78 они метились
	// именем маршрута ("POST /api/v1/callback/*path"); в дашбордах и алертах эта
	// строка не используется.
	RootMethodCallback = "callback"
	// RootMethodShortURL — метка обращений по короткому адресу §78.1, у которых
	// узел не разрезолвился (404). Отдельный ряд: мусорные обращения не должны
	// подмешиваться в боевые request/requestAsync.
	RootMethodShortURL = "route"
)

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
