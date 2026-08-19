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

	"nexus/internal/domain"
	"nexus/internal/platform/bootstrap"
	"nexus/internal/platform/config"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/grpcsender"
	"nexus/internal/platform/healthcheck"
	kafkapf "nexus/internal/platform/kafka"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/metrics"
	"nexus/internal/platform/nodeevents"
	otelpf "nexus/internal/platform/otel"
	pgpf "nexus/internal/platform/pg"
	"nexus/internal/platform/ratelimit"
	recoverypf "nexus/internal/platform/recovery"
	redispf "nexus/internal/platform/redis"
	"nexus/internal/platform/redislock"
	"nexus/internal/platform/reloader"
	"nexus/internal/platform/requestid"
	"nexus/internal/platform/safego"
	sentrypf "nexus/internal/platform/sentry"
	httpadapter "nexus/internal/receiver/adapter/in/http"
	"nexus/internal/receiver/adapter/out/nodecache"
	rabbitmqadapter "nexus/internal/receiver/adapter/out/rabbitmq"
	"nexus/internal/receiver/usecase"
	"nexus/internal/receiver/usecase/port"
)

type App struct {
	cfg     *config.Config
	logger  logging.Logger
	pg      *pgxpool.Pool
	redis   *goredis.Client
	cipher  *crypto.Cipher
	metrics *metrics.Metrics

	srv          *http.Server
	health       *healthcheck.Handler
	senderCl     *grpcsender.Client
	producer     *kafkapf.Producer
	otelShutdown otelpf.ShutdownFunc

	// §27: Puller-воркеры RabbitMQAsync.
	pullerCancel context.CancelFunc
	pullerDone   chan struct{}

	// done-каналы фоновых горутин — Stop дожидается их завершения
	// (Phase AUD.3: reload-callbacks не должны бежать параллельно
	// с закрытием соединений).
	reloadDone         <-chan struct{}
	shipperDone        <-chan struct{}
	nodeInvalidateDone <-chan struct{} // §57: подписчик инвалидации конфига узла

	// §51: ручка runtime-уровня логов + кольцо для Redis-шиппера.
	logCtl *bootstrap.LogController
}

func New(cfg *config.Config, pg *pgxpool.Pool, redis *goredis.Client, cipher *crypto.Cipher, otelShutdown otelpf.ShutdownFunc, schema bootstrap.SchemaState, logger logging.Logger, logCtl *bootstrap.LogController) *App {
	// §70.7: instance.id уже сверен с PostgreSQL в main (MustInstanceIdentity).
	m := metrics.New("receiver", metrics.WithInstance(cfg.Instance.ID))
	// §74.3: состояние схемы — в мониторинг (см. комментарий в web.New).
	m.SetSchemaState(schema.Version, schema.Ahead)
	return &App{
		cfg:          cfg,
		logger:       logger,
		pg:           pg,
		redis:        redis,
		cipher:       cipher,
		metrics:      m,
		otelShutdown: otelShutdown,
		logCtl:       logCtl,
	}
}

func (a *App) Start(ctx context.Context) error {
	senderCl, err := grpcsender.New(&a.cfg.Receiver.SenderGRPC, a.logger)
	if err != nil {
		return fmt.Errorf("init sender client: %w", err)
	}
	a.senderCl = senderCl

	reader := a.buildNodeReader()
	routeUC := usecase.NewRouteUsecase(reader, a.senderCl, a.cfg.Receiver.MaxHops, a.logger)

	a.producer = kafkapf.NewProducer(a.cfg, kafkapf.WithMetrics(a.metrics))
	routeAsyncUC := usecase.NewRouteAsyncUsecase(reader, a.producer, a.cfg.Kafka.AsyncTopic, a.cfg.Receiver.MaxHops, a.logger)

	a.startPullerManager(ctx)

	a.warnAsyncBodyLimitOverKafka()

	r, err := a.buildHTTPRouter(routeUC, routeAsyncUC)
	if err != nil {
		return err
	}

	a.startBackgroundSubscribers(ctx, reader)

	return a.serve(ctx, r)
}

// buildNodeReader собирает трёхуровневое чтение конфига узла (§9.2):
// L2 in-memory поверх Redis+PostgreSQL.
func (a *App) buildNodeReader() port.NodeReader {
	baseReader := nodecache.New(a.redis, a.pg, a.cipher, time.Duration(a.cfg.Redis.NodeTTLSec)*time.Second, a.logger)
	return nodecache.NewL2(baseReader, nodecache.L2Config{
		Enabled:  a.cfg.Receiver.L2Cache.Enabled,
		Size:     a.cfg.Receiver.L2Cache.Size,
		TTL:      time.Duration(a.cfg.Receiver.L2Cache.TTLMs) * time.Millisecond,
		StaleTTL: time.Duration(a.cfg.Receiver.L2Cache.StaleTTLMs) * time.Millisecond,
	}, a.logger, a.metrics)
}

// startPullerManager поднимает Puller-менеджер RabbitMQAsync (§27): один воркер
// на узел, reconcile из PG. Выключен флагом или отсутствием таких узлов — no-op.
// ClientIP/IP лога формируется как rabbitmq://host:port/vhost.
//
// Контекст менеджера отвязан от ctx приложения (WithoutCancel + свой cancel):
// Stop гасит воркеров отдельно и дожидается их, чтобы незакоммиченные сообщения
// вернулись в очередь брокера, а не потерялись на общем ctx.Done.
func (a *App) startPullerManager(ctx context.Context) {
	if a.cfg.Receiver.Puller.Disabled {
		return
	}
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
	).WithNodeLease(redislock.NewLease(a.redis, bootstrap.ReplicaName()))
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

// buildHTTPRouter собирает gin-роутер: middleware, health/metrics и маршруты
// шины под rate-limit.
func (a *App) buildHTTPRouter(routeUC *usecase.RouteUsecase, routeAsyncUC *usecase.RouteAsyncUsecase) (*gin.Engine, error) {
	handler := httpadapter.New(
		routeUC, routeAsyncUC,
		a.cfg.Receiver.MaxBodyBytes, a.cfg.Receiver.MaxAsyncBodyBytes,
		a.metrics, a.logger,
	)

	rl := ratelimit.New(a.redis, ratelimit.WithErrorSink(a.metrics))
	rlMw := httpadapter.RateLimitMiddleware(rl, a.cfg.Receiver.RateLimitPerNode, a.logger)

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	// Phase AUD.5: X-Forwarded-For доверяем только перечисленным прокси
	// (дефолт — loopback + приватные сети), иначе клиент подделывает IP
	// в логах ClickHouse и аудите.
	if err := r.SetTrustedProxies(a.cfg.Receiver.TrustedProxies); err != nil {
		return nil, fmt.Errorf("receiver trusted_proxies: %w", err)
	}
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
	// §93.5: Stop переводит этот же handler в режим дренажа, поэтому он живёт
	// полем App, а не только внутри роутера.
	a.health = hc
	hc.Register(r)
	r.GET("/metrics", gin.WrapH(a.metrics.Handler()))

	handler.Register(r, rlMw)
	return r, nil
}

// startBackgroundSubscribers запускает фоновых слушателей Redis: шиппер
// служебных логов (§51), hot-reload Sentry и уровня логов (§14.5/§51),
// инвалидацию конфига узла (§57).
func (a *App) startBackgroundSubscribers(ctx context.Context, reader port.NodeReader) {
	// §51: шиппер служебных логов в Redis (nexus:logs:receiver) — консоль
	// «Логи» в Web показывает записи всех трёх сервисов.
	a.shipperDone = a.logCtl.StartRedisShipper(ctx, a.redis, a.logger)

	// Hot-reload Sentry: подписываемся на pub/sub-канал, чтобы менять DSN/level
	// без рестарта при изменении app_settings через UI Web (§14.5 ТЗ).
	reloadSub := reloader.NewSubscriber(a.redis, a.logger)
	reloadSub.Register(reloader.SectionSentry,
		bootstrap.SentryReloader(a.pg, a.cfg, a.cfg.Build.ProjectName, a.cfg.Build.Version, a.cipher, a.logger))
	// §51: runtime-уровень логов из app_settings.logging.level. Тот же Reloader
	// сидирует стартовое значение (Init построил логгер до чтения app_settings).
	applyLogLevel := bootstrap.LogLevelReloader(a.pg, a.logCtl, a.cipher, a.logger)
	if err := applyLogLevel(ctx); err != nil {
		a.logger.Warn("seed log level from app_settings failed; using yaml level", a.logger.Err(err))
	}
	reloadSub.Register(reloader.SectionLogging, applyLogLevel)
	a.reloadDone = safego.Go(a.logger, "receiver.reloadSubscriber", func() {
		reloadSub.Run(ctx)
	})

	// §57: гарантированная инвалидация конфига узла. Web публикует событие при
	// изменении узла (в т.ч. авторизации); выселяем его из L1+Redis, следующий
	// запрос перечитает свежий конфиг из PG, не дожидаясь TTL.
	if inv, ok := reader.(nodecache.Invalidator); ok {
		nodeSub := nodeevents.NewSubscriber(a.redis, a.logger, a.nodeInvalidateHandler(inv))
		a.nodeInvalidateDone = safego.Go(a.logger, "receiver.nodeInvalidateSubscriber", func() {
			nodeSub.Run(ctx)
		})
	}
}

// serve поднимает HTTP-сервер и блокируется до остановки приложения или
// фатальной ошибки слушателя.
func (a *App) serve(ctx context.Context, r *gin.Engine) error {
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

// drain выводит реплику из ротации перед остановкой (§93.5): /ready начинает
// отвечать 503, и балансировщик перестаёт слать новые запросы, пока текущие
// доигрывают. В одиночной установке (shutdown.drain_sec отрицательный) — no-op.
func (a *App) drain(ctx context.Context) {
	pause := a.cfg.Shutdown.Drain()
	if a.health == nil || pause <= 0 {
		return
	}
	a.logger.Info("receiver draining before shutdown",
		a.logger.Str("pause", pause.String()))
	healthcheck.Drain(ctx, a.health, pause)
}

func (a *App) Stop(ctx context.Context) error {
	a.drain(ctx)

	shutdownCtx, cancel := context.WithTimeout(ctx, a.cfg.Shutdown.Timeout())
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
	// Дожидаемся фоновых горутин до закрытия соединений: reload-callback
	// не должен дёргать pg/redis, которые main уже закрывает.
	safego.Await(shutdownCtx, a.reloadDone, a.logger, "receiver.reloadSubscriber")
	safego.Await(shutdownCtx, a.nodeInvalidateDone, a.logger, "receiver.nodeInvalidateSubscriber")
	safego.Await(shutdownCtx, a.shipperDone, a.logger, "receiver.logShipper")
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

// nodeInvalidateHandler строит обработчик события инвалидации конфига узла (§57):
// резолвит team_id→slug и выселяет узел из кешей Receiver'а (L1 in-memory +
// Redis). Публикуется по team_id, поэтому slug резолвит Receiver — провал резолва
// slug на стороне Web не блокирует инвалидацию (§57.3).
func (a *App) nodeInvalidateHandler(inv nodecache.Invalidator) nodeevents.Handler {
	return func(ctx context.Context, ev nodeevents.Event) {
		slug, err := a.teamSlugByID(ctx, ev.TeamID)
		if err != nil {
			a.logger.Warn("node invalidation: resolve team slug failed",
				a.logger.Str("team_id", ev.TeamID), a.logger.Err(err))
			return
		}
		if err := inv.Invalidate(ctx, slug, ev.Path); err != nil {
			a.logger.Debug("node invalidation: evict failed",
				a.logger.Str("team", slug), a.logger.Str("path", ev.Path), a.logger.Err(err))
		}
		if ev.OldPath != "" {
			if err := inv.Invalidate(ctx, slug, ev.OldPath); err != nil {
				a.logger.Debug("node invalidation: evict old path failed",
					a.logger.Str("team", slug), a.logger.Str("path", ev.OldPath), a.logger.Err(err))
			}
		}
		a.logger.Debug("node invalidation applied",
			a.logger.Str("team", slug), a.logger.Str("path", ev.Path), a.logger.Str("old_path", ev.OldPath))
	}
}

// teamSlugByID резолвит slug команды по её id из PostgreSQL. Пустой id → default
// (событие без team_id трактуем как default-команда).
func (a *App) teamSlugByID(ctx context.Context, teamID string) (string, error) {
	if teamID == "" {
		return domain.DefaultTeamSlug, nil
	}
	var slug string
	if err := a.pg.QueryRow(ctx, "SELECT slug FROM teams WHERE id = $1", teamID).Scan(&slug); err != nil {
		return "", err
	}
	return slug, nil
}

// asyncEnvelopeOverheadRatio — во сколько раз async-конверт больше тела: JSON
// кодирует его в base64 (+33%), сверху идут заголовки/URL конверта. Служит
// только для стартовой проверки согласованности лимитов.
const asyncEnvelopeOverheadRatio = 1.4

// warnAsyncBodyLimitOverKafka предупреждает, если принятое по
// receiver.max_async_body_bytes тело заведомо не влезет в сообщение Kafka.
// Такое расхождение не ломает старт, но проявляется позже и неприятно: запрос
// принимается (клиент получил 200 и id), а публикация в топик падает —
// сообщение теряется. Гейта здесь нет намеренно: реальный потолок задаёт
// брокер, значения которого Receiver не знает, и падать на догадке нельзя.
func (a *App) warnAsyncBodyLimitOverKafka() {
	topicMax := a.cfg.Kafka.Topic.MaxMessageBytes
	asyncMax := a.cfg.Receiver.MaxAsyncBodyBytes
	if topicMax <= 0 || asyncMax <= 0 {
		return
	}
	need := int(float64(asyncMax) * asyncEnvelopeOverheadRatio)
	if need <= topicMax {
		a.logger.Debug("async body limit fits kafka message size",
			a.logger.Int("max_async_body_bytes", asyncMax),
			a.logger.Int("envelope_estimate", need),
			a.logger.Int("topic_max_message_bytes", topicMax))
		return
	}
	a.logger.Warn("receiver.max_async_body_bytes exceeds kafka.topic.max_message_bytes; large async requests will be accepted and then dropped on publish",
		a.logger.Int("max_async_body_bytes", asyncMax),
		a.logger.Int("envelope_estimate", need),
		a.logger.Int("topic_max_message_bytes", topicMax))
}
