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
	CHSchema      *CHSchemaHandler
	HostAllowlist *HostAllowlistHandler
	HeaderCatalog *HeaderCatalogHandler
	RequestField  *RequestFieldCatalogHandler
	RMQTest       *RMQTestHandler
	Kafka         *KafkaHandler
	AsyncQueue    *AsyncQueueHandler
	ServiceLogs   *ServiceLogsHandler
}

// Middlewares — общие middleware (auth-check, role-check, API token-check).
type Middlewares struct {
	APITokenAuth   gin.HandlerFunc // пытается auth по API token (Bearer db_*); пропускает, если не наш токен
	SessionAuth    gin.HandlerFunc // session-cookie auth
	RequireAdmin   gin.HandlerFunc // роль admin
	RequireManager gin.HandlerFunc // роль не ниже manager (manager+admin), §26
	KafkaRateLimit gin.HandlerFunc // §9 spec: лимит /api/kafka/* на пользователя
	CSRFCheck      gin.HandlerFunc // Phase AUD.4: Origin-check мутаций под session-cookie
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
	// CSRF Origin-check на весь /api/* (включая login): браузерная мутация
	// с чужим Origin отклоняется до auth/handler'ов. Reverse-proxy /api/v1/*
	// регистрируется отдельно (receiver_proxy) и не затрагивается.
	if mw.CSRFCheck != nil {
		api.Use(mw.CSRFCheck)
	}
	{
		api.POST("/auth/login", h.Auth.Login)

		// RequirePasswordChanged (П18): пока сессия в режиме «требуется смена
		// пароля», все эндпоинты ниже отдают 403 password_change_required,
		// кроме /me/password и auth-служебных (allowlist внутри middleware).
		authed := api.Group("/", mw.APITokenAuth, mw.SessionAuth, RequirePasswordChanged())
		authed.POST("/auth/logout", h.Auth.Logout)
		authed.GET("/auth/me", h.Auth.Me)

		// Team-switcher (multi-tenancy v2, §16 ТЗ). Только session-cookie:
		// API-токены ограничены одной командой по token.team_id.
		authed.GET("/me/teams", h.Auth.MyTeams)
		authed.POST("/me/switch-team", RequireSessionOnly(), h.Auth.SwitchTeam)
		// §49: избранные команды — self-service, только session-cookie
		// (API-токен ограничен одной командой, избранное ему ни к чему).
		authed.PUT("/me/favorite-teams", RequireSessionOnly(), h.Auth.SetFavoriteTeams)
		// §62: история поиска узлов — self-service, только session-cookie
		// (кросс-командный поиск для однокомандных API-токенов бессмыслен).
		authed.GET("/me/search-history", RequireSessionOnly(), h.Auth.SearchHistory)
		authed.POST("/me/search-history", RequireSessionOnly(), h.Auth.RecordSearch)
		authed.DELETE("/me/search-history", RequireSessionOnly(), h.Auth.ClearSearchHistory)
		// Self-service смена собственного пароля (§26): любая роль, только
		// session-cookie (API-токенам пароль менять незачем).
		authed.POST("/me/password", RequireSessionOnly(), h.Auth.ChangeOwnPassword)

		// API-токены — собственные, без scope (только session-cookie).
		authed.GET("/tokens", h.Token.List)
		authed.POST("/tokens", h.Token.Create)
		authed.POST("/tokens/:id/revoke", h.Token.Revoke)
		authed.POST("/tokens/:id/rotate", h.Token.Rotate)
		authed.DELETE("/tokens/:id", h.Token.Delete)

		// Чтение узлов: scope nodes:read для API tokens.
		authed.GET("/nodes", RequireScope("nodes:read"), h.Node.List)
		authed.GET("/nodes/:id", RequireScope("nodes:read"), h.Node.Get)
		// §58: команда узла для авто-переключения при открытии шаренной ссылки.
		// Только session-cookie: API-токены однокомандные и команду не меняют.
		// Проверяет членство пользователя — узел чужой команды скрыт (404, no-leak).
		authed.GET("/nodes/:id/team", RequireSessionOnly(), h.Node.ResolveTeam)

		// §62: глобальный поиск узлов по всем командам пользователя. Только
		// session-cookie: кросс-командная выдача однокомандным API-токенам
		// бессмысленна (как и /nodes/:id/team, /me/switch-team). Отдельный
		// префикс /search/*, а не /nodes/search: static-сегмент "search"
		// конфликтовал бы с wildcard ":id" из "/nodes/:id" в gin-роутере (ср.
		// приём с "log" vs "logs" ниже).
		authed.GET("/search/nodes", RequireSessionOnly(), h.Node.SearchAcrossTeams)

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

		// Справочник полей запроса (§41). GET — combobox любой сессии; POST —
		// manager (создание из формы узла) ниже.
		if h.RequestField != nil {
			authed.GET("/request-fields", h.RequestField.Search)
		}

		// Логи узла (§7.4): snapshot + SSE live-tail.
		// Регистрируются только если включён ClickHouse (см. app.go).
		if h.Logs != nil {
			authed.GET("/nodes/:id/logs", RequireScope("logs:read"), h.Logs.List)
			// Полное тело одной записи (§7.4.1): list/stream отдают только
			// метаданные, тела request/response тянутся лениво по клику на
			// строку. Путь "log" (не "logs") — чтобы не конфликтовать с
			// статическим сегментом ".../logs/stream" в gin-роутере.
			authed.GET("/nodes/:id/log/:logId", RequireScope("logs:read"), h.Logs.Get)
			// §42: срез тела по рунам (постраничная подгрузка «показать весь») и
			// потоковое скачивание тела файлом — большой ответ не вешает фронт.
			authed.GET("/nodes/:id/log/:logId/body", RequireScope("logs:read"), h.Logs.GetBody)
			authed.GET("/nodes/:id/log/:logId/body/download", RequireScope("logs:read"), h.Logs.GetBodyDownload)
			// §35: дешёвый счётчик неудач (done=0) для KPI вкладки «Очередь».
			authed.GET("/nodes/:id/logs/failed-count", RequireScope("logs:read"), h.Logs.CountFailed)
			// §48.3: фасеты фильтра логов — уникальные method (дропдаун) и
			// min/max дат (ограничение полей). Лениво дёргаются UI при
			// открытии списка / фокусе поля — данные всегда свежие.
			authed.GET("/nodes/:id/logs/methods", RequireScope("logs:read"), h.Logs.Methods)
			authed.GET("/nodes/:id/logs/date-range", RequireScope("logs:read"), h.Logs.DateRange)
			// SSE доступен только UI-сессиям (§7.14: для API-токенов — только
			// snapshot).
			authed.GET("/nodes/:id/logs/stream", RequireSessionOnly(), h.Logs.Stream)
		}

		// Метрики панели (§21): KPI/throughput/график. Read-only, scope
		// metrics:read для API-токенов. Эндпоинты деградируют, если нет
		// Prometheus/ClickHouse (см. MetricsUsecase), поэтому регистрируются
		// всегда.
		if h.Metrics != nil {
			authed.GET("/metrics/overview", RequireScope("metrics:read"), h.Metrics.Overview)
			authed.GET("/metrics/nodes", RequireScope("metrics:read"), h.Metrics.NodesOverview)
			authed.GET("/metrics/nodes/:id", RequireScope("metrics:read"), h.Metrics.Node)
			// §44.E: сверка Prometheus↔ClickHouse (диагностика расхождений счётчиков).
			authed.GET("/metrics/diagnostics", RequireScope("metrics:read"), h.Metrics.Diagnostics)
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
		// §35: лёгкая смена статуса (пауза/отключение) — manager+.
		authedManager.PATCH("/nodes/:id/status", h.Node.UpdateStatus)
		// §53: клонирование узла (включая креды) с новым path, копия — paused.
		authedManager.POST("/nodes/:id/copy", h.Node.Copy)
		authedManager.DELETE("/nodes/:id", h.Node.Delete)
		// §7.5.1: dry-run без сохранения конфига.
		authedManager.POST("/nodes/dry-run", h.DryRun.Run)
		// §7.4.1/§58: replay записи лога пере-отправляет запрос на внешнюю цель
		// (сайд-эффект) — manager+, только session-cookie. viewer видит кнопку
		// disabled (нет прав), API-токены реплеить не могут (логи — только snapshot).
		if h.Replay != nil {
			authedManager.POST("/logs/:id/replay", RequireSessionOnly(), h.Replay.Replay)
		}
		// §56: синхронизация схемы CH-таблицы узла (ALTER). Plan — предпросмотр
		// (read-only), Apply — исполнение. manager+ (как и правка узла).
		if h.CHSchema != nil {
			authedManager.POST("/nodes/:id/ch-schema/plan", RequireSessionOnly(), h.CHSchema.Plan)
			authedManager.POST("/nodes/:id/ch-schema/apply", RequireSessionOnly(), h.CHSchema.Apply)
		}
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

		// Справочник полей запроса (§41): create из combobox.
		if h.RequestField != nil {
			authedManager.POST("/request-fields", h.RequestField.Create)
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
		authedAdmin.GET("/nodes/:id/move-preview", h.Node.MovePreview)

		authedAdmin.GET("/users", h.User.List)
		authedAdmin.GET("/users/:id", h.User.Get)
		authedAdmin.POST("/users", h.User.Create)
		authedAdmin.PUT("/users/:id", h.User.Update)
		authedAdmin.PUT("/users/:id/default-team", h.User.SetDefaultTeam) // §45
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

		// Консоль служебных логов (§51.5): хвост slog-логов трёх сервисов из
		// Redis-колец + скачивание файлом. Admin-only, НЕ путать с per-node
		// /nodes/:id/logs (ClickHouse). Смена уровня — PUT /settings/app.
		if h.ServiceLogs != nil {
			authedAdmin.GET("/logs", h.ServiceLogs.List)
			authedAdmin.GET("/logs/download", h.ServiceLogs.Download)
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

		// Справочник заголовков (§24): переименование/удаление — admin-only
		// (страница управления в Настройках). GET/POST остаются выше (combobox
		// формы узла: чтение — любая сессия, idempotent create — manager+).
		if h.HeaderCatalog != nil {
			authedAdmin.PATCH("/headers/:id", h.HeaderCatalog.Update)
			authedAdmin.DELETE("/headers/:id", h.HeaderCatalog.Delete)
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

		// Управление async-очередью узла (§34.4): manager+ (роль «Управление
		// узлами»), node-scoped. Раньше было admin-only; открыто manager по
		// запросу — управление очередью узла относится к управлению узлом (как
		// пауза/отключение §35.4 и replay §58). Чтение деградирует
		// (kafka_available), мутации под глобальным CSRF и тем же rate-limit,
		// что Kafka-экран (защита от peek-флуда брокеров).
		if h.AsyncQueue != nil {
			aq := authedManager.Group("/nodes/:id/async-queue")
			if mw.KafkaRateLimit != nil {
				aq.Use(mw.KafkaRateLimit)
			}
			aq.GET("/messages", h.AsyncQueue.List)
			aq.GET("/messages/body", h.AsyncQueue.Body)
			aq.DELETE("/messages/:msgId", h.AsyncQueue.DeleteOne)
			aq.POST("/purge", h.AsyncQueue.Purge)
			aq.POST("/purge-failed", h.AsyncQueue.PurgeFailed)
			// §36.11: «Повторить все сейчас» — handler в ReplayHandler (нужен
			// dispatcher), маршрут manager+ под async-queue, как purge-failed.
			if h.Replay != nil {
				aq.POST("/replay-failed", h.Replay.ReplayFailed)
			}
		}
	}
}
