package http

import (
	"github.com/gin-gonic/gin"
)

// Handlers — bag всех HTTP-handler'ов Web Service.
type Handlers struct {
	Auth *AuthHandler
	Node *NodeHandler
	User *UserHandler
}

// Middlewares — общие middleware (auth-check, role-check).
type Middlewares struct {
	Auth        gin.HandlerFunc
	RequireAdmin gin.HandlerFunc
}

// RegisterAPI вешает /api/* маршруты.
// Логин — публичный; всё остальное — за auth-middleware; users-CRUD — admin only.
func RegisterAPI(r *gin.Engine, h Handlers, mw Middlewares) {
	api := r.Group("/api")
	{
		// public
		api.POST("/auth/login", h.Auth.Login)

		// authed
		authed := api.Group("/", mw.Auth)
		authed.POST("/auth/logout", h.Auth.Logout)
		authed.GET("/auth/me", h.Auth.Me)

		authed.GET("/nodes", h.Node.List)
		authed.GET("/nodes/:id", h.Node.Get)
		// мутации узлов — только admin (viewer см. §7.3)
		authedAdmin := authed.Group("/", mw.RequireAdmin)
		authedAdmin.POST("/nodes", h.Node.Create)
		authedAdmin.PUT("/nodes/:id", h.Node.Update)
		authedAdmin.DELETE("/nodes/:id", h.Node.Delete)

		authedAdmin.GET("/users", h.User.List)
		authedAdmin.GET("/users/:id", h.User.Get)
		authedAdmin.POST("/users", h.User.Create)
		authedAdmin.PUT("/users/:id", h.User.Update)
		authedAdmin.DELETE("/users/:id", h.User.Delete)
		authedAdmin.POST("/users/:id/password", h.User.ChangePassword)
	}
}
