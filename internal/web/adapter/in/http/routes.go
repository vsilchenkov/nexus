package http

import (
	"github.com/gin-gonic/gin"
)

// Handlers — bag всех HTTP-handler'ов Web Service.
type Handlers struct {
	Auth        *AuthHandler
	Node        *NodeHandler
	User        *UserHandler
	Token       *APITokenHandler
	Team        *TeamHandler
	Audit       *AuditHandler
	DryRun      *DryRunHandler
	Replay      *ReplayHandler
	Logs        *LogsHandler
	AppSettings *AppSettingsHandler
	Orphan      *OrphanHandler
	CHTemplate  *CHTemplateHandler
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
//
//	APITokenAuth — если есть Bearer db_*, аутентифицирует; иначе passthrough.
//	SessionAuth  — если ctxSession ещё не выставлен (API-токеном) — проверяет cookie.
//	RequireAdmin — для admin-only роутов.
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

		// Team-switcher (multi-tenancy v2, §16 ТЗ). Только session-cookie:
		// API-токены ограничены одной командой по token.team_id.
		authed.GET("/me/teams", h.Auth.MyTeams)
		authed.POST("/me/switch-team", RequireSessionOnly(), h.Auth.SwitchTeam)

		// API-токены — собственные, без scope (только session-cookie).
		authed.GET("/tokens", h.Token.List)
		authed.POST("/tokens", h.Token.Create)
		authed.POST("/tokens/:id/revoke", h.Token.Revoke)
		authed.DELETE("/tokens/:id", h.Token.Delete)

		// Чтение узлов: scope nodes:read для API tokens.
		authed.GET("/nodes", RequireScope("nodes:read"), h.Node.List)
		authed.GET("/nodes/:id", RequireScope("nodes:read"), h.Node.Get)

		// Шаблоны CH-таблиц (§19). GET доступен любой сессии (селектор
		// при настройке узла); мутации/verify — admin-only ниже.
		if h.CHTemplate != nil {
			authed.GET("/ch-templates", h.CHTemplate.List)
			authed.GET("/ch-templates/:id", h.CHTemplate.Get)
		}

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
		// Перенос узла в другую команду (multi-tenancy v2, Phase 11.B).
		authedAdmin.POST("/nodes/:id/move", h.Node.Move)
		// §7.5.1: dry-run без сохранения конфига. Только admin.
		authedAdmin.POST("/nodes/dry-run", h.DryRun.Run)

		authedAdmin.GET("/users", h.User.List)
		authedAdmin.GET("/users/:id", h.User.Get)
		authedAdmin.POST("/users", h.User.Create)
		authedAdmin.PUT("/users/:id", h.User.Update)
		authedAdmin.DELETE("/users/:id", h.User.Delete)
		authedAdmin.POST("/users/:id/password", h.User.ChangePassword)

		// Teams (multi-tenancy v2, Phase 10.C). Admin-only.
		// Регистрируется только если есть TeamHandler (Web запущен с
		// доступным ClickHouse — иначе TeamUsecase не сможет provision'ить).
		if h.Team != nil {
			authedAdmin.GET("/teams", h.Team.List)
			authedAdmin.POST("/teams", h.Team.Create)
			authedAdmin.GET("/teams/:id", h.Team.Get)
			authedAdmin.PUT("/teams/:id", h.Team.Update)
			authedAdmin.DELETE("/teams/:id", h.Team.Delete)
			authedAdmin.GET("/teams/:id/members", h.Team.ListMembers)
			authedAdmin.POST("/teams/:id/members", h.Team.AddMember)
			authedAdmin.PUT("/teams/:id/members/:user_id", h.Team.UpdateMemberRole)
			authedAdmin.DELETE("/teams/:id/members/:user_id", h.Team.RemoveMember)
		}

		// Audit log: admin-only; scope audit:read нужен только для API-токена.
		authedAdmin.GET("/audit", RequireScope("audit:read"), h.Audit.List)
		// CSV-экспорт журнала (§7.13, Phase 6.6).
		authedAdmin.GET("/audit/export.csv", RequireScope("audit:read"), h.Audit.ExportCSV)

		// Dynamic-настройки Sentry/ClickHouse (§14.5). Admin-only.
		if h.AppSettings != nil {
			authedAdmin.GET("/settings/app", h.AppSettings.Get)
			authedAdmin.PUT("/settings/app", h.AppSettings.Update)
			// Test connection с patch'ем настроек (Phase 6.3.2.6, §7.10).
			authedAdmin.POST("/settings/clickhouse/test", h.AppSettings.TestClickHouse)
			authedAdmin.POST("/settings/sentry/test", h.AppSettings.TestSentry)
			// Тестовое уведомление в Telegram (§20.7).
			authedAdmin.POST("/settings/notifications/test", h.AppSettings.TestTelegram)
		}

		// Шаблоны CH-таблиц (§19): мутации и verify — admin-only.
		if h.CHTemplate != nil {
			authedAdmin.POST("/ch-templates", h.CHTemplate.Create)
			authedAdmin.PUT("/ch-templates/:id", h.CHTemplate.Update)
			authedAdmin.DELETE("/ch-templates/:id", h.CHTemplate.Delete)
			authedAdmin.POST("/ch-templates/verify", h.CHTemplate.Verify)
		}

		// Orphan-таблицы ClickHouse (§7.10 / Phase 6.7). Admin-only.
		// Регистрируется только если включён ClickHouse (см. app.go).
		if h.Orphan != nil {
			authedAdmin.GET("/settings/clickhouse/orphans", h.Orphan.List)
			authedAdmin.DELETE("/settings/clickhouse/orphans/:table", h.Orphan.Drop)
		}
	}
}
