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

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	goredis "github.com/redis/go-redis/v9"

	"bus/internal/platform/config"
	"bus/internal/platform/crypto"
	"bus/internal/platform/healthcheck"
	"bus/internal/platform/logging"
	pgpf "bus/internal/platform/pg"
	"bus/internal/platform/ratelimit"
	redispf "bus/internal/platform/redis"
	httpadapter "bus/internal/web/adapter/in/http"
	pgrepo "bus/internal/web/adapter/out/postgres"
	rediscache "bus/internal/web/adapter/out/redis"
	"bus/internal/web/usecase"
)

type App struct {
	cfg    *config.Config
	logger logging.Logger
	pg     *pgxpool.Pool
	redis  *goredis.Client
	cipher *crypto.Cipher

	srv *http.Server
}

func New(cfg *config.Config, pg *pgxpool.Pool, redis *goredis.Client, cipher *crypto.Cipher, logger logging.Logger) *App {
	return &App{cfg: cfg, logger: logger, pg: pg, redis: redis, cipher: cipher}
}

func (a *App) Start(ctx context.Context) error {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	hc := healthcheck.New(
		[]healthcheck.Checker{
			pgpf.HealthChecker("postgres", a.pg),
			redispf.HealthChecker("redis", a.redis),
		},
		nil,
	)
	hc.Register(r)
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))

	// Сборка слоёв (Clean Architecture, §17.2).
	nodeRepo := pgrepo.NewNodeRepoPg(a.pg, a.cipher, a.logger)
	nodeCache := rediscache.NewNodeCacheRedis(a.redis, a.logger)
	auditRepo := pgrepo.NewAuditRepoPg(a.pg, a.logger)
	auditUC := usecase.NewAuditUsecase(auditRepo, a.logger)
	nodeUC := usecase.NewNodeUsecase(
		nodeRepo,
		nodeCache,
		auditUC,
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

	authHandler := httpadapter.NewAuthHandler(authUC, &a.cfg.Web, sessionTTL, a.logger)
	userHandler := httpadapter.NewUserHandler(userUC, authUC, a.logger)
	tokenHandler := httpadapter.NewAPITokenHandler(tokenUC, a.logger)
	auditHandler := httpadapter.NewAuditHandler(auditUC, a.logger)

	rl := ratelimit.New(a.redis)
	mw := httpadapter.Middlewares{
		APITokenAuth: httpadapter.APITokenAuthMiddleware(tokenUC, rl, a.cfg.Web.APITokenRateLimitPerMin, a.logger),
		SessionAuth:  httpadapter.AuthMiddleware(authUC, &a.cfg.Web),
		RequireAdmin: httpadapter.RequireRole("admin"),
	}
	httpadapter.RegisterAPI(r, httpadapter.Handlers{
		Auth:  authHandler,
		Node:  nodeHandler,
		User:  userHandler,
		Token: tokenHandler,
		Audit: auditHandler,
	}, mw)

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
