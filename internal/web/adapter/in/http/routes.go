package http

import (
	"github.com/gin-gonic/gin"
)

// Handlers — bag всех HTTP-handler'ов Web Service.
type Handlers struct {
	Auth   *AuthHandler
	Node   *NodeHandler
	User   *UserHandler
	Token  *APITokenHandler
	Audit  *AuditHandler
	DryRun *DryRunHandler
	Replay *ReplayHandler
	Logs   *LogsHandler
}

// Middlewares — общие middleware (auth-check, role-check, API token-check).
type Middlewares struct {
	APITokenAuth gin.HandlerFunc // пытается auth по API token (Bearer db_*); пропускает, если не наш токен
	SessionAuth  gin.HandlerFunc // session-cookie auth
	RequireAdmin gin.HandlerFunc
}

// RegisterAPI вешает /api/* маршруты.
//
// Pipeline:
//   APITokenAuth — если есть Bearer db_*, аутентифицирует; иначе passthrough.
//   SessionAuth  — если ctxSession ещё не выставлен (API-токеном) — проверяет cookie.
//   RequireAdmin — для admin-only роутов.
//
// API-токены вызывают только GET-эндпоинты (read-only, §7.14);
// для каждого нужен соответствующий scope через RequireScope.
func RegisterAPI(r *gin.Engine, h Handlers, mw Middlewares) {
	api := r.Group("/api")
	{
		api.POST("/auth/login", h.Auth.Login)

		authed := api.Group("/", mw.APITokenAuth, mw.SessionAuth)
		authed.POST("/auth/logout", h.Auth.Logout)
		authed.GET("/auth/me", h.Auth.Me)

		// API-токены — собственные, без scope (только session-cookie).
		authed.GET("/tokens", h.Token.List)
		authed.POST("/tokens", h.Token.Create)
		authed.POST("/tokens/:id/revoke", h.Token.Revoke)
		authed.DELETE("/tokens/:id", h.Token.Delete)

		// Чтение узлов: scope nodes:read для API tokens.
		authed.GET("/nodes", RequireScope("nodes:read"), h.Node.List)
		authed.GET("/nodes/:id", RequireScope("nodes:read"), h.Node.Get)

		// Логи узла (§7.4): snapshot + SSE live-tail.
		// Регистрируются только если включён ClickHouse (см. app.go).
		if h.Logs != nil {
			authed.GET("/nodes/:id/logs", RequireScope("logs:read"), h.Logs.List)
			// SSE доступен только UI-сессиям (§7.14: для API-токенов — только
			// snapshot).
			authed.GET("/nodes/:id/logs/stream", RequireSessionOnly(), h.Logs.Stream)
		}

		// Replay одного запроса (§7.4.1): только session-cookie (mutating).
		if h.Replay != nil {
			authed.POST("/logs/:id/replay", h.Replay.Replay)
		}

		// Mutating — только session-cookie + admin (API-токены сюда не пускаем).
		authedAdmin := authed.Group("/", mw.RequireAdmin)
		authedAdmin.POST("/nodes", h.Node.Create)
		authedAdmin.PUT("/nodes/:id", h.Node.Update)
		authedAdmin.DELETE("/nodes/:id", h.Node.Delete)
		// §7.5.1: dry-run без сохранения конфига. Только admin.
		authedAdmin.POST("/nodes/dry-run", h.DryRun.Run)

		authedAdmin.GET("/users", h.User.List)
		authedAdmin.GET("/users/:id", h.User.Get)
		authedAdmin.POST("/users", h.User.Create)
		authedAdmin.PUT("/users/:id", h.User.Update)
		authedAdmin.DELETE("/users/:id", h.User.Delete)
		authedAdmin.POST("/users/:id/password", h.User.ChangePassword)

		// Audit log: admin-only; scope audit:read нужен только для API-токена.
		authedAdmin.GET("/audit", RequireScope("audit:read"), h.Audit.List)
	}
}
