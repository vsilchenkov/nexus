// Package web — Web Service (§7 и §11 ТЗ).
//
// В Phase 1 — healthcheck + REST /api/nodes CRUD. SPA через embed.FS,
// аутентификация, Swagger, audit log — Phase 3.
package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"
	swaggerfiles "github.com/swaggo/files"
	ginswagger "github.com/swaggo/gin-swagger"

	// Регистрируют Swagger-доки в swag.Registry при импорте (§11, §25 ТЗ):
	// web (instance "swagger") и receiver (instance "receiver").
	_ "nexus/docs/receiver"
	_ "nexus/docs/web"
	"nexus/internal/domain"
	"nexus/internal/platform/bootstrap"
	chpf "nexus/internal/platform/clickhouse"
	"nexus/internal/platform/clock"
	"nexus/internal/platform/config"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/grpcsender"
	"nexus/internal/platform/healthcheck"
	"nexus/internal/platform/i18n"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/mail"
	"nexus/internal/platform/metrics"
	"nexus/internal/platform/nodeevents"
	otelpf "nexus/internal/platform/otel"
	pgpf "nexus/internal/platform/pg"
	"nexus/internal/platform/queuecancel"
	"nexus/internal/platform/ratelimit"
	recoverypf "nexus/internal/platform/recovery"
	redispf "nexus/internal/platform/redis"
	"nexus/internal/platform/reloader"
	"nexus/internal/platform/requestid"
	"nexus/internal/platform/safego"
	sentrypf "nexus/internal/platform/sentry"
	"nexus/internal/platform/telegram"
	httpadapter "nexus/internal/web/adapter/in/http"
	chreader "nexus/internal/web/adapter/out/clickhouse"
	"nexus/internal/web/adapter/out/instanceprobe"
	kafkaadmin "nexus/internal/web/adapter/out/kafkaadmin"
	pgrepo "nexus/internal/web/adapter/out/postgres"
	prometheusreader "nexus/internal/web/adapter/out/prometheus"
	rabbitmqadapter "nexus/internal/web/adapter/out/rabbitmq"
	rcvdispatcher "nexus/internal/web/adapter/out/receiver"
	rediscache "nexus/internal/web/adapter/out/redis"
	"nexus/internal/web/static"
	"nexus/internal/web/usecase"
	webport "nexus/internal/web/usecase/port"
)

// replayDispatchTimeout — таймаут HTTP-диспетчера replay (Web → Receiver):
// 600с максимального timeout_ms узла + 10с запас на шину. Раньше стоял хардкод
// 30с, и sync-replay долгого узла обрывался. Ретраи узла
// (timeout*(retry_count+1)+backoff) всё ещё могут не уложиться — осознанное
// ограничение, replay вернёт ошибку сети, а Sender дошлёт и залогирует.
const replayDispatchTimeout = 610 * time.Second

type App struct {
	cfg     *config.Config
	logger  logging.Logger
	pg      *pgxpool.Pool
	redis   *goredis.Client
	ch      chdriver.Conn
	chMgr   *chpf.Manager
	cipher  *crypto.Cipher
	metrics *metrics.Metrics

	srv          *http.Server
	otelShutdown otelpf.ShutdownFunc
	// senderClient — §55: gRPC-пул к Sender для dry-run в реальном режиме.
	// nil, если web.sender_grpc.addr не задан. Закрывается в Stop.
	senderClient *grpcsender.Client

	// done-каналы фоновых горутин — Stop дожидается их завершения
	// (Phase AUD.3): callbacks reload'а/housekeeping не должны бежать
	// параллельно с закрытием pg/redis/CH-соединений.
	notifDone        <-chan struct{}
	reloadDone       <-chan struct{}
	housekeepingDone <-chan struct{}
	shipperDone      <-chan struct{} // §51: done-канал шиппера служебных логов

	// §51: ручка runtime-уровня логов + кольцо для Redis-шиппера.
	logCtl *bootstrap.LogController

	// identity — §70: идентификатор ноды и признаки первого запуска. Определяет
	// имена БД ClickHouse новых команд и поведение гейта владения при старте.
	identity bootstrap.Identity
}

func New(cfg *config.Config, pg *pgxpool.Pool, redis *goredis.Client, ch chdriver.Conn, cipher *crypto.Cipher, otelShutdown otelpf.ShutdownFunc, identity bootstrap.Identity, schema bootstrap.SchemaState, logger logging.Logger, logCtl *bootstrap.LogController) *App {
	m := metrics.New("web", metrics.WithInstance(identity.ID.String()))
	// §74.3: состояние схемы — в мониторинг. Стартовый лог виден только в момент
	// запуска, а незавершённый откат нужно замечать и через сутки после него.
	m.SetSchemaState(schema.Version, schema.Ahead)
	return &App{
		cfg:          cfg,
		logger:       logger,
		pg:           pg,
		redis:        redis,
		ch:           ch,
		cipher:       cipher,
		metrics:      m,
		otelShutdown: otelShutdown,
		logCtl:       logCtl,
		identity:     identity,
	}
}

func (a *App) Start(ctx context.Context) error {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	// Phase AUD.5: X-Forwarded-For доверяем только перечисленным прокси
	// (дефолт — loopback + приватные сети), иначе клиент подделывает IP в аудите.
	if err := r.SetTrustedProxies(a.cfg.Web.TrustedProxies); err != nil {
		return fmt.Errorf("web trusted_proxies: %w", err)
	}
	r.Use(
		requestid.GinMiddleware(),
		otelpf.GinMiddleware("web"),
		sentrypf.GinMiddleware("web"),
		recoverypf.GinMiddleware(a.logger),
		metrics.GinMiddleware(a.metrics),
		i18n.GinMiddleware(),
		httpadapter.SecurityHeaders(), // Phase AUD.4: CSP/nosniff/frame-deny
	)

	// Phase AUD.4: cookie без Secure при SameSite=None бесполезна и опасна —
	// браузеры такие куки отбрасывают, а CSRF-защита SameSite исчезает.
	if strings.EqualFold(a.cfg.Web.SessionCookieSamesite, "none") && !a.cfg.Web.SessionCookieSecure {
		a.logger.Warn("web.session_cookie_samesite=none без session_cookie_secure=true — " +
			"куки будут отброшены браузером; включите secure или верните samesite=strict")
	}

	hc := healthcheck.New(
		[]healthcheck.Checker{
			pgpf.HealthChecker("postgres", a.pg),
			redispf.HealthChecker("redis", a.redis),
		},
		nil,
	)
	hc.Register(r)
	r.GET("/metrics", gin.WrapH(a.metrics.Handler()))

	// Версия приложения (§30/§34.3): публичный read-only эндпоинт регистрируется
	// ниже, после создания appSettingsUC — он нужен провайдеру dev-override версии.

	// Swagger UI (§11, §25 ТЗ): Web раздаёт два дока (оба собираются `make
	// swagger` и встраиваются через embed-импорты выше).
	//   /swagger/web/      — Web Service API  (instance "swagger", default);
	//   /swagger/receiver/ — Receiver API     (instance "receiver").
	// Старый /swagger/index.html редиректится на web для совместимости.
	r.GET("/swagger/web/*any", ginswagger.WrapHandler(swaggerfiles.Handler, ginswagger.InstanceName("swagger")))
	r.GET("/swagger/receiver/*any", ginswagger.WrapHandler(swaggerfiles.Handler, ginswagger.InstanceName("receiver")))
	r.GET("/swagger", func(c *gin.Context) { c.Redirect(http.StatusMovedPermanently, "/swagger/web/index.html") })
	r.GET("/swagger/index.html", func(c *gin.Context) { c.Redirect(http.StatusMovedPermanently, "/swagger/web/index.html") })

	// Сборка слоёв (Clean Architecture, §17.2).
	// TeamRepo — multi-tenancy v2 (Phase 10.1 миграция 0008). Резолвим UUID
	// 'default'-team при старте — он используется как fallback в NodeUsecase,
	// OrphanScanner для legacy single-team-кода (до блока B с team-switcher).
	teamRepo := pgrepo.NewTeamRepoPg(a.pg, a.logger)
	defaultTeam, err := teamRepo.GetBySlug(ctx, domain.DefaultTeamSlug)
	if err != nil {
		return fmt.Errorf("resolve default team: %w (run --migrate-up?)", err)
	}
	defaultTeamID := defaultTeam.ID

	// ClickHouse Manager + team-provisioner создаём заранее (до NodeUsecase):
	// provisioner нужен NodeUsecase.Move (RENAME TABLE, Phase 11.B) и
	// TeamUsecase.Create (Phase 10.C). При отсутствии CH остаётся nil —
	// Move переносит только PG-метаданные, TeamUsecase.Create вернёт
	// ErrCHUnavailable. Тип переменной — интерфейс, чтобы nil-проверки в
	// usecase работали корректно (не nil-обёртка над nil-указателем).
	var teamProvisioner webport.TeamProvisioner
	var chGuard *chpf.Guard
	if a.ch != nil {
		a.chMgr = chpf.NewManager(a.ch, chpf.New, &a.cfg.ClickHouse, a.logger)
		// §70.3: гейт владения БД. Кеш вердиктов привязан к серверу, поэтому
		// сбрасывается после hot-reload соединения (адрес CH меняется из UI).
		chGuard = chpf.NewGuard(a.chMgr, a.identity.ID, a.logger)
		a.chMgr.OnReload(func() {
			chGuard.Invalidate()
			// Смена адреса ClickHouse из UI — единственный способ увести ноду на
			// сервер, где её маркеров нет. Тогда разрушающие операции начнут
			// отклоняться штатным гейтом, и без этой строки причина искалась бы
			// по косвенным признакам («housekeeping перестал чистить»).
			a.logger.Warn("clickhouse connection reloaded: ownership verdicts dropped, "+
				"they will be re-checked on the next operation",
				a.logger.Str("instance", a.identity.ID.String()))
		})

		prov := chreader.NewTeamProvisioner(a.chMgr, a.logger)
		prov.SetOwnership(chGuard)
		teamProvisioner = prov
	}

	nodeRepo := pgrepo.NewNodeRepoPg(a.pg, a.cipher, a.logger)

	// §37: миграция существующих CH-таблиц — добавить колонку node_id, иначе
	// SELECT по новой схеме упадёт. Идемпотентно (ALTER … IF NOT EXISTS), до
	// старта HTTP-сервера. Новые таблицы получают колонку из шаблона.
	//
	// Список таблиц — по ВСЕМ командам (ListClickHouseTables, без team-фильтра),
	// как у Sender. Раньше брался List(TeamID: defaultTeamID) → БД не-default
	// команд Web не альтерил вообще, и на мультикомандном бою чтение логов
	// падало с CH code 47 «Unknown expression identifier request_size» до
	// рестарта Sender'а (Sentry 158619).
	if a.chMgr != nil {
		if tables, err := nodeRepo.ListClickHouseTables(ctx); err != nil {
			a.logger.Warn("§37 ensure ch columns: list ch tables failed", a.logger.Err(err))
		} else {
			// §70.4: чужие таблицы из обслуживания исключаются — ADD COLUMN
			// фиксирует порядок колонок, а backfill вообще мутирует все строки.
			if chGuard != nil {
				tables = chGuard.FilterManagedTables(ctx, tables)
			}
			a.logger.Debug("ensure ch columns: tables collected", a.logger.Int("count", len(tables)))
			chpf.EnsureNodeIDColumn(ctx, a.chMgr.Conn(), tables, a.logger)
			chpf.EnsureHTTPMethodColumn(ctx, a.chMgr.Conn(), tables, a.logger) // §39
			chpf.EnsureClientHostColumn(ctx, a.chMgr.Conn(), tables, a.logger) // §67
			chpf.EnsureBodySizeColumns(ctx, a.chMgr.Conn(), tables, a.logger)  // §42-доп
			chpf.BackfillBodySizes(ctx, a.chMgr.Conn(), tables, a.logger)      // §42-доп
		}
	}

	nodeCache := rediscache.NewNodeCacheRedis(a.redis, a.cipher, a.logger)
	auditRepo := pgrepo.NewAuditRepoPg(a.pg, a.logger)
	// §86.7: членства нужны журналу только для сквозного режима scope=all;
	// на запись аудита (её делают все usecase) это не влияет.
	auditUC := usecase.NewAuditUsecase(auditRepo, a.logger).WithTeams(teamRepo)
	uow := pgrepo.NewUnitOfWorkPg(a.pg, a.cipher, a.logger)
	chTemplateRepo := pgrepo.NewCHTemplateRepoPg(a.pg, a.logger)
	// §32.2: список своих authority для self-reference валидации target_url.
	// Явный конфиг приоритетен; иначе выводим из ReceiverURL.
	selfIngressHosts := resolveSelfIngressHosts(a.cfg.Web.SelfIngressHosts, a.cfg.Web.ReceiverURL)
	nodeUC := usecase.NewNodeUsecase(
		nodeRepo,
		nodeCache,
		auditUC,
		uow,
		teamRepo,
		teamProvisioner,
		chTemplateRepo,
		time.Duration(a.cfg.Redis.NodeTTLSec)*time.Second,
		a.cfg.Web.NodesHardLimit,
		defaultTeamID,
		selfIngressHosts,
		a.logger,
	)
	// §57: гарантированная инвалидация конфига узла в Receiver — Web публикует
	// событие при изменении узла, Receiver выселяет его из кешей.
	nodeUC.SetInvalidationPublisher(nodeevents.NewPublisher(a.redis))
	// Перенос узла между командами не должен утаскивать таблицу логов, если её
	// делят другие узлы (тот же nodeRepo реализует port.NodeTableUsage).
	nodeUC.SetTableUsage(nodeRepo)
	// §70.6: узел не может ссылаться на таблицу в БД другой ноды (кроме режима
	// внешней таблицы §64).
	if chGuard != nil {
		nodeUC.SetOwnership(chGuard)
	}
	// §27.8: health-ридер Puller-воркеров из общего Redis-стора (rmq:health).
	rmqHealthReader := rediscache.NewRMQHealthReaderRedis(a.redis)
	nodeHandler := httpadapter.NewNodeHandler(nodeUC, rmqHealthReader, a.logger)

	userRepo := pgrepo.NewUserRepoPg(a.pg, a.logger)
	sessionRepo := rediscache.NewSessionRepoRedis(a.redis)
	// §34.2: длительность сессии через провайдер (live, без рестарта). Fallback —
	// env-конфиг; сидинг из app_settings и hot-reload (секция security) — ниже,
	// после создания appSettingsUC.
	sessionTTLProvider := usecase.NewSessionTTLProvider(a.cfg.Redis.SessionTTLSec)
	authUC := usecase.NewAuthUsecase(userRepo, sessionRepo, teamRepo, auditUC, sessionTTLProvider.Get, a.logger).
		WithFavoriteTeams(teamRepo). // §49: избранные команды (TeamRepoPg реализует и FavoriteTeamRepo)
		WithSearchHistory(userRepo)  // §62: история поиска узлов (UserRepoPg реализует SearchHistoryRepo)
	userUC := usecase.NewUserUsecase(userRepo, sessionRepo, teamRepo, auditUC, defaultTeamID, a.logger)
	// §71: персональные предпочтения (UserRepoPg реализует UserPreferenceRepo,
	// TeamRepoPg — TeamMembershipLister).
	prefUC := usecase.NewPreferenceUsecase(userRepo, teamRepo, a.logger)
	prefHandler := httpadapter.NewPreferenceHandler(prefUC, a.logger)

	tokenRepo := pgrepo.NewAPITokenRepoPg(a.pg, a.logger)
	// teamRepo — для проверки членства при выборе команды токена (§18.3).
	tokenUC := usecase.NewAPITokenUsecase(tokenRepo, userRepo, teamRepo, auditUC, a.logger)

	appSettingsRepo := pgrepo.NewAppSettingsRepoPg(a.pg, a.logger)
	reloadPublisher := reloader.NewPublisher(a.redis)
	appSettingsUC := usecase.NewAppSettingsUsecase(appSettingsRepo, auditUC, reloadPublisher, a.cfg.Web.AllowVersionOverride, a.logger)

	// Версия приложения (§30/§34.3): публичный эндпоинт на корневом движке (вне
	// auth-группы) — SPA показывает версию в футере (+ commit/build_date в
	// tooltip). В dev (allow_version_override) version можно переопределить через
	// app_settings.general.version_override.
	versionOverride := func(ctx context.Context) string {
		s, err := appSettingsUC.Raw(ctx)
		if err != nil || s == nil || s.General.VersionOverride == nil {
			return ""
		}
		return *s.General.VersionOverride
	}
	// §88.4.5: признак «восстановление доступно» держится в памяти процесса и
	// обновляется по reload-событию секции mail — публичный /api/version не
	// должен ходить в БД (его опрашивают и соседние инстансы §73).
	mailAvailability := usecase.NewMailAvailabilityProvider(appSettingsRepo, a.logger)
	if err := mailAvailability.Refresh(ctx); err != nil {
		a.logger.Warn("mail availability initial read failed", a.logger.Err(err))
	}

	r.GET("/api/version", httpadapter.NewVersionHandler(
		a.cfg.Build.Version, a.cfg.Build.Commit, a.cfg.Build.BuildDate,
		a.cfg.Web.AllowVersionOverride, versionOverride,
		a.identity.ID.String(), // §70.8: бейдж ноды в шапке
		a.cfg.Web.DevMode,      // §85.8: префилл логина только на стенде
	).WithPasswordResetReady(mailAvailability.Get).Get)
	// Telegram-клиент (§20): для тестовой отправки и планировщика уведомлений.
	telegramClient := telegram.New(a.logger)
	// SMTP-отправитель (§88.3): тестовое письмо и восстановление пароля.
	mailSender := mail.New(a.logger)
	// SettingsTester (Phase 6.3.2.6): test connection без сохранения.
	settingsTester := usecase.NewSettingsTester(
		appSettingsRepo, a.cfg, chpf.New, usecase.DefaultSentryClientFactory, telegramClient,
		mailSender, a.metrics,
		a.cfg.Build.ProjectName, a.cfg.Build.Version, a.logger,
	)

	// §51: шиппер служебных логов в Redis (nexus:logs:web).
	a.shipperDone = a.logCtl.StartRedisShipper(ctx, a.redis, a.logger)

	// Подписчик hot-reload (§14.5). Web сам слушает события, чтобы admin-инстансы
	// в кластере применили изменения, отправленные через другой инстанс.
	// ClickHouseReloader регистрируется ниже — после создания chMgr (если CH доступен).
	reloadSub := reloader.NewSubscriber(a.redis, a.logger)
	reloadSub.Register(reloader.SectionSentry,
		bootstrap.SentryReloader(a.pg, a.cfg, a.cfg.Build.ProjectName, a.cfg.Build.Version, a.cipher, a.logger))

	// §34.2: длительность сессии из app_settings (сидинг на старте + hot-reload
	// секции security). nil → сброс провайдера на env-fallback.
	applySessionTTL := func(ctx context.Context) error {
		s, err := appSettingsUC.Raw(ctx)
		if err != nil {
			return err
		}
		if s != nil && s.Security.SessionTTLSeconds != nil {
			sessionTTLProvider.Set(*s.Security.SessionTTLSeconds)
		} else {
			sessionTTLProvider.Set(0)
		}
		return nil
	}
	if err := applySessionTTL(ctx); err != nil {
		a.logger.Warn("seed session TTL from app_settings failed; using env fallback", a.logger.Err(err))
	}
	reloadSub.Register(reloader.SectionSecurity, applySessionTTL)

	// §88.4.5: единственный подписчик секции mail. Сам SMTP-транспорт
	// перезагружать нечего (соединение живёт одну отправку) — обновляется
	// признак доступности восстановления, который отдаёт /api/version.
	reloadSub.Register(reloader.SectionMail, mailAvailability.Refresh)

	// §51: runtime-уровень логов из app_settings.logging.level (+ сид старта).
	applyLogLevel := bootstrap.LogLevelReloader(a.pg, a.logCtl, a.cipher, a.logger)
	if err := applyLogLevel(ctx); err != nil {
		a.logger.Warn("seed log level from app_settings failed; using yaml level", a.logger.Err(err))
	}
	reloadSub.Register(reloader.SectionLogging, applyLogLevel)

	authHandler := httpadapter.NewAuthHandler(authUC, &a.cfg.Web, sessionTTLProvider.Get, a.logger)
	userHandler := httpadapter.NewUserHandler(userUC, authUC, a.logger)
	tokenHandler := httpadapter.NewAPITokenHandler(tokenUC, a.logger)
	auditHandler := httpadapter.NewAuditHandler(auditUC, a.logger)
	appSettingsHandler := httpadapter.NewAppSettingsHandler(
		appSettingsUC, settingsTester, a.cfg.Web.NodeDefaultMaxBodySize,
		a.identity.ID.CHDatabasePrefix(), // §70.8: предпросмотр имени БД команды
		a.logger)

	// §51: консоль служебных логов — хвост Redis-колец nexus:logs:* трёх
	// сервисов (admin-only, маршруты /api/logs*).
	serviceLogsUC := usecase.NewServiceLogsUsecase(rediscache.NewServiceLogReaderRedis(a.redis, a.logger), a.logger)
	serviceLogsHandler := httpadapter.NewServiceLogsHandler(serviceLogsUC, a.logger)

	// Шаблоны CH-таблиц (§19). chTemplateRepo создан выше (для NodeUsecase);
	// usecase/handler создаём всегда (GET работает без ClickHouse); provisioner
	// может быть nil — Verify тогда вернёт 503.
	chTemplateUC := usecase.NewCHTemplateUsecase(chTemplateRepo, teamProvisioner, teamRepo, auditUC, a.logger)
	chTemplateHandler := httpadapter.NewCHTemplateHandler(chTemplateUC, a.logger)

	// §56: синхронизация схемы CH-таблицы узла через ALTER. Инспектор nil при
	// отсутствии ClickHouse → usecase вернёт ErrCHUnavailable (503). Handler
	// создаём всегда: endpoint деградирует, а не исчезает.
	var chSchemaInspector webport.CHSchemaInspector
	// §64: тот же инспектор проверяет структуру внешней таблицы. Держим отдельную
	// переменную конкретного типа: присвоение nil-указателя в интерфейсную
	// переменную дало бы typed-nil, и проверка `columns == nil` в usecase не
	// сработала бы (вместо 503 был бы паник-nil при вызове).
	var chTableVerifyUC *usecase.CHTableVerifyUsecase
	if a.chMgr != nil {
		insp := chreader.NewSchemaInspector(a.chMgr, a.logger)
		// §70.4: ALTER'ы §56 действуют на таблицу целиком — на чужой запрещены.
		insp.SetOwnership(chGuard)
		chSchemaInspector = insp
		chTableVerifyUC = usecase.NewCHTableVerifyUsecase(insp, a.logger)
	} else {
		chTableVerifyUC = usecase.NewCHTableVerifyUsecase(nil, a.logger)
	}
	chSchemaUC := usecase.NewCHSchemaSyncUsecase(nodeUC, chTemplateRepo, chSchemaInspector, auditUC, a.logger)
	chSchemaHandler := httpadapter.NewCHSchemaHandler(chSchemaUC, a.logger)
	chTableVerifyHandler := httpadapter.NewCHTableVerifyHandler(chTableVerifyUC, a.logger)
	// §83: предпросмотр ответа приёма — чистый рендер по присланной спеке,
	// внешних зависимостей у него нет.
	ackPreviewHandler := httpadapter.NewAckPreviewHandler(
		usecase.NewAckPreviewUsecase(a.logger), a.logger)

	// Каталог разрешённых хостов (§23). Не зависит от ClickHouse — создаётся
	// всегда. Привязка к узлу пересобирает снимок nodes.url_allowed_hosts и
	// write-through кеш (тот же TTL, что у NodeUsecase).
	hostAllowlistUC := usecase.NewHostAllowlistUsecase(
		pgrepo.NewHostAllowlistRepoPg(a.pg, a.logger),
		nodeRepo, nodeCache, teamRepo, uow, auditUC,
		time.Duration(a.cfg.Redis.NodeTTLSec)*time.Second,
		a.logger,
	)
	hostAllowlistHandler := httpadapter.NewHostAllowlistHandler(hostAllowlistUC, a.logger)

	// Справочник заголовков (§24). usage_count считается on-read из
	// nodes.forward_headers; отдельной таблицы привязки нет.
	headerCatalogUC := usecase.NewHeaderCatalogUsecase(
		pgrepo.NewHeaderCatalogRepoPg(a.pg, a.logger), auditUC, a.logger,
	)
	headerCatalogHandler := httpadapter.NewHeaderCatalogHandler(headerCatalogUC, a.logger)

	// Справочник полей запроса (§41). usage_count считается on-read из
	// nodes.auth_dynamic_field / incoming_auth_dynamic_field.
	requestFieldUC := usecase.NewRequestFieldCatalogUsecase(
		pgrepo.NewRequestFieldCatalogRepoPg(a.pg, a.logger), auditUC, a.logger,
	)
	requestFieldHandler := httpadapter.NewRequestFieldCatalogHandler(requestFieldUC, a.logger)

	// §55: клиент к Sender для dry-run в реальном режиме. Опционален — адрес не
	// задан → реальный вызов недоступен (шаг «Response» вернёт skipped), mock
	// работает как прежде. Ошибку создания не эскалируем: Web не должен падать
	// из-за инструмента отладки; nil-клиент отчёт объяснит.
	var senderClient webport.SenderClient
	if addr := a.cfg.Web.SenderGRPC.Addr; addr != "" {
		sc, err := grpcsender.New(&a.cfg.Web.SenderGRPC, a.logger)
		if err != nil {
			a.logger.Warn("§55 dry-run: sender client init failed, real mode disabled",
				a.logger.Str("addr", addr), a.logger.Err(err))
		} else {
			senderClient = sc
			a.senderClient = sc
			a.logger.Debug("§55 dry-run: sender client ready", a.logger.Str("addr", addr))
		}
	}
	dryRunUC := usecase.NewDryRunUsecase(auditUC, senderClient, a.cfg.Web.SelfIngressHosts, a.logger)
	dryRunHandler := httpadapter.NewDryRunHandler(dryRunUC, nodeUC, a.logger)

	rl := ratelimit.New(a.redis, ratelimit.WithErrorSink(a.metrics))
	// Анти-брутфорс /api/auth/login (Phase AUD.4): лимит попыток на IP и
	// на login через общий Redis-лимитер; fail-open при сбое Redis (§9.4).
	authUC.WithLoginRateLimit(rl, a.cfg.Web.LoginRateLimitPerMin)

	// §88: восстановление пароля. Шаблоны разбираются один раз; ошибка не
	// валит сервис — почта деградирует, остальной интерфейс работает.
	var passwordResetHandler *httpadapter.PasswordResetHandler
	if mailTemplates, tplErr := mail.NewTemplates(); tplErr != nil {
		a.logger.ErrorWithOp("mail templates parse failed; password reset disabled",
			tplErr, "web.mail_templates")
	} else {
		oneTimeTokenRepo := pgrepo.NewOneTimeTokenRepoPg(a.pg, a.logger)
		// §88.7: смена пароля гасит выданные ссылки. Хук ставится на
		// ChangePassword — единственную точку смены, — поэтому покрывает и
		// админскую смену, и self-service, и само восстановление.
		authUC.WithPasswordResetInvalidator(oneTimeTokenRepo)

		passwordResetUC := usecase.NewPasswordResetUsecase(
			userRepo, oneTimeTokenRepo, appSettingsRepo, mailSender, mailTemplates,
			authUC, auditUC, a.cfg.Build.ProjectName, a.identity.ID.String(), a.logger,
			usecase.WithPasswordResetMetrics(a.metrics),
			usecase.WithPasswordResetMailMetrics(a.metrics),
		)
		passwordResetHandler = httpadapter.NewPasswordResetHandler(
			passwordResetUC, rl, a.cfg.Web.PasswordResetRateLimitPerMin, a.logger)
	}

	// §27.8: проверка подключения к RabbitMQ (диагностический AMQP-handshake,
	// rate-limit на пользователя через общий Redis-лимитер).
	rmqTestHandler := httpadapter.NewRMQTestHandler(
		usecase.NewRMQTester(rabbitmqadapter.NewProber(), a.logger),
		rl, a.cfg.Web.RMQTestRateLimitPerMin, a.logger)

	// §73: реестр соседних инстансов Nexus. Проба ходит на публичные
	// /api/version и /ready соседа, поэтому токен не нужен и зависимостей,
	// кроме PostgreSQL, у раздела нет — регистрируется всегда.
	peerInstanceHandler := httpadapter.NewPeerInstanceHandler(
		usecase.NewPeerInstanceUsecase(
			pgrepo.NewPeerInstanceRepoPg(a.pg, a.logger),
			instanceprobe.New(time.Duration(a.cfg.Web.InstanceProbeTimeoutMs)*time.Millisecond, a.logger),
			auditUC, a.logger),
		rl, a.cfg.Web.InstanceProbeRateLimitPerMin, a.logger)

	// Prometheus query-клиент для метрик панели (§21). Опционален: при пустом
	// prometheus.url остаётся nil — MetricsUsecase деградирует
	// (prometheus_available=false), не падает.
	var promMetrics webport.PromMetrics
	if a.cfg.Prometheus.URL != "" {
		pc, err := prometheusreader.New(
			a.cfg.Prometheus.URL,
			time.Duration(a.cfg.Prometheus.TimeoutMs)*time.Millisecond,
			a.logger,
		)
		if err != nil {
			a.logger.Warn("prometheus client init failed; panel metrics degraded", a.logger.Err(err))
		} else {
			promMetrics = pc
		}
	}

	// Replay + live-tail зависят от ClickHouse-чтения и HTTP-диспетчера
	// к Receiver. Подключаем только если CH-клиент инициализирован.
	// ConnProvider — clickhouse.Manager, чтобы при hot-reload (Phase 6.3.2.5)
	// LogReaderCH автоматически переключился на новый conn.
	var (
		replayHandler  *httpadapter.ReplayHandler
		logsHandler    *httpadapter.LogsHandler
		orphanHandler  *httpadapter.OrphanHandler
		teamHandler    *httpadapter.TeamHandler
		nodeLogMetrics webport.NodeLogMetrics   // §21: per-node KPI/график из CH (nil без CH)
		failedPurger   webport.FailedLogsPurger // §35/§36: очистка неудачных доставок (nil без CH)
	)
	// §34.4/§36.11: cancel-set tombstones (Redis) — общий для очистки очереди и
	// отмены оригиналов при «Повторить все сейчас». nil без Redis.
	var queueCancel webport.QueueCancelWriter
	if a.redis != nil {
		queueCancel = queuecancel.New(a.redis)
	}
	dlqRetention := time.Duration(a.cfg.Kafka.Topic.RetentionMs) * time.Millisecond
	if a.ch != nil {
		// a.chMgr уже создан выше (вместе с teamProvisioner).
		logReader := chreader.NewLogReader(a.chMgr, a.logger)
		// На ОБЩЕЙ таблице логов записи без node_id не должны засчитываться
		// каждому её узлу (иначе после переноса узел видит чужое, в т.ч. из
		// другой команды). Карта «таблица → число узлов» кешируется внутри.
		logReader.SetTableUsage(nodeRepo)
		// §70.4: запрет удаления записей в чужой таблице + строгая атрибуция на
		// ней (карта «таблица → число узлов» считается по своей PostgreSQL и для
		// межнодовой таблицы врёт).
		logReader.SetOwnership(chGuard)
		nodeLogMetrics = logReader // точные per-node метрики узла из CH-логов
		failedPurger = logReader   // очистка «Неудачных доставок» из CH-логов
		dispatcher := rcvdispatcher.NewHTTPDispatcher(a.cfg.Web.ReceiverURL, replayDispatchTimeout, a.logger)
		replayUC := usecase.NewReplayUsecaseWithCancel(
			logReader, nodeRepo, dispatcher, rl, auditUC,
			a.cfg.Web.ReplayRateLimitPerUserPerMin, queueCancel, teamRepo, dlqRetention, a.logger,
			// §79.2: после успешного массового повтора убираем строки done=0
			// оригиналов — иначе записи остаются в «Неудачных доставках» до
			// ручной очистки, хотя сообщения уже доставлены.
			usecase.WithFailedCleaner(logReader),
			// §85.9: у массового повтора за период свой счёт — цикл батчей
			// крутит клиент, и общий лимит одиночного replay остановил бы его.
			usecase.WithPeriodRateLimit(a.cfg.Web.ReplayPeriodRateLimitPerUserPerMin),
		)
		logsUC := usecase.NewLogsUsecase(logReader, nodeRepo, a.logger)
		replayHandler = httpadapter.NewReplayHandler(replayUC, a.logger)
		logsHandler = httpadapter.NewLogsHandler(logsUC, a.logger)

		// Orphan-сканер (Phase 6.7 + 10.D.2 multi-tenancy): таблицы в CH
		// без узла в Postgres. Сканирует и дропает только в БД allow-list'а
		// (teams.ch_database). chCfg остаётся для метаданных.
		orphanScanner := usecase.NewOrphanScanner(a.chMgr, nodeRepo, teamRepo, &a.cfg.ClickHouse, auditUC, a.logger)
		// §70.4: таблицы соседней ноды не показываются «бесхозными» и не дропаются.
		orphanScanner.SetOwnership(chGuard)
		orphanHandler = httpadapter.NewOrphanHandler(orphanScanner, a.logger)

		// Team provisioning (Phase 10.C): teamProvisioner создан выше.
		teamUC := usecase.NewTeamUsecase(teamRepo, teamProvisioner, auditUC, a.identity.ID, a.logger)
		teamHandler = httpadapter.NewTeamHandler(teamUC, a.logger)

		// ClickHouse hot-reload: Web не держит chlog.Writer, поэтому writers пуст.
		// Manager.Reload swap'нет conn — LogReaderCH сразу пойдёт через новый.
		reloadSub.Register(reloader.SectionClickHouse,
			bootstrap.ClickHouseReloader(a.pg, a.cfg, a.chMgr, nil, a.cipher, a.logger))
	} else {
		// CH-клиент недоступен — но overlay в cfg всё равно полезно обновлять,
		// чтобы при следующем рестарте подхватились свежие значения.
		reloadSub.Register(reloader.SectionClickHouse,
			bootstrap.ClickHouseOverlayReloader(a.pg, a.cfg, a.cipher, a.logger))
	}

	// Планировщик Telegram-уведомлений (§22): ошибки берутся из Prometheus
	// (метрика nexus_request_incomplete_total) — единый источник с графиками.
	// Требует Prometheus; без него уведомления не запускаются.
	if promMetrics != nil {
		notifScheduler := usecase.NewNotificationScheduler(
			appSettingsUC, teamRepo, nodeRepo, promMetrics, telegramClient,
			rediscache.NewNotifLock(a.redis), rediscache.NewNotifCheckpoint(a.redis),
			a.identity.ID.String(), a.logger,
		)
		reloadSub.Register(reloader.SectionNotifications, func(ctx context.Context) error {
			notifScheduler.Reschedule(ctx)
			return nil
		})
		a.notifDone = safego.Go(a.logger, "web.notificationScheduler", func() {
			notifScheduler.Run(ctx)
		})
	} else {
		a.logger.Warn("telegram notifications disabled: prometheus not configured (§22)")
	}
	a.reloadDone = safego.Go(a.logger, "web.reloadSubscriber", func() {
		reloadSub.Run(ctx)
	})

	// Метрики панели (§21): Prometheus (глобальные KPI/очередь/throughput) +
	// ClickHouse (ТОЧНЫЕ per-node KPI/график узла). Оба источника опциональны —
	// usecase деградирует (prometheus_available/chart_available=false), поэтому
	// handler создаётся всегда.
	// §46: персистентный статус «Down» из Redis (приоритетнее Prometheus-гауджа,
	// переживает рестарт). nil без Redis → applyLastErrors деградирует на Prometheus.
	// ВАЖНО: тип переменной — интерфейс, иначе typed-nil попал бы в non-nil интерфейс.
	var nodeStatusReader webport.NodeStatusReader
	if a.redis != nil {
		nodeStatusReader = rediscache.NewNodeStatusReaderRedis(a.redis, a.logger)
	}
	metricsUC := usecase.NewMetricsUsecase(promMetrics, nodeLogMetrics, nodeRepo, appSettingsRepo, nodeStatusReader, a.logger,
		// §79.5.1: порог точной формы графика — рычаг оператора на больших таблицах.
		usecase.WithExactChartMaxRecords(a.cfg.Web.MetricsExactChartMaxRecords),
		// §86.4: сквозной скоуп «Все команды» + кеш агрегата шапки. Без членств
		// режим просто недоступен, поведение одной команды не меняется.
		usecase.WithMetricsTeams(teamRepo),
		usecase.WithTotalsCacheTTL(usecase.DefaultTotalsCacheTTL))
	metricsHandler := httpadapter.NewMetricsHandler(metricsUC, a.logger)

	// Мониторинг Kafka (§4 spec): Prometheus (throughput/lag/KPI/top-узлы) +
	// Kafka Admin (топики/брокеры/ping, только при заданных брокерах) + Redis-кеш
	// метаданных (TTL 30с). Все источники опциональны — usecase деградирует.
	var kafkaAdmin webport.KafkaAdmin
	// §34.4: тот же admin-клиент реализует AsyncQueuePeeker (peek очереди).
	var asyncPeeker webport.AsyncQueuePeeker
	if a.cfg.Kafka.Brokers != "" {
		kac := kafkaadmin.New(a.cfg.Kafka.Brokers, 5*time.Second, 30, a.logger)
		kafkaAdmin = kac
		asyncPeeker = kac
	}
	kafkaUC := usecase.NewKafkaMonitorUsecase(
		promMetrics, kafkaAdmin, rediscache.NewKafkaCacheRedis(a.redis, 30*time.Second),
		kafkaThresholds(&a.cfg.Web.KafkaAlerts), a.logger,
	)
	kafkaHandler := httpadapter.NewKafkaHandler(kafkaUC, a.logger)

	// §34.4: управление async-очередью узла (peek + cancel-set tombstones).
	// queueCancel создан выше (общий с replay «Повторить все»).
	// §3.6: очередь узла расщеплена на два топика — основной и delay-топик
	// отложенных сообщений paused-узлов. Вкладка «Очередь» показывает оба,
	// иначе бэклог паузы исчезает из UI, а «Очистить все ожидающие» его не находит.
	asyncQueueUC := usecase.NewAsyncQueueUsecase(
		asyncPeeker, queueCancel, failedPurger, nodeRepo, auditUC,
		a.cfg.Kafka.ConsumerGroup, a.cfg.Kafka.AsyncTopic,
		a.cfg.Kafka.PausedGroup(), a.cfg.Kafka.PausedTopic,
		dlqRetention, 0, a.logger,
	)
	asyncQueueHandler := httpadapter.NewAsyncQueueHandler(asyncQueueUC, a.logger)

	// §81.4: состояние и ручной сброс circuit breaker'а узла. Оба порта живут в
	// Redis, поэтому без него usecase отвечает «недоступно», а карточка в UI
	// прячется. Типы переменных — интерфейсы: typed-nil в non-nil интерфейсе дал
	// бы панику вместо честной деградации (та же грабля, что в §64.4).
	var (
		nodeBreaker    webport.NodeBreaker
		statusResetter webport.NodeStatusResetter
	)
	if a.redis != nil {
		nodeBreaker = rediscache.NewNodeBreakerRedis(a.redis, a.logger)
		statusResetter = rediscache.NewNodeStatusReaderRedis(a.redis, a.logger)
	}
	breakerHandler := httpadapter.NewBreakerHandler(
		usecase.NewNodeBreakerUsecase(nodeBreaker, statusResetter, nodeRepo, auditUC, a.logger), a.logger)

	// §84.7: исход последнего вызова узла для шапки страницы. Резолвер общий с
	// рабочим столом (usecase.LastOutcomeResolver) — правило приоритета
	// Redis→Prometheus существует в одном экземпляре, иначе две копии разойдутся.
	// Оба источника опциональны: при отсутствии обоих эндпоинт честно отвечает
	// available=false, и бейдж не рисуется.
	nodeRuntimeHandler := httpadapter.NewNodeRuntimeHandler(
		usecase.NewNodeRuntimeUsecase(
			nodeRepo,
			usecase.NewLastOutcomeResolver(nodeStatusReader, promMetrics, a.logger),
			clock.System(),
			a.logger,
		), a.logger)

	mw := httpadapter.Middlewares{
		APITokenAuth:    httpadapter.APITokenAuthMiddleware(tokenUC, rl, a.cfg.Web.APITokenRateLimitPerMin, a.logger),
		SessionAuth:     httpadapter.AuthMiddleware(authUC, &a.cfg.Web),
		RequireAdmin:    httpadapter.RequireMinRole(domain.UserRoleAdmin),
		RequireManager:  httpadapter.RequireMinRole(domain.UserRoleManager),
		RequireOperator: httpadapter.RequireMinRole(domain.UserRoleOperator),
		KafkaRateLimit:  httpadapter.KafkaRateLimitMiddleware(rl, a.cfg.Web.KafkaMonitorRateLimitPerMin),
		CSRFCheck:       httpadapter.CSRFOriginCheck(a.logger),
	}
	httpadapter.RegisterAPI(r, httpadapter.Handlers{
		Auth:          authHandler,
		PasswordReset: passwordResetHandler, // §88
		Node:          nodeHandler,
		User:          userHandler,
		Token:         tokenHandler,
		Team:          teamHandler,
		Audit:         auditHandler,
		DryRun:        dryRunHandler,
		Replay:        replayHandler,
		Logs:          logsHandler,
		Metrics:       metricsHandler,
		AppSettings:   appSettingsHandler,
		Orphan:        orphanHandler,
		CHTemplate:    chTemplateHandler,
		CHSchema:      chSchemaHandler,
		CHTableVerify: chTableVerifyHandler,
		AckPreview:    ackPreviewHandler,
		HostAllowlist: hostAllowlistHandler,
		HeaderCatalog: headerCatalogHandler,
		RequestField:  requestFieldHandler,
		RMQTest:       rmqTestHandler,
		Instances:     peerInstanceHandler,
		Breaker:       breakerHandler,
		NodeRuntime:   nodeRuntimeHandler,
		Kafka:         kafkaHandler,
		AsyncQueue:    asyncQueueHandler,
		ServiceLogs:   serviceLogsHandler,
		Prefs:         prefHandler,
	}, mw)

	// Реверс-прокси боевых эндпоинтов Receiver (§17.1, единый вход): Web
	// проксирует /api/v1/request|requestAsync|callback на Receiver. Иначе эти
	// пути провалились бы в SPA-fallback и вернули index.html вместо ответа узла.
	httpadapter.RegisterReceiverProxy(r, a.cfg.Web.ReceiverURL, a.logger)

	// SPA fallback: всё, что не API/инфра — отдаём index.html (§17.1 ТЗ).
	// Регистрируется ПОСЛЕ всех API-роутов, чтобы NoRoute переопределялся
	// именно SPA-fallback'ом.
	httpadapter.SPAFallback(r, static.FS())

	// Housekeeping cron: ежедневное удаление старых audit-записей (§7.13).
	hk := usecase.NewHousekeeping(auditUC, a.cfg.Web.AuditRetentionDays, a.logger)
	a.housekeepingDone = safego.Go(a.logger, "web.housekeeping", func() {
		hk.Run(ctx)
	})

	// ReadTimeout/WriteTimeout здесь НЕ задаются намеренно, и это не упущение:
	// через Web идут SSE-стримы (live-tail логов, §7.4) и проксирование
	// sync-запросов к Receiver, где таймаут узла доходит до 600 с (§таймауты).
	// Общий WriteTimeout рвал бы и то, и другое — ровно так боевой
	// receiver.write_timeout_ms=10000 обрывал долгие вызовы. Дедлайны живут
	// per-request: в контексте запроса и в таймауте узла.
	// IdleTimeout ограничивает только ПРОСТАИВАЮЩИЕ keep-alive соединения —
	// без него брошенный клиентом сокет висел бы до перезапуска процесса.
	a.srv = &http.Server{
		Addr:              a.cfg.Web.HTTPAddr,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       time.Duration(a.cfg.Web.IdleTimeoutSec) * time.Second,
	}

	a.logger.Info("web listening",
		a.logger.Str("addr", a.cfg.Web.HTTPAddr))

	errCh := make(chan error, 1)
	go func() {
		defer safego.Recover(a.logger, "web.listenAndServe")
		if err := a.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("web listen: %w", err)
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err
	}
}

func (a *App) Stop(ctx context.Context) error {
	if a.srv == nil {
		return nil
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	a.logger.Info("web shutting down")
	if err := a.srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("web shutdown: %w", err)
	}
	// Дожидаемся фоновых горутин до закрытия соединений (runner уже отменил
	// их ctx): callbacks reload'а трогают pg/CH, housekeeping — pg.
	awaitCtx, awaitCancel := context.WithTimeout(ctx, 10*time.Second)
	safego.Await(awaitCtx, a.notifDone, a.logger, "web.notificationScheduler")
	safego.Await(awaitCtx, a.reloadDone, a.logger, "web.reloadSubscriber")
	safego.Await(awaitCtx, a.housekeepingDone, a.logger, "web.housekeeping")
	safego.Await(awaitCtx, a.shipperDone, a.logger, "web.logShipper")
	awaitCancel()
	if a.chMgr != nil {
		closeCtx, cancelClose := context.WithTimeout(ctx, 5*time.Second)
		_ = a.chMgr.Close(closeCtx)
		cancelClose()
	}
	// §55: пул gRPC-соединений к Sender (dry-run). nil, если реальный режим не
	// сконфигурирован.
	if a.senderClient != nil {
		if err := a.senderClient.Close(); err != nil {
			a.logger.Warn("web: sender grpc client close failed", a.logger.Err(err))
		}
	}
	if a.otelShutdown != nil {
		otelCtx, cancelOtel := context.WithTimeout(ctx, 5*time.Second)
		defer cancelOtel()
		if err := a.otelShutdown(otelCtx); err != nil {
			a.logger.Warn("otel tracer shutdown returned error", a.logger.Err(err))
		}
	}
	return nil
}

// resolveSelfIngressHosts формирует список своих authority для self-reference
// валидации target_url (§32.2). Явный конфиг (self_ingress_hosts) приоритетен;
// если он пуст — выводим authority из receiverURL (host[:port]). Пустой
// результат означает «проверка отключена».
func resolveSelfIngressHosts(configured []string, receiverURL string) []string {
	if len(configured) > 0 {
		return configured
	}
	receiverURL = strings.TrimSpace(receiverURL)
	if receiverURL == "" {
		return nil
	}
	parsed, err := url.Parse(receiverURL)
	if err != nil || parsed.Host == "" {
		return nil
	}
	return []string{parsed.Host}
}

// kafkaThresholds маппит config-секцию порогов в usecase-тип (usecase не
// зависит от config). Значения уже с дефолтами (applyKafkaAlertsDefaults).
func kafkaThresholds(c *config.KafkaAlertsSection) usecase.KafkaThresholds {
	return usecase.KafkaThresholds{
		LagWarning:              c.LagWarning,
		LagCritical:             c.LagCritical,
		LagGrowthCriticalPerSec: c.LagGrowthCriticalPerSec,
		ErrorRateWarning:        c.ErrorRateWarning,
		ErrorRateCritical:       c.ErrorRateCritical,
		ProduceP95WarningMs:     c.ProduceLatencyP95WarningMs,
		ProduceP95CriticalMs:    c.ProduceLatencyP95CriticalMs,
	}
}
