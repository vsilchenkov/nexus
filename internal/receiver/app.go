// Package receiver — Receiver Service (§3 ТЗ).
//
// Phase 1: handlers /v1/request/* (sync) + healthcheck.
// /v1/requestAsync/* — заглушка 501, реальная реализация в Phase 2.
package receiver

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
	kafkapf "bus/internal/platform/kafka"
	"bus/internal/platform/logging"
	pgpf "bus/internal/platform/pg"
	"bus/internal/platform/ratelimit"
	redispf "bus/internal/platform/redis"
	sentrypf "bus/internal/platform/sentry"
	httpadapter "bus/internal/receiver/adapter/in/http"
	"bus/internal/receiver/adapter/out/grpcsender"
	"bus/internal/receiver/adapter/out/nodecache"
	"bus/internal/receiver/usecase"
)

type App struct {
	cfg    *config.Config
	logger logging.Logger
	pg     *pgxpool.Pool
	redis  *goredis.Client
	cipher *crypto.Cipher

	srv      *http.Server
	senderCl *grpcsender.Client
	producer *kafkapf.Producer
}

func New(cfg *config.Config, pg *pgxpool.Pool, redis *goredis.Client, cipher *crypto.Cipher, logger logging.Logger) *App {
	return &App{cfg: cfg, logger: logger, pg: pg, redis: redis, cipher: cipher}
}

func (a *App) Start(ctx context.Context) error {
	senderCl, err := grpcsender.New(&a.cfg.Receiver.SenderGRPC, a.logger)
	if err != nil {
		return fmt.Errorf("init sender client: %w", err)
	}
	a.senderCl = senderCl

	reader := nodecache.New(a.redis, a.pg, a.cipher, time.Duration(a.cfg.Redis.NodeTTLSec)*time.Second, a.logger)
	routeUC := usecase.NewRouteUsecase(reader, a.senderCl, a.logger)

	a.producer = kafkapf.NewProducer(a.cfg)
	routeAsyncUC := usecase.NewRouteAsyncUsecase(reader, a.producer, a.cfg.Kafka.AsyncTopic, a.logger)

	handler := httpadapter.New(routeUC, routeAsyncUC, a.cfg.Receiver.MaxBodyBytes, a.logger)

	rl := ratelimit.New(a.redis)
	rlMw := httpadapter.RateLimitMiddleware(rl, a.cfg.Receiver.RateLimitPerNode, a.logger)

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(sentrypf.GinMiddleware("receiver"), gin.Recovery())

	hc := healthcheck.New(
		[]healthcheck.Checker{pgpf.HealthChecker("postgres", a.pg)},
		[]healthcheck.Checker{redispf.HealthChecker("redis", a.redis)},
	)
	hc.Register(r)
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))

	handler.Register(r, rlMw)

	a.srv = &http.Server{
		Addr:              a.cfg.Receiver.HTTPAddr,
		Handler:           r,
		ReadTimeout:       time.Duration(a.cfg.Receiver.ReadTimeoutMs) * time.Millisecond,
		WriteTimeout:      time.Duration(a.cfg.Receiver.WriteTimeoutMs) * time.Millisecond,
		IdleTimeout:       time.Duration(a.cfg.Receiver.IdleTimeoutSec) * time.Second,
		MaxHeaderBytes:    a.cfg.Receiver.MaxHeaderBytes,
		ReadHeaderTimeout: 5 * time.Second,
	}

	a.logger.Info("receiver listening",
		a.logger.Str("addr", a.cfg.Receiver.HTTPAddr))

	errCh := make(chan error, 1)
	go func() {
		if err := a.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("receiver listen: %w", err)
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
	shutdownCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	a.logger.Info("receiver shutting down")

	if a.srv != nil {
		if err := a.srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("receiver shutdown: %w", err)
		}
	}
	if a.senderCl != nil {
		_ = a.senderCl.Close()
	}
	if a.producer != nil {
		_ = a.producer.Close()
	}
	return nil
}
