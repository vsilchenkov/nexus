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
	"nexus/internal/platform/nodestatus"
	otelpf "nexus/internal/platform/otel"
	pgpf "nexus/internal/platform/pg"
	"nexus/internal/platform/queuecancel"
	"nexus/internal/platform/rdns"
	recoverypf "nexus/internal/platform/recovery"
	"nexus/internal/platform/reloader"
	"nexus/internal/platform/requestid"
	"nexus/internal/platform/safego"
	sentrypf "nexus/internal/platform/sentry"
	grpcadapter "nexus/internal/sender/adapter/in/grpc"
	kafkaadapter "nexus/internal/sender/adapter/in/kafka"
	"nexus/internal/sender/adapter/out/chlog"
	"nexus/internal/sender/adapter/out/chlogretry"
	"nexus/internal/sender/adapter/out/httpclient"
	"nexus/internal/sender/adapter/out/nodepg"
	"nexus/internal/sender/usecase"
	senderv1 "nexus/proto/sender/v1"
)

// grpcResponseEnvelopeReserve — запас под envelope gRPC SendResponse (заголовки,
// статус) при вычислении лимита тела ответа из grpc_max_message_bytes (§43-rev):
// тело + envelope должны влезть в gRPC-сообщение, иначе ResourceExhausted.
const grpcResponseEnvelopeReserve = 1 << 20 // 1 МиБ

type App struct {
	cfg     *config.Config
	logger  logging.Logger
	pg      *pgxpool.Pool
	ch      chdrv.Conn
	redis   *goredis.Client
	cipher  *crypto.Cipher
	metrics *metrics.Metrics

	grpcSrv       *grpc.Server
	adminSrv      *http.Server
	chMgr         *chpf.Manager
	chWriter      *chlog.WriterManager
	producer      *kafkapf.Producer
	consumer      *kafkaadapter.ConsumerGroup
	retryConsumer *kafkaadapter.ChLogRetryConsumer // §38: дренаж nexus.logs.retry в CH (nil если retry выключен)
	dlqReproc     *kafkaadapter.DLQReprocessor     // §36: авто-репроцессор DLQ (nil если выключен)
	pausedSweep   *kafkaadapter.PausedSweeper      // §3.6: sweeper delay-топика paused-узлов (nil если выключен)
	otelShutdown  otelpf.ShutdownFunc

	// done-каналы фоновых горутин — Stop дожидается их завершения
	// (Phase AUD.3): housekeeping/lag-reporter/reload-callbacks не должны
	// бежать параллельно с закрытием CH/Kafka/Redis-соединений.
	housekeepingDone <-chan struct{}
	kafkaLagDone     <-chan struct{}
	reloadDone       <-chan struct{}
	dlqReprocDone    <-chan struct{} // §36: done-канал sweeper'а DLQ
	pausedSweepDone  <-chan struct{} // §3.6: done-канал sweeper'а delay-топика
	shipperDone      <-chan struct{} // §51: done-канал шиппера служебных логов

	// §51: ручка runtime-уровня логов + кольцо для Redis-шиппера.
	logCtl *bootstrap.LogController

	// identity — §70: идентификатор ноды. Sender ничего не захватывает и не
	// усыновляет, но обязан отличать свои ClickHouse-таблицы от чужих.
	identity bootstrap.Identity
}

func New(
	cfg *config.Config,
	pg *pgxpool.Pool,
	ch chdrv.Conn,
	redis *goredis.Client,
	cipher *crypto.Cipher,
	otelShutdown otelpf.ShutdownFunc,
	identity bootstrap.Identity,
	logger logging.Logger,
	logCtl *bootstrap.LogController,
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
		logCtl:       logCtl,
		identity:     identity,
	}
}

func (a *App) Start(ctx context.Context) error {
	// Общие сервисы.
	// ClickHouse Manager — владелец соединения для hot-reload (§8.4 / Phase 6.3.2.5).
	// Все consumers (chWriter, CHHousekeeping, HealthChecker) идут через него,
	// а не через raw a.ch, чтобы при reload swap conn'а был для них прозрачным.
	a.chMgr = chpf.NewManager(a.ch, chpf.New, &a.cfg.ClickHouse, a.logger)
	// §70.3: гейт владения ClickHouse-БД. Sender ничего не захватывает и не
	// усыновляет (он может стартовать раньше Web и «застолбить» БД, которую Web
	// ещё не связал с командой) — только отличает свои таблицы от чужих.
	chGuard := chpf.NewGuard(a.chMgr, a.identity.ID, a.logger)
	a.chMgr.OnReload(chGuard.Invalidate)
	// §38: Kafka-продьюсер нужен и async/DLQ, и retrier'у проваленных CH-батчей —
	// создаём ДО chWriter (раньше создавался ниже, в секции async-consumer).
	a.producer = kafkapf.NewProducer(a.cfg, kafkapf.WithMetrics(a.metrics))
	var chRetrier chlog.BatchRetrier
	if a.cfg.Kafka.RetryTopic != "" {
		chRetrier = chlogretry.New(a.producer, a.cfg.Kafka.RetryTopic, a.cfg.Kafka.Topic.MaxMessageBytes, a.logger)
	}
	a.chWriter = chlog.NewManagerWithRetrier(a.chMgr, &a.cfg.ClickHouse, chRetrier, a.metrics, a.logger)
	// §43-rev: транспортный лимит тела ОТВЕТА = gRPC-потолок минус запас под
	// envelope SendResponse (заголовки/статус). httpclient оборвёт чтение на нём
	// (memory-safe), Send отдаст клиенту 502. Один источник — config.
	respLimit := a.cfg.Sender.GRPCMaxMessageBytes - grpcResponseEnvelopeReserve
	httpc := httpclient.New(&a.cfg.Sender.HTTPClient, a.logger, respLimit)

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
	// §67: reverse-DNS резолв client_host — асинхронный, кеш Redis + L1,
	// fail-open (a.redis может быть nil → режим «только L1»).
	var sendOpts []usecase.SendOption
	if !a.cfg.Sender.RDNS.Disabled {
		hr := rdns.New(ctx, a.redis, rdns.Config{
			Timeout:     time.Duration(a.cfg.Sender.RDNS.TimeoutMs) * time.Millisecond,
			CacheTTL:    time.Duration(a.cfg.Sender.RDNS.CacheTTLSec) * time.Second,
			NegativeTTL: time.Duration(a.cfg.Sender.RDNS.NegativeTTLSec) * time.Second,
		}, a.logger)
		sendOpts = append(sendOpts, usecase.WithHostResolver(hr))
	}
	sendUC := usecase.NewSendUsecase(httpc, a.chWriter, cb, a.logger, respLimit, sendOpts...)

	// §46: персист исхода последнего вызова узла в Redis (переживает рестарт →
	// статус «Down» корректен после деплоя). Noop без Redis — поведение как §41.
	var nodeStatus usecase.NodeStatusWriter = nodestatus.Noop{}
	if a.redis != nil {
		nodeStatus = nodestatus.NewRedisWriter(a.redis, a.logger)
	}

	// gRPC adapter для sync.
	grpcSvc := grpcadapter.NewServer(sendUC, a.metrics, nodeStatus, a.logger)

	// Async consumer. (Продьюсер уже создан выше — §38.)
	nodeReader := nodepg.New(a.pg, a.cipher, a.logger)

	// §37: миграция CH-таблиц — добавить колонку node_id ДО старта consumer'а и
	// репроцессора (иначе INSERT по новой схеме упадёт на старых таблицах).
	// Идемпотентно (ALTER … IF NOT EXISTS); новые таблицы — из шаблона.
	if a.chMgr != nil {
		if tables, err := nodeReader.ListClickHouseTables(ctx); err != nil {
			a.logger.Warn("§37 ensure node_id: list tables failed", a.logger.Err(err))
		} else {
			// §70.4: чужие таблицы из обслуживания схемы исключаются (ADD COLUMN
			// фиксирует порядок колонок, backfill мутирует все строки).
			tables = chGuard.FilterManagedTables(ctx, tables)
			chpf.EnsureNodeIDColumn(ctx, a.chMgr.Conn(), tables, a.logger)
			chpf.EnsureHTTPMethodColumn(ctx, a.chMgr.Conn(), tables, a.logger) // §39
			chpf.EnsureClientHostColumn(ctx, a.chMgr.Conn(), tables, a.logger) // §67
			chpf.EnsureBodySizeColumns(ctx, a.chMgr.Conn(), tables, a.logger)  // §42-доп
			chpf.BackfillBodySizes(ctx, a.chMgr.Conn(), tables, a.logger)      // §42-доп
		}
	}
	// §34.4: cancel-set отменённых через UI сообщений (Redis). nil при отсутствии
	// Redis — проверка в AsyncProcessor тогда выключена.
	var cancelSet usecase.CancelSet
	if a.redis != nil {
		cancelSet = queuecancel.New(a.redis)
	}
	// §3.6: сообщения paused-узлов уезжают в delay-топик, а не удерживают
	// партицию основного топика (в ней по хешу node_path лежат ДРУГИЕ узлы).
	pausedRequeue := usecase.WithPausedRequeue(a.cfg.Kafka.PausedTopic)
	asyncProc := usecase.NewAsyncProcessor(nodeReader, sendUC, a.producer, cancelSet, nodeStatus, a.cfg.Kafka.DLQTopic, a.metrics, a.logger, pausedRequeue)
	a.consumer = kafkaadapter.NewConsumerGroup(a.cfg, a.cfg.Kafka.AsyncTopic, asyncProc, a.logger, kafkaadapter.WithMetrics(a.metrics))
	a.consumer.Start(ctx)

	// §38: retry-консьюмер дренит nexus.logs.retry обратно в CH. Отдельная
	// consumer-group (<group>-clog-retry), чтобы не конкурировать за партиции
	// с основным async-consumer'ом. Прямой INSERT в обход буфера Writer'а —
	// при сбое CH не коммитит offset, Kafka передоставит (цикла нет).
	if a.cfg.Kafka.RetryTopic != "" {
		retryHandler := kafkaadapter.NewChLogRetryHandler(a.chWriter, a.metrics, a.logger)
		a.retryConsumer = kafkaadapter.NewChLogRetryConsumer(
			a.cfg, a.cfg.Kafka.RetryTopic, a.cfg.Kafka.ConsumerGroup+"-clog-retry",
			retryHandler, a.logger)
		a.retryConsumer.Start(ctx)
	}

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

	// §3.6: sweeper delay-топика — обслуживает бэклог paused-узлов отдельно от
	// основного потока. Тот же AsyncProcessor: узел всё ещё на паузе → перенос в
	// хвост delay-топика, ожил → доставка, отменён/disabled → drop.
	// ВНИМАНИЕ: выключение sweeper'а означает, что накопленный за паузу бэклог не
	// будет доставлен вообще (и протухнет по retention топика).
	if !a.cfg.Sender.PausedSweep.Disabled {
		a.pausedSweep = kafkaadapter.NewPausedSweeper(
			a.cfg, asyncProc,
			time.Duration(a.cfg.Sender.PausedSweep.IntervalSec)*time.Second,
			a.cfg.Sender.PausedSweep.MaxScan, a.metrics, a.logger)
		a.pausedSweepDone = safego.Go(a.logger, "sender.pausedSweep", func() {
			a.pausedSweep.Run(ctx)
		})
	}

	// CH partition-drop housekeeping (§4.3 ТЗ): фоновый цикл раз в сутки.
	hk := usecase.NewCHHousekeeping(a.chMgr, nodeReader, a.logger).WithOwnership(chGuard)
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
		// §51: шиппер служебных логов в Redis (nexus:logs:sender).
		a.shipperDone = a.logCtl.StartRedisShipper(ctx, a.redis, a.logger)

		reloadSub := reloader.NewSubscriber(a.redis, a.logger)
		reloadSub.Register(reloader.SectionSentry,
			bootstrap.SentryReloader(a.pg, a.cfg, a.cfg.Build.ProjectName, a.cfg.Build.Version, a.logger))
		reloadSub.Register(reloader.SectionClickHouse,
			bootstrap.ClickHouseReloader(a.pg, a.cfg, a.chMgr,
				[]bootstrap.WriterReloader{a.chWriter}, a.logger))
		// §51: runtime-уровень логов из app_settings.logging.level (+ сид старта).
		applyLogLevel := bootstrap.LogLevelReloader(a.pg, a.logCtl, a.logger)
		if err := applyLogLevel(ctx); err != nil {
			a.logger.Warn("seed log level from app_settings failed; using yaml level", a.logger.Err(err))
		}
		reloadSub.Register(reloader.SectionLogging, applyLogLevel)
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
		// §42: лимит размера сообщения (оба направления). Дефолт gRPC recv 4 МиБ
		// мал — тело запроса (до receiver.max_body_bytes) и тело ответа апстрима
		// (десятки МБ) иначе режутся ResourceExhausted.
		grpc.MaxRecvMsgSize(a.cfg.Sender.GRPCMaxMessageBytes),
		grpc.MaxSendMsgSize(a.cfg.Sender.GRPCMaxMessageBytes),
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
			a.publishLag(a.consumer.Snapshot(), group, a.cfg.Kafka.AsyncTopic)
			// §38: lag retry-топика = объём проваленных батчей, ждущих CH.
			if a.retryConsumer != nil {
				a.publishLag(a.retryConsumer.Snapshot(), a.retryConsumer.Group(), a.cfg.Kafka.RetryTopic)
			}
			// §3.6: lag delay-топика = объём бэклога paused-узлов. Растёт, пока
			// узлы на паузе; НЕ убывающий после снятия пауз lag означает, что
			// sweeper не работает и бэклог протухнет по retention — повод для алерта.
			if a.pausedSweep != nil {
				a.publishLag(a.pausedSweep.Snapshot(), a.pausedSweep.Group(), a.cfg.Kafka.PausedTopic)
			}
		}
	}
}

// publishLag пушит снимки lag в Prometheus. Пустые topic/partition в
// kafka-go ReaderStats заменяются на конфигурационный топик и "all".
func (a *App) publishLag(snaps []kafkaadapter.LagSnapshot, group, defaultTopic string) {
	for _, snap := range snaps {
		topic := snap.Topic
		if topic == "" {
			topic = defaultTopic
		}
		part := snap.Partition
		if part == "" {
			part = "all"
		}
		a.metrics.KafkaLag.WithLabelValues(topic, part, group).Set(float64(snap.Lag))
	}
}

func (a *App) Stop(ctx context.Context) error {
	a.logger.Info("sender shutting down")

	if a.consumer != nil {
		a.consumer.Stop()
	}
	// §38: retry-консьюмер останавливаем до chWriter — его InsertBatch ходит
	// через chWriter; после Stop chWriter вернёт ошибку и offset не закоммитится.
	if a.retryConsumer != nil {
		a.retryConsumer.Stop()
	}
	// §36: закрываем consumer DLQ-sweeper'а — Run выйдет из FetchMessage и
	// завершит горутину (дожидаемся ниже через dlqReprocDone).
	if a.dlqReproc != nil {
		a.dlqReproc.Stop()
	}
	// §3.6: аналогично для sweeper'а delay-топика paused-узлов.
	if a.pausedSweep != nil {
		a.pausedSweep.Stop()
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
	safego.Await(awaitCtx, a.pausedSweepDone, a.logger, "sender.pausedSweep")
	safego.Await(awaitCtx, a.shipperDone, a.logger, "sender.logShipper")
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
