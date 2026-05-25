package http

import "github.com/gin-gonic/gin"

// Handlers — bag всех HTTP-handler'ов Web Service.
type Handlers struct {
	Node *NodeHandler
}

// RegisterAPI вешает /api/* маршруты.
//
// В Phase 1 без middleware — auth добавляется в Phase 3 (см. §7.1).
// До этого CRUD узлов доступен анонимно; не разворачивайте Phase 1
// в production без reverse-proxy с авторизацией.
func RegisterAPI(r *gin.Engine, h Handlers) {
	api := r.Group("/api")
	{
		api.GET("/nodes", h.Node.List)
		api.POST("/nodes", h.Node.Create)
		api.GET("/nodes/:id", h.Node.Get)
		api.PUT("/nodes/:id", h.Node.Update)
		api.DELETE("/nodes/:id", h.Node.Delete)
	}
}
