// Package sender — Sender Service (§4 ТЗ).
//
// В Phase 0 — gRPC-сервер с healthcheck-сервисом (grpc_health_v1)
// + admin HTTP с /health, /ready, /metrics. Реальные методы Send,
// HTTP-клиент и Kafka-consumer — Phase 1/2.
package sender

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	"bus/internal/platform/config"
	"bus/internal/platform/healthcheck"
	"bus/internal/platform/kafka"
	"bus/internal/platform/logging"
	pgpf "bus/internal/platform/pg"
)

// App — заглушка Sender на Phase 0.
type App struct {
	cfg         *config.Config
	logger      logging.Logger
	pg          *pgxpool.Pool
	kafkaDialer *kafka.Dialer

	grpcSrv  *grpc.Server
	adminSrv *http.Server
}

func New(cfg *config.Config, pg *pgxpool.Pool, kafkaDialer *kafka.Dialer, logger logging.Logger) *App {
	return &App{cfg: cfg, logger: logger, pg: pg, kafkaDialer: kafkaDialer}
}

// Start поднимает gRPC-сервер и admin-HTTP параллельно. Блокирует до ctx.Done.
func (a *App) Start(ctx context.Context) error {
	errCh := make(chan error, 2)

	go func() { errCh <- a.startGRPC() }()
	go func() { errCh <- a.startAdminHTTP() }()

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err
	}
}

func (a *App) startGRPC() error {
	lis, err := net.Listen("tcp", a.cfg.Sender.GRPCAddr)
	if err != nil {
		return fmt.Errorf("sender grpc listen %s: %w", a.cfg.Sender.GRPCAddr, err)
	}

	a.grpcSrv = grpc.NewServer(
		grpc.MaxConcurrentStreams(a.cfg.Sender.GRPCMaxConcurrentStreams),
	)
	hsrv := health.NewServer()
	hsrv.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(a.grpcSrv, hsrv)
	reflection.Register(a.grpcSrv)

	a.logger.Info("sender grpc listening",
		a.logger.Str("addr", a.cfg.Sender.GRPCAddr))

	if err := a.grpcSrv.Serve(lis); err != nil {
		return fmt.Errorf("sender grpc serve: %w", err)
	}
	return nil
}

func (a *App) startAdminHTTP() error {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	hc := healthcheck.New(
		[]healthcheck.Checker{pgpf.HealthChecker("postgres", a.pg)},
		[]healthcheck.Checker{kafka.HealthChecker("kafka", a.kafkaDialer)},
	)
	hc.Register(r)
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))

	a.adminSrv = &http.Server{
		Addr:              a.cfg.Sender.AdminHTTPAddr,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
	}

	a.logger.Info("sender admin http listening",
		a.logger.Str("addr", a.cfg.Sender.AdminHTTPAddr))

	if err := a.adminSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("sender admin http: %w", err)
	}
	return nil
}

// Stop — graceful shutdown gRPC + HTTP.
func (a *App) Stop(ctx context.Context) error {
	a.logger.Info("sender shutting down")

	if a.grpcSrv != nil {
		done := make(chan struct{})
		go func() {
			a.grpcSrv.GracefulStop()
			close(done)
		}()
		select {
		case <-done:
		case <-ctx.Done():
			a.grpcSrv.Stop()
		case <-time.After(30 * time.Second):
			a.grpcSrv.Stop()
		}
	}

	if a.adminSrv != nil {
		shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := a.adminSrv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("sender admin shutdown: %w", err)
		}
	}
	return nil
}
