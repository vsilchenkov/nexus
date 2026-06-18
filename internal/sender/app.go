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

	"nexus/internal/platform/bootstrap"
	"nexus/internal/platform/circuitbreaker"
	chpf "nexus/internal/platform/clickhouse"
	"nexus/internal/platform/config"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/healthcheck"
	kafkapf "nexus/internal/platform/kafka"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/metrics"
	otelpf "nexus/internal/platform/otel"
	pgpf "nexus/internal/platform/pg"
	"nexus/internal/platform/queuecancel"
	recoverypf "nexus/internal/platform/recovery"
	"nexus/internal/platform/reloader"
	"nexus/internal/platform/requestid"
	"nexus/internal/platform/safego"
	sentrypf "nexus/internal/platform/sentry"
	grpcadapter "nexus/internal/sender/adapter/in/grpc"
	kafkaadapter "nexus/internal/sender/adapter/in/kafka"
	"nexus/internal/sender/adapter/out/chlog"
	"nexus/internal/sender/adapter/out/httpclient"
	"nexus/internal/sender/adapter/out/nodepg"
	"nexus/internal/sender/usecase"
	senderv1 "nexus/proto/sender/v1"
)

type App struct {
	cfg     *config.Config
	logger  logging.Logger
	pg      *pgxpool.Pool
	ch      chdrv.Conn
	redis   *goredis.Client
	cipher  *crypto.Cipher
	metrics *metrics.Metrics

	grpcSrv      *grpc.Server
	adminSrv     *http.Server
	chMgr        *chpf.Manager
	chWriter     *chlog.WriterManager
	producer     *kafkapf.Producer
	consumer     *kafkaadapter.ConsumerGroup
	dlqReproc    *kafkaadapter.DLQReprocessor // §36: авто-репроцессор DLQ (nil если выключен)
	otelShutdown otelpf.ShutdownFunc

	// done-каналы фоновых горутин — Stop дожидается их завершения
	// (Phase AUD.3): housekeeping/lag-reporter/reload-callbacks не должны
	// бежать параллельно с закрытием CH/Kafka/Redis-соединений.
	housekeepingDone <-chan struct{}
	kafkaLagDone     <-chan struct{}
	reloadDone       <-chan struct{}
	dlqReprocDone    <-chan struct{} // §36: done-канал sweeper'а DLQ
}

func New(
	cfg *config.Config,
	pg *pgxpool.Pool,
	ch chdrv.Conn,
	redis *goredis.Client,
	cipher *crypto.Cipher,
	otelShutdown otelpf.ShutdownFunc,
	logger logging.Logger,
) *App {
	return &App{
		cfg:          cfg,
		logger:       logger,
		pg:           pg,
		ch:           ch,
		redis:        redis,
		cipher:       cipher,
		metrics:      metrics.New("sender"),
		otelShutdown: otelShutdown,
	}
}

func (a *App) Start(ctx context.Context) error {
	// Общие сервисы.
	// ClickHouse Manager — владелец соединения для hot-reload (§8.4 / Phase 6.3.2.5).
	// Все consumers (chWriter, CHHousekeeping, HealthChecker) идут через него,
	// а не через raw a.ch, чтобы при reload swap conn'а был для них прозрачным.
	a.chMgr = chpf.NewManager(a.ch, chpf.New, &a.cfg.ClickHouse, a.logger)
	a.chWriter = chlog.NewManagerWithFallback(a.chMgr, &a.cfg.ClickHouse, a.cfg.ClickHouse.FallbackDir, a.metrics, a.logger)
	httpc := httpclient.New(&a.cfg.Sender.HTTPClient, a.logger)

	// Circuit breaker per node — порог 5 ошибок подряд, cooldown 30s.
	// Параметры можно вынести в конфиг в Phase 4.
	var (
		cb      usecase.CircuitBreaker
		breaker usecase.BreakerInspector // §36: read-only IsOpen для репроцессора DLQ
	)
	if a.redis != nil {
		b := circuitbreaker.New(a.redis, 5, 30*time.Second)
		cb, breaker = b, b
	}
	sendUC := usecase.NewSendUsecase(httpc, a.chWriter, cb, a.logger)

	// gRPC adapter для sync.
	grpcSvc := grpcadapter.NewServer(sendUC, a.metrics, a.logger)

	// Async consumer.
	a.producer = kafkapf.NewProducer(a.cfg, kafkapf.WithMetrics(a.metrics))
	nodeReader := nodepg.New(a.pg, a.cipher, a.logger)
	// §34.4: cancel-set отменённых через UI сообщений (Redis). nil при отсутствии
	// Redis — проверка в AsyncProcessor тогда выключена.
	var cancelSet usecase.CancelSet
	if a.redis != nil {
		cancelSet = queuecancel.New(a.redis)
	}
	asyncProc := usecase.NewAsyncProcessor(nodeReader, sendUC, a.producer, cancelSet, a.cfg.Kafka.DLQTopic, a.metrics, a.logger)
	a.consumer = kafkaadapter.NewConsumerGroup(a.cfg, a.cfg.Kafka.AsyncTopic, asyncProc, a.logger, kafkaadapter.WithMetrics(a.metrics))
	a.consumer.Start(ctx)

	// §36: авто-репроцессор DLQ — фоновый sweeper над nexus.async.dlq, повторно
	// доставляет неудачные async-сообщения до TTL. Отдельная consumer-группа,
	// не мешает основному consumer'у. Выключается sender.reprocessor.disabled.
	if !a.cfg.Sender.Reprocessor.Disabled {
		reprocUC := usecase.NewDLQReprocessor(
			nodeReader, sendUC, a.producer, a.chWriter, cancelSet, breaker,
			a.cfg.Kafka.DLQTopic, a.metrics, a.logger)
		a.dlqReproc = kafkaadapter.NewDLQReprocessor(
			a.cfg, reprocUC,
			time.Duration(a.cfg.Sender.Reprocessor.IntervalSec)*time.Second,
			a.cfg.Sender.Reprocessor.MaxScan, a.metrics, a.logger)
		a.dlqReprocDone = safego.Go(a.logger, "sender.dlqReprocessor", func() {
			a.dlqReproc.Run(ctx)
		})
	}

	// CH partition-drop housekeeping (§4.3 ТЗ): фоновый цикл раз в сутки.
	hk := usecase.NewCHHousekeeping(a.chMgr, nodeReader, a.logger)
	a.housekeepingDone = safego.Go(a.logger, "sender.chHousekeeping", func() {
		hk.Run(ctx)
	})

	// Kafka lag reporter (§6 ТЗ): раз в 15 секунд снимаем Stats() со всех
	// инстансов consumer-группы и пушим в Prometheus.
	a.kafkaLagDone = safego.Go(a.logger, "sender.reportKafkaLag", func() {
		a.reportKafkaLag(ctx)
	})

	// Hot-reload Sentry и ClickHouse (§14.5 / §8.4 ТЗ, Phase 6.3.2 + 6.3.2.5).
	// Подписчик слушает Redis pub/sub и применяет изменения, опубликованные Web
	// после PUT /api/settings/app. Sentry — sentry.Init c новыми параметрами.
	// CH — полный reconnect через chpf.Manager + пересоздание chlog.Writer.
	if a.redis != nil {
		reloadSub := reloader.NewSubscriber(a.redis, a.logger)
		reloadSub.Register(reloader.SectionSentry,
			bootstrap.SentryReloader(a.pg, a.cfg, a.cfg.Build.ProjectName, a.cfg.Build.Version, a.logger))
		reloadSub.Register(reloader.SectionClickHouse,
			bootstrap.ClickHouseReloader(a.pg, a.cfg, a.chMgr,
				[]bootstrap.WriterReloader{a.chWriter}, a.logger))
		a.reloadDone = safego.Go(a.logger, "sender.reloadSubscriber", func() {
			reloadSub.Run(ctx)
		})
	}

	errCh := make(chan error, 2)
	go func() {
		defer safego.Recover(a.logger, "sender.grpc")
		errCh <- a.startGRPC(grpcSvc)
	}()
	go func() {
		defer safego.Recover(a.logger, "sender.adminHTTP")
		errCh <- a.startAdminHTTP()
	}()

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
		// OTel: extract traceparent из incoming metadata + server-span
		// вокруг каждого unary-вызова (§16 ТЗ, Phase 8.3).
		grpc.UnaryInterceptor(otelpf.UnaryServerInterceptor()),
	)

	senderv1.RegisterSenderServiceServer(a.grpcSrv, svc)

	hsrv := health.NewServer()
	hsrv.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	hsrv.SetServingStatus("nexus.sender.v1.SenderService", healthpb.HealthCheckResponse_SERVING)
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
	r.Use(
		requestid.GinMiddleware(),
		otelpf.GinMiddleware("sender"),
		sentrypf.GinMiddleware("sender"),
		recoverypf.GinMiddleware(a.logger),
		metrics.GinMiddleware(a.metrics),
	)

	hc := healthcheck.New(
		[]healthcheck.Checker{
			pgpf.HealthChecker("postgres", a.pg),
			chpf.HealthChecker("clickhouse", a.chMgr),
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

// reportKafkaLag — фоновый цикл (§6 ТЗ nexus_kafka_lag).
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
	// §36: закрываем consumer DLQ-sweeper'а — Run выйдет из FetchMessage и
	// завершит горутину (дожидаемся ниже через dlqReprocDone).
	if a.dlqReproc != nil {
		a.dlqReproc.Stop()
	}

	if a.grpcSrv != nil {
		done := make(chan struct{})
		go func() {
			defer safego.Recover(a.logger, "sender.grpcGracefulStop")
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

	// Фоновые горутины должны завершиться до закрытия CH/Kafka-ресурсов:
	// runner уже отменил их ctx, здесь только дожидаемся выхода.
	awaitCtx, awaitCancel := context.WithTimeout(ctx, 10*time.Second)
	safego.Await(awaitCtx, a.housekeepingDone, a.logger, "sender.chHousekeeping")
	safego.Await(awaitCtx, a.kafkaLagDone, a.logger, "sender.reportKafkaLag")
	safego.Await(awaitCtx, a.reloadDone, a.logger, "sender.reloadSubscriber")
	safego.Await(awaitCtx, a.dlqReprocDone, a.logger, "sender.dlqReprocessor")
	awaitCancel()

	if a.chWriter != nil {
		flushCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		a.chWriter.Stop(flushCtx)
		cancel()
	}
	if a.chMgr != nil {
		closeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_ = a.chMgr.Close(closeCtx)
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
	if a.otelShutdown != nil {
		shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := a.otelShutdown(shutdownCtx); err != nil {
			a.logger.Warn("otel tracer shutdown returned error", a.logger.Err(err))
		}
	}
	return nil
}
