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
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"
	swaggerfiles "github.com/swaggo/files"
	ginswagger "github.com/swaggo/gin-swagger"

	// Регистрирует Web Swagger-doc в swag.Registry при импорте (§11 ТЗ).
	_ "nexus/docs/web"
	"nexus/internal/domain"
	"nexus/internal/platform/bootstrap"
	chpf "nexus/internal/platform/clickhouse"
	"nexus/internal/platform/config"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/healthcheck"
	"nexus/internal/platform/i18n"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/metrics"
	otelpf "nexus/internal/platform/otel"
	pgpf "nexus/internal/platform/pg"
	"nexus/internal/platform/ratelimit"
	redispf "nexus/internal/platform/redis"
	"nexus/internal/platform/reloader"
	sentrypf "nexus/internal/platform/sentry"
	httpadapter "nexus/internal/web/adapter/in/http"
	chreader "nexus/internal/web/adapter/out/clickhouse"
	pgrepo "nexus/internal/web/adapter/out/postgres"
	rcvdispatcher "nexus/internal/web/adapter/out/receiver"
	rediscache "nexus/internal/web/adapter/out/redis"
	"nexus/internal/web/static"
	"nexus/internal/web/usecase"
	webport "nexus/internal/web/usecase/port"
)

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
}

func New(cfg *config.Config, pg *pgxpool.Pool, redis *goredis.Client, ch chdriver.Conn, cipher *crypto.Cipher, otelShutdown otelpf.ShutdownFunc, logger logging.Logger) *App {
	return &App{
		cfg:          cfg,
		logger:       logger,
		pg:           pg,
		redis:        redis,
		ch:           ch,
		cipher:       cipher,
		metrics:      metrics.New("web"),
		otelShutdown: otelShutdown,
	}
}

func (a *App) Start(ctx context.Context) error {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(otelpf.GinMiddleware("web"), sentrypf.GinMiddleware("web"), metrics.GinMiddleware(a.metrics), i18n.GinMiddleware(), gin.Recovery())

	hc := healthcheck.New(
		[]healthcheck.Checker{
			pgpf.HealthChecker("postgres", a.pg),
			redispf.HealthChecker("redis", a.redis),
		},
		nil,
	)
	hc.Register(r)
	r.GET("/metrics", gin.WrapH(a.metrics.Handler()))

	// Swagger UI (§11 ТЗ): /swagger/index.html.
	// Дока генерируется аннотациями над handlers и попадает в docs/web/ через `make swagger`.
	r.GET("/swagger/*any", ginswagger.WrapHandler(swaggerfiles.Handler))

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
	if a.ch != nil {
		a.chMgr = chpf.NewManager(a.ch, chpf.New, &a.cfg.ClickHouse, a.logger)
		teamProvisioner = chreader.NewTeamProvisioner(a.chMgr, a.logger)
	}

	nodeRepo := pgrepo.NewNodeRepoPg(a.pg, a.cipher, a.logger)
	nodeCache := rediscache.NewNodeCacheRedis(a.redis, a.logger)
	auditRepo := pgrepo.NewAuditRepoPg(a.pg, a.logger)
	auditUC := usecase.NewAuditUsecase(auditRepo, a.logger)
	uow := pgrepo.NewUnitOfWorkPg(a.pg, a.cipher, a.logger)
	nodeUC := usecase.NewNodeUsecase(
		nodeRepo,
		nodeCache,
		auditUC,
		uow,
		teamRepo,
		teamProvisioner,
		time.Duration(a.cfg.Redis.NodeTTLSec)*time.Second,
		a.cfg.Web.NodesHardLimit,
		defaultTeamID,
		a.logger,
	)
	nodeHandler := httpadapter.NewNodeHandler(nodeUC, a.logger)

	userRepo := pgrepo.NewUserRepoPg(a.pg, a.logger)
	sessionRepo := rediscache.NewSessionRepoRedis(a.redis)
	sessionTTL := time.Duration(a.cfg.Redis.SessionTTLSec) * time.Second
	authUC := usecase.NewAuthUsecase(userRepo, sessionRepo, teamRepo, auditUC, sessionTTL, a.logger)
	userUC := usecase.NewUserUsecase(userRepo, sessionRepo, teamRepo, auditUC, defaultTeamID, a.logger)

	tokenRepo := pgrepo.NewAPITokenRepoPg(a.pg, a.logger)
	tokenUC := usecase.NewAPITokenUsecase(tokenRepo, userRepo, auditUC, a.logger)

	appSettingsRepo := pgrepo.NewAppSettingsRepoPg(a.pg, a.logger)
	reloadPublisher := reloader.NewPublisher(a.redis)
	appSettingsUC := usecase.NewAppSettingsUsecase(appSettingsRepo, auditUC, reloadPublisher, a.logger)
	// SettingsTester (Phase 6.3.2.6): test connection без сохранения.
	settingsTester := usecase.NewSettingsTester(
		appSettingsRepo, a.cfg, chpf.New, usecase.DefaultSentryClientFactory,
		a.cfg.Build.ProjectName, a.cfg.Build.Version, a.logger,
	)

	// Подписчик hot-reload (§14.5). Web сам слушает события, чтобы admin-инстансы
	// в кластере применили изменения, отправленные через другой инстанс.
	// ClickHouseReloader регистрируется ниже — после создания chMgr (если CH доступен).
	reloadSub := reloader.NewSubscriber(a.redis, a.logger)
	reloadSub.Register(reloader.SectionSentry,
		bootstrap.SentryReloader(a.pg, a.cfg, a.cfg.Build.ProjectName, a.cfg.Build.Version, a.logger))

	authHandler := httpadapter.NewAuthHandler(authUC, &a.cfg.Web, sessionTTL, a.logger)
	userHandler := httpadapter.NewUserHandler(userUC, authUC, a.logger)
	tokenHandler := httpadapter.NewAPITokenHandler(tokenUC, a.logger)
	auditHandler := httpadapter.NewAuditHandler(auditUC, a.logger)
	appSettingsHandler := httpadapter.NewAppSettingsHandler(appSettingsUC, settingsTester, a.logger)

	// Шаблоны CH-таблиц (§19). Repo и usecase создаём всегда (GET работает без
	// ClickHouse); provisioner может быть nil — Verify тогда вернёт 503.
	chTemplateRepo := pgrepo.NewCHTemplateRepoPg(a.pg, a.logger)
	chTemplateUC := usecase.NewCHTemplateUsecase(chTemplateRepo, teamProvisioner, teamRepo, auditUC, a.logger)
	chTemplateHandler := httpadapter.NewCHTemplateHandler(chTemplateUC, a.logger)

	dryRunUC := usecase.NewDryRunUsecase(auditUC, a.logger)
	dryRunHandler := httpadapter.NewDryRunHandler(dryRunUC, a.logger)

	rl := ratelimit.New(a.redis)

	// Replay + live-tail зависят от ClickHouse-чтения и HTTP-диспетчера
	// к Receiver. Подключаем только если CH-клиент инициализирован.
	// ConnProvider — clickhouse.Manager, чтобы при hot-reload (Phase 6.3.2.5)
	// LogReaderCH автоматически переключился на новый conn.
	var (
		replayHandler *httpadapter.ReplayHandler
		logsHandler   *httpadapter.LogsHandler
		orphanHandler *httpadapter.OrphanHandler
		teamHandler   *httpadapter.TeamHandler
	)
	if a.ch != nil {
		// a.chMgr уже создан выше (вместе с teamProvisioner).
		logReader := chreader.NewLogReader(a.chMgr, a.logger)
		dispatcher := rcvdispatcher.NewHTTPDispatcher(a.cfg.Web.ReceiverURL, 30*time.Second, a.logger)
		replayUC := usecase.NewReplayUsecase(
			logReader, nodeRepo, dispatcher, rl, auditUC,
			a.cfg.Web.ReplayRateLimitPerUserPerMin, a.logger,
		)
		logsUC := usecase.NewLogsUsecase(logReader, nodeRepo, a.logger)
		replayHandler = httpadapter.NewReplayHandler(replayUC, a.logger)
		logsHandler = httpadapter.NewLogsHandler(logsUC, a.logger)

		// Orphan-сканер (Phase 6.7 + 10.D.2 multi-tenancy): таблицы в CH
		// без узла в Postgres. Сканирует и дропает только в БД allow-list'а
		// (teams.ch_database). chCfg остаётся для метаданных.
		orphanScanner := usecase.NewOrphanScanner(a.chMgr, nodeRepo, teamRepo, &a.cfg.ClickHouse, auditUC, a.logger)
		orphanHandler = httpadapter.NewOrphanHandler(orphanScanner, a.logger)

		// Team provisioning (Phase 10.C): teamProvisioner создан выше.
		teamUC := usecase.NewTeamUsecase(teamRepo, teamProvisioner, auditUC, a.logger)
		teamHandler = httpadapter.NewTeamHandler(teamUC, a.logger)

		// ClickHouse hot-reload: Web не держит chlog.Writer, поэтому writers пуст.
		// Manager.Reload swap'нет conn — LogReaderCH сразу пойдёт через новый.
		reloadSub.Register(reloader.SectionClickHouse,
			bootstrap.ClickHouseReloader(a.pg, a.cfg, a.chMgr, nil, a.logger))
	} else {
		// CH-клиент недоступен — но overlay в cfg всё равно полезно обновлять,
		// чтобы при следующем рестарте подхватились свежие значения.
		reloadSub.Register(reloader.SectionClickHouse,
			bootstrap.ClickHouseOverlayReloader(a.pg, a.cfg, a.logger))
	}
	go reloadSub.Run(ctx)

	mw := httpadapter.Middlewares{
		APITokenAuth: httpadapter.APITokenAuthMiddleware(tokenUC, rl, a.cfg.Web.APITokenRateLimitPerMin, a.logger),
		SessionAuth:  httpadapter.AuthMiddleware(authUC, &a.cfg.Web),
		RequireAdmin: httpadapter.RequireRole("admin"),
	}
	httpadapter.RegisterAPI(r, httpadapter.Handlers{
		Auth:        authHandler,
		Node:        nodeHandler,
		User:        userHandler,
		Token:       tokenHandler,
		Team:        teamHandler,
		Audit:       auditHandler,
		DryRun:      dryRunHandler,
		Replay:      replayHandler,
		Logs:        logsHandler,
		AppSettings: appSettingsHandler,
		Orphan:      orphanHandler,
		CHTemplate:  chTemplateHandler,
	}, mw)

	// SPA fallback: всё, что не API/инфра — отдаём index.html (§17.1 ТЗ).
	// Регистрируется ПОСЛЕ всех API-роутов, чтобы NoRoute переопределялся
	// именно SPA-fallback'ом.
	httpadapter.SPAFallback(r, static.FS())

	// Housekeeping cron: ежедневное удаление старых audit-записей (§7.13).
	hk := usecase.NewHousekeeping(auditUC, a.cfg.Web.AuditRetentionDays, a.logger)
	go hk.Run(ctx)

	a.srv = &http.Server{
		Addr:              a.cfg.Web.HTTPAddr,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
	}

	a.logger.Info("web listening",
		a.logger.Str("addr", a.cfg.Web.HTTPAddr))

	errCh := make(chan error, 1)
	go func() {
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
	if a.chMgr != nil {
		closeCtx, cancelClose := context.WithTimeout(ctx, 5*time.Second)
		_ = a.chMgr.Close(closeCtx)
		cancelClose()
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
