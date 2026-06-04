package http

import (
	"github.com/gin-gonic/gin"
)

// Handlers — bag всех HTTP-handler'ов Web Service.
type Handlers struct {
	Auth          *AuthHandler
	Node          *NodeHandler
	User          *UserHandler
	Token         *APITokenHandler
	Team          *TeamHandler
	Audit         *AuditHandler
	DryRun        *DryRunHandler
	Replay        *ReplayHandler
	Logs          *LogsHandler
	Metrics       *MetricsHandler
	AppSettings   *AppSettingsHandler
	Orphan        *OrphanHandler
	CHTemplate    *CHTemplateHandler
	HostAllowlist *HostAllowlistHandler
	HeaderCatalog *HeaderCatalogHandler
	RMQTest       *RMQTestHandler
	Kafka         *KafkaHandler
}

// Middlewares — общие middleware (auth-check, role-check, API token-check).
type Middlewares struct {
	APITokenAuth   gin.HandlerFunc // пытается auth по API token (Bearer db_*); пропускает, если не наш токен
	SessionAuth    gin.HandlerFunc // session-cookie auth
	RequireAdmin   gin.HandlerFunc // роль admin
	RequireManager gin.HandlerFunc // роль не ниже manager (manager+admin), §26
	KafkaRateLimit gin.HandlerFunc // §9 spec: лимит /api/kafka/* на пользователя
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
		// Self-service смена собственного пароля (§26): любая роль, только
		// session-cookie (API-токенам пароль менять незачем).
		authed.POST("/me/password", RequireSessionOnly(), h.Auth.ChangeOwnPassword)

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

		// Каталог разрешённых хостов (§23). GET доступен любой сессии (combobox
		// формы узла); мутации и привязки — admin-only ниже.
		if h.HostAllowlist != nil {
			authed.GET("/allowed-hosts", h.HostAllowlist.Search)
			authed.GET("/nodes/:id/allowed-hosts", RequireScope("nodes:read"), h.HostAllowlist.ListByNode)
		}

		// Справочник заголовков (§24). GET — combobox любой сессии; POST —
		// admin (создание из формы узла) ниже.
		if h.HeaderCatalog != nil {
			authed.GET("/headers", h.HeaderCatalog.Search)
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

		// Метрики панели (§21): KPI/throughput/график. Read-only, scope
		// metrics:read для API-токенов. Эндпоинты деградируют, если нет
		// Prometheus/ClickHouse (см. MetricsUsecase), поэтому регистрируются
		// всегда.
		if h.Metrics != nil {
			authed.GET("/metrics/overview", RequireScope("metrics:read"), h.Metrics.Overview)
			authed.GET("/metrics/nodes", RequireScope("metrics:read"), h.Metrics.NodesOverview)
			authed.GET("/metrics/nodes/:id", RequireScope("metrics:read"), h.Metrics.Node)
		}

		// Публичный адрес приложения (§28, Пункт 1): read-only для любого
		// авторизованного — UI собирает полный адрес узла. PUT/секреты
		// остаются admin-only ниже (/settings/app).
		if h.AppSettings != nil {
			authed.GET("/settings/public", h.AppSettings.GetPublic)
		}

		// Mutating — только session-cookie (API-токены сюда не пускаем).
		//
		// authedManager (роль manager+admin, §26): управление узлами и
		// связанными каталогами + чтение Audit log.
		authedManager := authed.Group("/", mw.RequireManager)
		authedManager.POST("/nodes", h.Node.Create)
		authedManager.PUT("/nodes/:id", h.Node.Update)
		authedManager.DELETE("/nodes/:id", h.Node.Delete)
		// §7.5.1: dry-run без сохранения конфига.
		authedManager.POST("/nodes/dry-run", h.DryRun.Run)
		// §27.8: проверка подключения к RabbitMQ (только session, manager+,
		// rate-limit внутри handler'а). Регистрируется только если включён.
		if h.RMQTest != nil {
			authedManager.POST("/nodes/test-rmq", RequireSessionOnly(), h.RMQTest.TestRMQ)
		}

		// Каталог разрешённых хостов (§23): мутации каталога и привязки.
		if h.HostAllowlist != nil {
			authedManager.POST("/allowed-hosts", h.HostAllowlist.Create)
			authedManager.PATCH("/allowed-hosts/:id", h.HostAllowlist.Update)
			authedManager.DELETE("/allowed-hosts/:id", h.HostAllowlist.Delete)
			authedManager.POST("/allowed-hosts/preview", h.HostAllowlist.Preview)
			authedManager.POST("/nodes/:id/allowed-hosts", h.HostAllowlist.Attach)
			authedManager.DELETE("/nodes/:id/allowed-hosts/:host_id", h.HostAllowlist.Detach)
		}

		// Справочник заголовков (§24): create из combobox.
		if h.HeaderCatalog != nil {
			authedManager.POST("/headers", h.HeaderCatalog.Create)
		}

		// Audit log: чтение доступно manager+admin (§26); scope audit:read нужен
		// только для API-токена.
		authedManager.GET("/audit", RequireScope("audit:read"), h.Audit.List)
		// CSV-экспорт журнала (§7.13, Phase 6.6).
		authedManager.GET("/audit/export.csv", RequireScope("audit:read"), h.Audit.ExportCSV)

		// authedAdmin — только admin: перенос узлов между командами, управление
		// пользователями/командами, общие настройки и шаблоны CH.
		authedAdmin := authed.Group("/", mw.RequireAdmin)
		// Перенос узла в другую команду (multi-tenancy v2, Phase 11.B). Admin-only.
		authedAdmin.POST("/nodes/:id/move", h.Node.Move)

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

		// Мониторинг Kafka (§4 spec): admin-only, read-only. Источники
		// деградируют (флаги *_available), поэтому регистрируется всегда.
		// Отдельный rate-limit на пользователя (§9) на всю группу /kafka/*.
		if h.Kafka != nil {
			kafka := authedAdmin.Group("/kafka", mw.KafkaRateLimit)
			kafka.GET("/overview", h.Kafka.Overview)
			kafka.GET("/timeseries", h.Kafka.Timeseries)
			kafka.GET("/topics", h.Kafka.Topics)
			kafka.GET("/by-node", h.Kafka.ByNode)
			kafka.POST("/test", h.Kafka.Test)
		}
	}
}
