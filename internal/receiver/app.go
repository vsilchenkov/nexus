// Package receiver — Receiver Service (§3 ТЗ).
//
// В Phase 0 — только healthcheck (/health, /ready) и /metrics.
// HTTP-эндпоинты /v1/request/* и /v1/requestAsync/* — Phase 1.
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
	"bus/internal/platform/healthcheck"
	"bus/internal/platform/logging"
	pgpf "bus/internal/platform/pg"
	redispf "bus/internal/platform/redis"
)

// App — заглушка Receiver на Phase 0.
type App struct {
	cfg    *config.Config
	logger logging.Logger
	pg     *pgxpool.Pool
	redis  *goredis.Client

	srv *http.Server
}

// New собирает App. Зависимости — pgxpool и redis-client — уже подняты в main.
func New(cfg *config.Config, pg *pgxpool.Pool, redis *goredis.Client, logger logging.Logger) *App {
	return &App{cfg: cfg, logger: logger, pg: pg, redis: redis}
}

// Start блокирующе поднимает HTTP-сервер. Возвращается, когда ctx отменён
// или сервер упал.
func (a *App) Start(ctx context.Context) error {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	hc := healthcheck.New(
		[]healthcheck.Checker{pgpf.HealthChecker("postgres", a.pg)},
		[]healthcheck.Checker{redispf.HealthChecker("redis", a.redis)},
	)
	hc.Register(r)

	r.GET("/metrics", gin.WrapH(promhttp.Handler()))

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

// Stop — graceful shutdown HTTP-сервера.
func (a *App) Stop(ctx context.Context) error {
	if a.srv == nil {
		return nil
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	a.logger.Info("receiver shutting down")
	if err := a.srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("receiver shutdown: %w", err)
	}
	return nil
}
