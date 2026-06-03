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
	goredis "github.com/redis/go-redis/v9"

	"nexus/internal/platform/bootstrap"
	"nexus/internal/platform/config"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/healthcheck"
	kafkapf "nexus/internal/platform/kafka"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/metrics"
	otelpf "nexus/internal/platform/otel"
	pgpf "nexus/internal/platform/pg"
	"nexus/internal/platform/ratelimit"
	recoverypf "nexus/internal/platform/recovery"
	redispf "nexus/internal/platform/redis"
	"nexus/internal/platform/reloader"
	"nexus/internal/platform/requestid"
	"nexus/internal/platform/safego"
	sentrypf "nexus/internal/platform/sentry"
	httpadapter "nexus/internal/receiver/adapter/in/http"
	"nexus/internal/receiver/adapter/out/grpcsender"
	"nexus/internal/receiver/adapter/out/nodecache"
	rabbitmqadapter "nexus/internal/receiver/adapter/out/rabbitmq"
	"nexus/internal/receiver/usecase"
)

type App struct {
	cfg     *config.Config
	logger  logging.Logger
	pg      *pgxpool.Pool
	redis   *goredis.Client
	cipher  *crypto.Cipher
	metrics *metrics.Metrics

	srv          *http.Server
	senderCl     *grpcsender.Client
	producer     *kafkapf.Producer
	otelShutdown otelpf.ShutdownFunc

	// §27: Puller-воркеры RabbitMQAsync.
	pullerCancel context.CancelFunc
	pullerDone   chan struct{}
}

func New(cfg *config.Config, pg *pgxpool.Pool, redis *goredis.Client, cipher *crypto.Cipher, otelShutdown otelpf.ShutdownFunc, logger logging.Logger) *App {
	return &App{
		cfg:          cfg,
		logger:       logger,
		pg:           pg,
		redis:        redis,
		cipher:       cipher,
		metrics:      metrics.New("receiver"),
		otelShutdown: otelShutdown,
	}
}

func (a *App) Start(ctx context.Context) error {
	senderCl, err := grpcsender.New(&a.cfg.Receiver.SenderGRPC, a.logger)
	if err != nil {
		return fmt.Errorf("init sender client: %w", err)
	}
	a.senderCl = senderCl

	baseReader := nodecache.New(a.redis, a.pg, a.cipher, time.Duration(a.cfg.Redis.NodeTTLSec)*time.Second, a.logger)
	reader := nodecache.NewL2(baseReader, nodecache.L2Config{
		Enabled:  a.cfg.Receiver.L2Cache.Enabled,
		Size:     a.cfg.Receiver.L2Cache.Size,
		TTL:      time.Duration(a.cfg.Receiver.L2Cache.TTLMs) * time.Millisecond,
		StaleTTL: time.Duration(a.cfg.Receiver.L2Cache.StaleTTLMs) * time.Millisecond,
	}, a.logger, a.metrics)
	routeUC := usecase.NewRouteUsecase(reader, a.senderCl, a.logger)

	a.producer = kafkapf.NewProducer(a.cfg)
	routeAsyncUC := usecase.NewRouteAsyncUsecase(reader, a.producer, a.cfg.Kafka.AsyncTopic, a.logger)

	// §27: Puller-менеджер RabbitMQAsync. Один воркer на узел; reconcile из PG.
	// Запускается, если не выключен явно; при отсутствии узлов RabbitMQAsync —
	// no-op. ClientIP/IP лога формируется как rabbitmq://host:port/vhost.
	if !a.cfg.Receiver.Puller.Disabled {
		pullerMgr := usecase.NewPullerManager(
			rabbitmqadapter.NewNodeLister(a.pg, a.cipher, a.logger),
			rabbitmqadapter.NewConnector(),
			a.producer,
			a.cfg.Kafka.AsyncTopic,
			a.cfg.Kafka.Topic.MaxMessageBytes,
			time.Duration(a.cfg.Receiver.Puller.ReconcileSec)*time.Second,
			a.metrics,
			rabbitmqadapter.NewHealthSink(a.redis),
			a.logger,
		)
		pctx, pcancel := context.WithCancel(context.WithoutCancel(ctx))
		a.pullerCancel = pcancel
		a.pullerDone = make(chan struct{})
		go func() {
			defer close(a.pullerDone)
			defer safego.Recover(a.logger, "receiver.pullerManager")
			pullerMgr.Run(pctx)
		}()
		a.logger.Info("rabbitmq puller manager started")
	}

	handler := httpadapter.New(routeUC, routeAsyncUC, a.cfg.Receiver.MaxBodyBytes, a.logger)

	rl := ratelimit.New(a.redis)
	rlMw := httpadapter.RateLimitMiddleware(rl, a.cfg.Receiver.RateLimitPerNode, a.logger)

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(
		requestid.GinMiddleware(),
		otelpf.GinMiddleware("receiver"),
		sentrypf.GinMiddleware("receiver"),
		recoverypf.GinMiddleware(a.logger),
		metrics.GinMiddleware(a.metrics),
	)

	hc := healthcheck.New(
		[]healthcheck.Checker{pgpf.HealthChecker("postgres", a.pg)},
		[]healthcheck.Checker{redispf.HealthChecker("redis", a.redis)},
	)
	hc.Register(r)
	r.GET("/metrics", gin.WrapH(a.metrics.Handler()))

	handler.Register(r, rlMw)

	// Hot-reload Sentry: подписываемся на pub/sub-канал, чтобы менять DSN/level
	// без рестарта при изменении app_settings через UI Web (§14.5 ТЗ).
	reloadSub := reloader.NewSubscriber(a.redis, a.logger)
	reloadSub.Register(reloader.SectionSentry,
		bootstrap.SentryReloader(a.pg, a.cfg, a.cfg.Build.ProjectName, a.cfg.Build.Version, a.logger))
	go func() {
		defer safego.Recover(a.logger, "receiver.reloadSubscriber")
		reloadSub.Run(ctx)
	}()

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
		defer safego.Recover(a.logger, "receiver.listenAndServe")
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

	// §27: останавливаем Puller-воркеры первыми (graceful: дождаться текущий
	// батч, nack необработанных, закрыть каналы — это делает PullerManager).
	if a.pullerCancel != nil {
		a.pullerCancel()
		select {
		case <-a.pullerDone:
		case <-shutdownCtx.Done():
			a.logger.Warn("rabbitmq puller did not stop within shutdown deadline")
		}
	}

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
	if a.otelShutdown != nil {
		if err := a.otelShutdown(shutdownCtx); err != nil {
			a.logger.Warn("otel tracer shutdown returned error", a.logger.Err(err))
		}
	}
	return nil
}
