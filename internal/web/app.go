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

	"bus/internal/platform/bootstrap"
	"bus/internal/platform/config"
	"bus/internal/platform/crypto"
	"bus/internal/platform/healthcheck"
	"bus/internal/platform/logging"
	"bus/internal/platform/i18n"
	"bus/internal/platform/metrics"
	pgpf "bus/internal/platform/pg"
	"bus/internal/platform/ratelimit"
	redispf "bus/internal/platform/redis"
	"bus/internal/platform/reloader"
	sentrypf "bus/internal/platform/sentry"
	httpadapter "bus/internal/web/adapter/in/http"
	chreader "bus/internal/web/adapter/out/clickhouse"
	pgrepo "bus/internal/web/adapter/out/postgres"
	rcvdispatcher "bus/internal/web/adapter/out/receiver"
	rediscache "bus/internal/web/adapter/out/redis"
	"bus/internal/web/static"
	"bus/internal/web/usecase"
)

type App struct {
	cfg     *config.Config
	logger  logging.Logger
	pg      *pgxpool.Pool
	redis   *goredis.Client
	ch      chdriver.Conn
	cipher  *crypto.Cipher
	metrics *metrics.Metrics

	srv *http.Server
}

func New(cfg *config.Config, pg *pgxpool.Pool, redis *goredis.Client, ch chdriver.Conn, cipher *crypto.Cipher, logger logging.Logger) *App {
	return &App{
		cfg:     cfg,
		logger:  logger,
		pg:      pg,
		redis:   redis,
		ch:      ch,
		cipher:  cipher,
		metrics: metrics.New("web"),
	}
}

func (a *App) Start(ctx context.Context) error {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(sentrypf.GinMiddleware("web"), metrics.GinMiddleware(a.metrics), i18n.GinMiddleware(), gin.Recovery())

	hc := healthcheck.New(
		[]healthcheck.Checker{
			pgpf.HealthChecker("postgres", a.pg),
			redispf.HealthChecker("redis", a.redis),
		},
		nil,
	)
	hc.Register(r)
	r.GET("/metrics", gin.WrapH(a.metrics.Handler()))

	// Сборка слоёв (Clean Architecture, §17.2).
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
		time.Duration(a.cfg.Redis.NodeTTLSec)*time.Second,
		a.cfg.Web.NodesHardLimit,
		a.logger,
	)
	nodeHandler := httpadapter.NewNodeHandler(nodeUC, a.logger)

	userRepo := pgrepo.NewUserRepoPg(a.pg, a.logger)
	sessionRepo := rediscache.NewSessionRepoRedis(a.redis)
	sessionTTL := time.Duration(a.cfg.Redis.SessionTTLSec) * time.Second
	authUC := usecase.NewAuthUsecase(userRepo, sessionRepo, auditUC, sessionTTL, a.logger)
	userUC := usecase.NewUserUsecase(userRepo, sessionRepo, auditUC, a.logger)

	tokenRepo := pgrepo.NewAPITokenRepoPg(a.pg, a.logger)
	tokenUC := usecase.NewAPITokenUsecase(tokenRepo, userRepo, auditUC, a.logger)

	appSettingsRepo := pgrepo.NewAppSettingsRepoPg(a.pg, a.logger)
	reloadPublisher := reloader.NewPublisher(a.redis)
	appSettingsUC := usecase.NewAppSettingsUsecase(appSettingsRepo, auditUC, reloadPublisher, a.logger)

	// Подписчик hot-reload (§14.5). Web сам слушает события, чтобы admin-инстансы
	// в кластере применили изменения, отправленные через другой инстанс.
	reloadSub := reloader.NewSubscriber(a.redis, a.logger)
	reloadSub.Register(reloader.SectionSentry,
		bootstrap.SentryReloader(a.pg, a.cfg, a.cfg.Build.ProjectName, a.cfg.Build.Version, a.logger))
	reloadSub.Register(reloader.SectionClickHouse,
		bootstrap.ClickHouseOverlayReloader(a.pg, a.cfg, a.logger))
	go reloadSub.Run(ctx)

	authHandler := httpadapter.NewAuthHandler(authUC, &a.cfg.Web, sessionTTL, a.logger)
	userHandler := httpadapter.NewUserHandler(userUC, authUC, a.logger)
	tokenHandler := httpadapter.NewAPITokenHandler(tokenUC, a.logger)
	auditHandler := httpadapter.NewAuditHandler(auditUC, a.logger)
	appSettingsHandler := httpadapter.NewAppSettingsHandler(appSettingsUC, a.logger)

	dryRunUC := usecase.NewDryRunUsecase(auditUC, a.logger)
	dryRunHandler := httpadapter.NewDryRunHandler(dryRunUC, a.logger)

	rl := ratelimit.New(a.redis)

	// Replay + live-tail зависят от ClickHouse-чтения и HTTP-диспетчера
	// к Receiver. Подключаем только если CH-клиент инициализирован.
	var (
		replayHandler *httpadapter.ReplayHandler
		logsHandler   *httpadapter.LogsHandler
	)
	if a.ch != nil {
		logReader := chreader.NewLogReader(a.ch, a.logger)
		dispatcher := rcvdispatcher.NewHTTPDispatcher(a.cfg.Web.ReceiverURL, 30*time.Second, a.logger)
		replayUC := usecase.NewReplayUsecase(
			logReader, nodeRepo, dispatcher, rl, auditUC,
			a.cfg.Web.ReplayRateLimitPerUserPerMin, a.logger,
		)
		logsUC := usecase.NewLogsUsecase(logReader, nodeRepo, a.logger)
		replayHandler = httpadapter.NewReplayHandler(replayUC, a.logger)
		logsHandler = httpadapter.NewLogsHandler(logsUC, a.logger)
	}

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
		Audit:       auditHandler,
		DryRun:      dryRunHandler,
		Replay:      replayHandler,
		Logs:        logsHandler,
		AppSettings: appSettingsHandler,
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
	return nil
}
