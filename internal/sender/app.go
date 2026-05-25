// Package sender — Sender Service (§4 ТЗ).
//
// Phase 2: gRPC (sync) + Kafka consumer (async) + DLQ + paused-pacing.
package sender

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	chdrv "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	"bus/internal/platform/circuitbreaker"
	chpf "bus/internal/platform/clickhouse"
	"bus/internal/platform/config"
	"bus/internal/platform/crypto"
	"bus/internal/platform/healthcheck"
	kafkapf "bus/internal/platform/kafka"
	"bus/internal/platform/logging"
	"bus/internal/platform/metrics"
	pgpf "bus/internal/platform/pg"
	sentrypf "bus/internal/platform/sentry"
	kafkaadapter "bus/internal/sender/adapter/in/kafka"
	grpcadapter "bus/internal/sender/adapter/in/grpc"
	"bus/internal/sender/adapter/out/chlog"
	"bus/internal/sender/adapter/out/httpclient"
	"bus/internal/sender/adapter/out/nodepg"
	"bus/internal/sender/usecase"
	senderv1 "bus/proto/sender/v1"
)

type App struct {
	cfg     *config.Config
	logger  logging.Logger
	pg      *pgxpool.Pool
	ch      chdrv.Conn
	redis   *goredis.Client
	cipher  *crypto.Cipher
	metrics *metrics.Metrics

	grpcSrv  *grpc.Server
	adminSrv *http.Server
	chWriter *chlog.Writer
	producer *kafkapf.Producer
	consumer *kafkaadapter.ConsumerGroup
}

func New(
	cfg *config.Config,
	pg *pgxpool.Pool,
	ch chdrv.Conn,
	redis *goredis.Client,
	cipher *crypto.Cipher,
	logger logging.Logger,
) *App {
	return &App{
		cfg:     cfg,
		logger:  logger,
		pg:      pg,
		ch:      ch,
		redis:   redis,
		cipher:  cipher,
		metrics: metrics.New("sender"),
	}
}

func (a *App) Start(ctx context.Context) error {
	// Общие сервисы.
	a.chWriter = chlog.NewWithFallback(a.ch, &a.cfg.ClickHouse, a.cfg.ClickHouse.FallbackDir, a.metrics, a.logger)
	httpc := httpclient.New(&a.cfg.Sender.HTTPClient, a.logger)

	// Circuit breaker per node — порог 5 ошибок подряд, cooldown 30s.
	// Параметры можно вынести в конфиг в Phase 4.
	var cb usecase.CircuitBreaker
	if a.redis != nil {
		cb = circuitbreaker.New(a.redis, 5, 30*time.Second)
	}
	sendUC := usecase.NewSendUsecase(httpc, a.chWriter, cb, a.logger)

	// gRPC adapter для sync.
	grpcSvc := grpcadapter.NewServer(sendUC, a.metrics, a.logger)

	// Async consumer.
	a.producer = kafkapf.NewProducer(a.cfg)
	nodeReader := nodepg.New(a.pg, a.cipher, a.logger)
	asyncProc := usecase.NewAsyncProcessor(nodeReader, sendUC, a.producer, a.cfg.Kafka.DLQTopic, a.metrics, a.logger)
	a.consumer = kafkaadapter.NewConsumerGroup(a.cfg, a.cfg.Kafka.AsyncTopic, asyncProc, a.logger)
	a.consumer.Start(ctx)

	// CH partition-drop housekeeping (§4.3 ТЗ): фоновый цикл раз в сутки.
	hk := usecase.NewCHHousekeeping(a.ch, nodeReader, a.logger)
	go hk.Run(ctx)

	// Kafka lag reporter (§6 ТЗ): раз в 15 секунд снимаем Stats() со всех
	// инстансов consumer-группы и пушим в Prometheus.
	go a.reportKafkaLag(ctx)

	errCh := make(chan error, 2)
	go func() { errCh <- a.startGRPC(grpcSvc) }()
	go func() { errCh <- a.startAdminHTTP() }()

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err
	}
}

func (a *App) startGRPC(svc *grpcadapter.Server) error {
	lis, err := net.Listen("tcp", a.cfg.Sender.GRPCAddr)
	if err != nil {
		return fmt.Errorf("sender grpc listen %s: %w", a.cfg.Sender.GRPCAddr, err)
	}

	a.grpcSrv = grpc.NewServer(
		grpc.MaxConcurrentStreams(a.cfg.Sender.GRPCMaxConcurrentStreams),
	)

	senderv1.RegisterSenderServiceServer(a.grpcSrv, svc)

	hsrv := health.NewServer()
	hsrv.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	hsrv.SetServingStatus("databus.sender.v1.SenderService", healthpb.HealthCheckResponse_SERVING)
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
	r.Use(sentrypf.GinMiddleware("sender"), metrics.GinMiddleware(a.metrics), gin.Recovery())

	hc := healthcheck.New(
		[]healthcheck.Checker{
			pgpf.HealthChecker("postgres", a.pg),
			chpf.HealthChecker("clickhouse", a.ch),
		},
		nil,
	)
	hc.Register(r)
	r.GET("/metrics", gin.WrapH(a.metrics.Handler()))

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

// reportKafkaLag — фоновый цикл (§6 ТЗ databus_kafka_lag).
// Опрашивает все consumer-инстансы группы и публикует Lag в Prometheus.
// Интервал 15 секунд — компромисс между актуальностью и нагрузкой.
func (a *App) reportKafkaLag(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	group := a.consumer.Group()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, snap := range a.consumer.Snapshot() {
				topic := snap.Topic
				if topic == "" {
					topic = a.cfg.Kafka.AsyncTopic
				}
				part := snap.Partition
				if part == "" {
					part = "all"
				}
				a.metrics.KafkaLag.WithLabelValues(topic, part, group).Set(float64(snap.Lag))
			}
		}
	}
}

func (a *App) Stop(ctx context.Context) error {
	a.logger.Info("sender shutting down")

	if a.consumer != nil {
		a.consumer.Stop()
	}

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

	if a.chWriter != nil {
		flushCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		a.chWriter.Stop(flushCtx)
		cancel()
	}
	if a.producer != nil {
		_ = a.producer.Close()
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
