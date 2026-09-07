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

	"nexus/internal/domain"
	"nexus/internal/platform/bootstrap"
	"nexus/internal/platform/circuitbreaker"
	chpf "nexus/internal/platform/clickhouse"
	"nexus/internal/platform/config"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/grpcsender"
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
	"nexus/internal/platform/redislock"
	"nexus/internal/platform/reloader"
	"nexus/internal/platform/requestid"
	"nexus/internal/platform/safego"
	sentrypf "nexus/internal/platform/sentry"
	grpcadapter "nexus/internal/sender/adapter/in/grpc"
	kafkaadapter "nexus/internal/sender/adapter/in/kafka"
	"nexus/internal/sender/adapter/out/chlog"
	"nexus/internal/sender/adapter/out/chlogretry"
	"nexus/internal/sender/adapter/out/httpclient"
	"nexus/internal/sender/adapter/out/maskpg"
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
	grpcHealth    *health.Server
	httpHealth    *healthcheck.Handler
	adminSrv      *http.Server
	chMgr         *chpf.Manager
	chWriter      *chlog.WriterManager
	producer      *kafkapf.Producer
	consumer      *kafkaadapter.ConsumerGroup
	retryConsumer *kafkaadapter.ChLogRetryConsumer // §38: дренаж nexus.logs.retry в CH (nil если retry выключен)
	dlqReproc     *kafkaadapter.DLQReprocessor     // §36: авто-репроцессор DLQ (nil если выключен)
	pausedSweep   *kafkaadapter.PausedSweeper      // §3.6: sweeper delay-топика paused-узлов (nil если выключен)
	otelShutdown  otelpf.ShutdownFunc

	// breakerPolicy — держатель ГЛОБАЛЬНОЙ политики защиты узла (§98.5).
	// Создаётся вместе с breaker'ом (то есть только при живом Redis) и
	// обновляется reloader'ом секции general без рестарта.
	breakerPolicy *circuitbreaker.PolicyProvider

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

	// §95: маскирование секретов в логах узлов. Провайдер держит атомарный набор
	// скомпилированных regexp (сидится из PG на старте, обновляется reload'ом
	// секции masking); reader читает справочник из PG. logMaskSeedDone — done
	// фонового ретрая сида (nil, если сид удался с первого раза).
	logMaskProvider *usecase.LogMaskProvider
	maskReader      *maskpg.Reader
	logMaskSeedDone <-chan struct{}

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
		metrics:      metrics.New("sender", metrics.WithInstance(identity.ID.String())),
		otelShutdown: otelShutdown,
		logCtl:       logCtl,
		identity:     identity,
	}
}

func (a *App) Start(ctx context.Context) error {
	// §95: провайдер маскирования создаём ДО chWriter (initClickHouseAndProducer
	// передаёт его в WriterManager). Набор шаблонов сидим из PG сразу после —
	// до первого лога, чтобы секрет не проскочил незамаскированным на старте.
	a.logMaskProvider = usecase.NewLogMaskProvider()
	a.maskReader = maskpg.New(a.pg, a.logger)

	chGuard := a.initClickHouseAndProducer()
	a.seedLogMasks(ctx)

	sendUC, breaker, nodeStatus := a.buildSendUsecase(ctx)

	// gRPC adapter для sync.
	grpcSvc := grpcadapter.NewServer(sendUC, a.metrics, nodeStatus, a.logger)

	nodeReader := nodepg.New(a.pg, a.cipher, a.logger)
	a.ensureCHSchema(ctx, nodeReader, chGuard)
	a.startAsyncPipeline(ctx, nodeReader, sendUC, breaker, nodeStatus)
	a.startBackgroundJobs(ctx, nodeReader, chGuard)

	return a.serveEndpoints(ctx, grpcSvc)
}

// initClickHouseAndProducer поднимает общие для всех потребителей ресурсы:
// менеджер соединения ClickHouse (владелец conn при hot-reload), гейт владения
// таблицами и Kafka-продьюсер. Возвращает гейт — он нужен и обслуживанию схемы,
// и housekeeping'у.
func (a *App) initClickHouseAndProducer() *chpf.Guard {
	// ClickHouse Manager — владелец соединения для hot-reload (§8.4 / Phase 6.3.2.5).
	// Все consumers (chWriter, CHHousekeeping, HealthChecker) идут через него,
	// а не через raw a.ch, чтобы при reload swap conn'а был для них прозрачным.
	a.chMgr = chpf.NewManager(a.ch, chpf.New, &a.cfg.ClickHouse, a.logger)
	// §70.3: гейт владения ClickHouse-БД. Sender ничего не захватывает и не
	// усыновляет (он может стартовать раньше Web и «застолбить» БД, которую Web
	// ещё не связал с командой) — только отличает свои таблицы от чужих.
	chGuard := chpf.NewGuard(a.chMgr, a.identity.ID, a.logger)
	a.chMgr.OnReload(func() {
		chGuard.Invalidate()
		// Симметрично Web: смена адреса ClickHouse из UI может увести ноду на
		// сервер без её маркеров, и тогда уборка по retention начнёт молча
		// пропускать таблицы. Без этой строки причину пришлось бы искать по
		// косвенным признакам.
		a.logger.Warn("clickhouse connection reloaded: ownership verdicts dropped, "+
			"they will be re-checked on the next operation",
			a.logger.Str("instance", a.identity.ID.String()))
	})
	// §38: Kafka-продьюсер нужен и async/DLQ, и retrier'у проваленных CH-батчей —
	// создаём ДО chWriter (раньше создавался ниже, в секции async-consumer).
	a.producer = kafkapf.NewProducer(a.cfg, kafkapf.WithMetrics(a.metrics))
	var chRetrier chlog.BatchRetrier
	if a.cfg.Kafka.RetryTopic != "" {
		chRetrier = chlogretry.New(a.producer, a.cfg.Kafka.RetryTopic, a.cfg.Kafka.Topic.MaxMessageBytes, a.logger)
	}
	a.chWriter = chlog.NewManagerWithRetrier(a.chMgr, &a.cfg.ClickHouse, chRetrier, a.metrics, a.logMaskProvider, a.logger)
	return chGuard
}

// applyLogMasks читает активные шаблоны маскирования (§95) из PG и атомарно
// заменяет набор в провайдере. Единая точка для сида, фонового ретрая и reload.
func (a *App) applyLogMasks(ctx context.Context) (applied, skipped int, err error) {
	patterns, err := a.maskReader.Load(ctx)
	if err != nil {
		return 0, 0, err
	}
	applied, skipped = a.logMaskProvider.Set(patterns)
	return applied, skipped, nil
}

// seedLogMasks заполняет провайдер маскирования из PG на старте — ДО первого
// лога узла. При ошибке старт НЕ блокируется: web накатывает миграцию 0041
// параллельно со стартом Sender, и таблицы может ещё не быть (инцидент kz
// 24.08.2026 — seed падал на 42P01 «relation log_mask_patterns does not exist»,
// и маскирование молча оставалось выключенным до ручного reload настроек).
// Поэтому при неудаче дочитываем в фоне с backoff, пока таблица не появится.
func (a *App) seedLogMasks(ctx context.Context) {
	applied, skipped, err := a.applyLogMasks(ctx)
	if err == nil {
		a.logger.Info("§95 log mask patterns loaded",
			a.logger.Int("applied", applied),
			a.logger.Int("skipped", skipped))
		return
	}
	a.logger.Warn("§95 seed log masks failed; retrying in background until the table appears",
		a.logger.Err(err))
	a.logMaskSeedDone = safego.Go(a.logger, "sender.seedLogMasksRetry", func() {
		a.retryLoadLogMasks(ctx)
	})
}

// retryLoadLogMasks дочитывает шаблоны в фоне, пока таблица не появится (миграция
// доедет) или ctx не отменят. Не бесконечно: после исчерпания попыток
// маскирование остаётся выключенным до reload настроек (как раньше), но типовая
// гонка «Sender стартовал раньше миграции» закрывается сама за секунды.
func (a *App) retryLoadLogMasks(ctx context.Context) {
	ok := retryUntil(ctx, 3*time.Second, 20, func(ctx context.Context) error {
		applied, skipped, err := a.applyLogMasks(ctx)
		if err != nil {
			a.logger.Debug("§95 log masks still unavailable, will retry", a.logger.Err(err))
			return err
		}
		a.logger.Info("§95 log mask patterns loaded after retry",
			a.logger.Int("applied", applied),
			a.logger.Int("skipped", skipped))
		return nil
	})
	if !ok {
		a.logger.Warn("§95 log masks unavailable after retries; masking stays off until a settings reload")
	}
}

// retryUntil вызывает attempt с интервалом interval до первого успеха (nil),
// исчерпания maxAttempts или отмены ctx. true — успех. Первая попытка тоже ждёт
// interval: вызывающий уже сделал немедленную попытку до фонового ретрая.
func retryUntil(ctx context.Context, interval time.Duration, maxAttempts int, attempt func(context.Context) error) bool {
	t := time.NewTicker(interval)
	defer t.Stop()
	for range maxAttempts {
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
			if attempt(ctx) == nil {
				return true
			}
		}
	}
	return false
}

// buildSendUsecase собирает доставку: HTTP-клиент с транспортным лимитом,
// circuit breaker, reverse-DNS резолвер клиента и писатель статуса узла.
// Возвращает usecase, read-only инспектор breaker'а (нужен репроцессору DLQ)
// и writer статуса (нужен gRPC-адаптеру).
func (a *App) buildSendUsecase(ctx context.Context) (*usecase.SendUsecase, usecase.BreakerInspector, usecase.NodeStatusWriter) {
	// §43-rev: транспортный лимит тела ОТВЕТА = gRPC-потолок минус запас под
	// envelope SendResponse (заголовки/статус). httpclient оборвёт чтение на нём
	// (memory-safe), Send отдаст клиенту 502. Один источник — config.
	respLimit := a.cfg.Sender.GRPCMaxMessageBytes - grpcResponseEnvelopeReserve
	httpc := httpclient.New(&a.cfg.Sender.HTTPClient, a.logger, respLimit)

	// Circuit breaker per node. §81.3: политика больше не литерал — глобальные
	// значения приходят из конфигурации, узел может их переопределить (тогда
	// они приезжают в самом вызове, см. SendInput.Breaker*).
	var (
		cb      usecase.CircuitBreaker
		breaker usecase.BreakerInspector // §36: read-only IsOpen для репроцессора DLQ
	)
	if a.redis != nil {
		// §98.5: глобальная политика подменяема на ходу — стартовое значение из
		// конфигурации, дальше её задаёт администратор в интерфейсе, и оно
		// приезжает событием настроек (секция general). Провайдер держится в
		// поле приложения: его же обновляет reloader, поднимаемый ниже.
		a.breakerPolicy = circuitbreaker.NewPolicyProvider(domain.BreakerPolicy{
			Threshold: a.cfg.Sender.CircuitBreaker.Threshold,
			Cooldown:  time.Duration(a.cfg.Sender.CircuitBreaker.CooldownSec) * time.Second,
		})
		b := circuitbreaker.NewWithPolicy(a.redis, a.breakerPolicy)
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
		nodeStatus = nodestatus.NewRedisWriter(a.redis, a.cfg.Sender.NodeDownThreshold, a.logger)
	}

	return sendUC, breaker, nodeStatus
}

// ensureCHSchema приводит существующие лог-таблицы к текущей схеме ДО старта
// consumer'ов: иначе первый же INSERT по новой схеме упадёт на старой таблице.
// Все операции идемпотентны (ADD COLUMN IF NOT EXISTS).
func (a *App) ensureCHSchema(ctx context.Context, nodeReader *nodepg.Reader, chGuard *chpf.Guard) {
	if a.chMgr == nil {
		return
	}
	// §37: миграция CH-таблиц — добавить колонку node_id ДО старта consumer'а и
	// репроцессора (иначе INSERT по новой схеме упадёт на старых таблицах).
	// Идемпотентно (ALTER … IF NOT EXISTS); новые таблицы — из шаблона.
	tables, err := nodeReader.ListClickHouseTables(ctx)
	if err != nil {
		a.logger.Warn("§37 ensure node_id: list tables failed", a.logger.Err(err))
		return
	}
	// §70.4: чужие таблицы из обслуживания схемы исключаются (ADD COLUMN
	// фиксирует порядок колонок, backfill мутирует все строки).
	tables = chGuard.FilterManagedTables(ctx, tables)
	chpf.EnsureNodeIDColumn(ctx, a.chMgr.Conn(), tables, a.logger)
	chpf.EnsureHTTPMethodColumn(ctx, a.chMgr.Conn(), tables, a.logger) // §39
	chpf.EnsureClientHostColumn(ctx, a.chMgr.Conn(), tables, a.logger) // §67
	chpf.EnsureBodySizeColumns(ctx, a.chMgr.Conn(), tables, a.logger)  // §42-доп
	chpf.BackfillBodySizes(ctx, a.chMgr.Conn(), tables, a.logger)      // §42-доп
}

// startAsyncPipeline поднимает всё, что обрабатывает очереди: основной
// async-consumer, retry-консьюмер проваленных CH-батчей (§38), авто-репроцессор
// DLQ (§36) и sweeper delay-топика paused-узлов (§3.6).
func (a *App) startAsyncPipeline(
	ctx context.Context,
	nodeReader *nodepg.Reader,
	sendUC *usecase.SendUsecase,
	breaker usecase.BreakerInspector,
	nodeStatus usecase.NodeStatusWriter,
) {
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

}

// startBackgroundJobs запускает периодические задачи, не связанные с очередями:
// уборку партиций ClickHouse по retention (§4.3), репортер лага Kafka (§6) и
// подписчиков hot-reload вместе с шиппером служебных логов (§14.5/§8.4/§51).
func (a *App) startBackgroundJobs(ctx context.Context, nodeReader *nodepg.Reader, chGuard *chpf.Guard) {
	// CH partition-drop housekeeping (§4.3 ТЗ): фоновый цикл раз в сутки.
	// §93.7: с двумя репликами суточный тикер срабатывает дважды — лок
	// оставляет уборку одной из них.
	hk := usecase.NewCHHousekeeping(a.chMgr, nodeReader, a.logger).
		WithOwnership(chGuard).
		WithCycleLock(redislock.New(a.redis, "sender-ch-housekeeping"))
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
			bootstrap.SentryReloader(a.pg, a.cfg, a.cfg.Build.ProjectName, a.cfg.Build.Version, a.cipher, a.logger))
		reloadSub.Register(reloader.SectionClickHouse,
			bootstrap.ClickHouseReloader(a.pg, a.cfg, a.chMgr,
				[]bootstrap.WriterReloader{a.chWriter}, a.cipher, a.logger))
		// §51: runtime-уровень логов из app_settings.logging.level (+ сид старта).
		applyLogLevel := bootstrap.LogLevelReloader(a.pg, a.logCtl, a.cipher, a.logger)
		if err := applyLogLevel(ctx); err != nil {
			a.logger.Warn("seed log level from app_settings failed; using yaml level", a.logger.Err(err))
		}
		reloadSub.Register(reloader.SectionLogging, applyLogLevel)
		// §98.5: глобальная политика защиты узла из app_settings.general
		// (+ сид старта). Без Redis breaker не работает вовсе, поэтому и
		// провайдер существует только внутри этой ветки.
		if a.breakerPolicy != nil {
			applyBreaker := bootstrap.BreakerPolicyReloader(a.pg, a.breakerPolicy,
				a.cfg.Sender.CircuitBreaker.Threshold, a.cfg.Sender.CircuitBreaker.CooldownSec,
				a.cipher, a.logger)
			// Ошибка сида не валит старт: до приезда настроек действует
			// конфигурация — то же поведение, что было до §98.
			if err := applyBreaker(ctx); err != nil {
				a.logger.Warn("seed circuit breaker policy from app_settings failed; using yaml values",
					a.logger.Err(err))
			}
			reloadSub.Register(reloader.SectionGeneral, applyBreaker)
		}
		// §95: справочник маскирования логов изменился в UI → перечитать из PG и
		// пересобрать набор regexp без рестарта. Сид уже сделан в Start
		// (seedLogMasks) — здесь только горячее обновление.
		reloadSub.Register(reloader.SectionMasking, func(ctx context.Context) error {
			applied, skipped, err := a.applyLogMasks(ctx)
			if err != nil {
				return fmt.Errorf("§95 reload log masks: %w", err)
			}
			a.logger.Info("§95 log mask patterns reloaded",
				a.logger.Int("applied", applied),
				a.logger.Int("skipped", skipped))
			return nil
		})
		a.reloadDone = safego.Go(a.logger, "sender.reloadSubscriber", func() {
			reloadSub.Run(ctx)
		})
	}
}

// serveEndpoints поднимает gRPC и админ-HTTP и блокируется до остановки
// приложения или фатальной ошибки любого из слушателей.
func (a *App) serveEndpoints(ctx context.Context, grpcSvc *grpcadapter.Server) error {
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

// GRPCServerOptions — полный набор опций gRPC-сервера Sender'а.
//
// Вынесен из startGRPC и экспортирован, чтобы тест поднимал сервер ровно теми
// же опциями, что боевой процесс. Иначе регресс §82.1 не ловился бы: тест со
// «своим» grpc.NewServer остался бы зелёным даже после удаления политики
// keepalive из боевой сборки — а именно её отсутствие и рвало sync-вызовы.
func GRPCServerOptions(cfg *config.SenderSection) []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.MaxConcurrentStreams(cfg.GRPCMaxConcurrentStreams),
		// §82.1: без объявленной политики grpc-go считает штатные keepalive-ping'и
		// клиентов (каждые 30 с) нарушением и на третьем рвёт соединение вместе с
		// идущим по нему sync-вызовом — потолок запроса 4×keepalive_time_sec.
		// Обе половины контракта живут в grpcsender, см. ServerKeepalivePolicy.
		grpcsender.ServerKeepalivePolicy(&cfg.GRPCKeepalive),
		// §42: лимит размера сообщения (оба направления). Дефолт gRPC recv 4 МиБ
		// мал — тело запроса (до receiver.max_body_bytes) и тело ответа апстрима
		// (десятки МБ) иначе режутся ResourceExhausted.
		grpc.MaxRecvMsgSize(cfg.GRPCMaxMessageBytes),
		grpc.MaxSendMsgSize(cfg.GRPCMaxMessageBytes),
		// OTel: extract traceparent из incoming metadata + server-span
		// вокруг каждого unary-вызова (§16 ТЗ, Phase 8.3).
		grpc.UnaryInterceptor(otelpf.UnaryServerInterceptor()),
	}
}

func (a *App) startGRPC(svc *grpcadapter.Server) error {
	lis, err := net.Listen("tcp", a.cfg.Sender.GRPCAddr)
	if err != nil {
		return fmt.Errorf("sender grpc listen %s: %w", a.cfg.Sender.GRPCAddr, err)
	}

	a.grpcSrv = grpc.NewServer(GRPCServerOptions(&a.cfg.Sender)...)

	senderv1.RegisterSenderServiceServer(a.grpcSrv, svc)

	hsrv := health.NewServer()
	hsrv.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	hsrv.SetServingStatus("nexus.sender.v1.SenderService", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(a.grpcSrv, hsrv)
	// §93.5: на остановке Stop переводит этот же сервер в NOT_SERVING, и клиенты
	// с healthCheckConfig (Receiver, platform/grpcsender) убирают реплику из
	// round_robin ДО того, как gRPC-сервер начнёт отказывать в соединениях.
	a.grpcHealth = hsrv
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
	a.httpHealth = hc
	hc.Register(r)
	r.GET("/metrics", gin.WrapH(a.metrics.Handler()))

	// Админ-порт отдаёт только /health, /ready и /metrics — ни длинных ответов,
	// ни стримов здесь нет, поэтому таймауты фиксированные (в отличие от Web,
	// где WriteTimeout невозможен). IdleTimeout закрывает простаивающие
	// keep-alive соединения scrape'а Prometheus.
	a.adminSrv = &http.Server{
		Addr:              a.cfg.Sender.AdminHTTPAddr,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
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

// Stop — порядок остановки существенен и зафиксирован в шагах ниже: сперва
// перестаём читать очереди, потом гасим приём gRPC, потом дожидаемся фоновых
// горутин и только затем закрываем ресурсы, которыми они пользуются.
// drain выводит реплику из ротации перед остановкой (§93.5).
//
// У Sender'а два канала, по которым его находят, и оба надо погасить ДО
// остановки: gRPC-health (по нему Receiver балансирует вызовы, §93.4) и
// HTTP /ready на админ-порту (его смотрят мониторинг и §73). Порядок именно
// такой: сперва перестаём быть кандидатом на новые вызовы, потом ждём, потом
// останавливаем серверы.
func (a *App) drain(ctx context.Context) {
	// NOT_SERVING выставляем всегда, даже когда пауза выключена: это стоит
	// доли миллисекунды, а клиенту даёт шанс не выбрать эту реплику для
	// следующего вызова. Shutdown() переводит ВСЕ зарегистрированные сервисы в
	// NOT_SERVING и запрещает возврат в SERVING — ровно семантика остановки.
	if a.grpcHealth != nil {
		a.grpcHealth.Shutdown()
	}

	pause := a.cfg.Shutdown.Drain()
	if pause <= 0 || a.httpHealth == nil {
		return
	}
	a.logger.Info("sender draining before shutdown",
		a.logger.Str("pause", pause.String()))
	healthcheck.Drain(ctx, a.httpHealth, pause)
}

func (a *App) Stop(ctx context.Context) error {
	a.logger.Info("sender shutting down")

	a.drain(ctx)
	a.stopQueueConsumers()
	a.stopGRPC(ctx)
	a.awaitBackgroundJobs(ctx)
	a.closeStorageResources(ctx)

	if a.adminSrv != nil {
		shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := a.adminSrv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("sender admin shutdown: %w", err)
		}
	}
	return a.shutdownTelemetry(ctx)
}

// stopQueueConsumers прекращает чтение всех Kafka-очередей.
func (a *App) stopQueueConsumers() {
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
}

// stopGRPC гасит gRPC-сервер graceful, но не дольше shutdown.timeout_sec (или
// до отмены ctx): зависший клиентский стрим не должен удерживать остановку
// сервиса. Тот же потолок применяется к HTTP-серверам Receiver и Web — выкат
// не должен зависеть от того, по какому протоколу пришёл долгий запрос.
func (a *App) stopGRPC(ctx context.Context) {
	if a.grpcSrv == nil {
		return
	}
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
	case <-time.After(a.cfg.Shutdown.Timeout()):
		a.grpcSrv.Stop()
	}
}

// awaitBackgroundJobs дожидается фоновых горутин: они должны завершиться до
// закрытия CH/Kafka-ресурсов, которыми пользуются. Контекст им уже отменён
// runner'ом — здесь только ожидание выхода.
func (a *App) awaitBackgroundJobs(ctx context.Context) {
	awaitCtx, awaitCancel := context.WithTimeout(ctx, 10*time.Second)
	defer awaitCancel()

	safego.Await(awaitCtx, a.housekeepingDone, a.logger, "sender.chHousekeeping")
	safego.Await(awaitCtx, a.kafkaLagDone, a.logger, "sender.reportKafkaLag")
	safego.Await(awaitCtx, a.reloadDone, a.logger, "sender.reloadSubscriber")
	safego.Await(awaitCtx, a.dlqReprocDone, a.logger, "sender.dlqReprocessor")
	safego.Await(awaitCtx, a.pausedSweepDone, a.logger, "sender.pausedSweep")
	safego.Await(awaitCtx, a.shipperDone, a.logger, "sender.logShipper")
	safego.Await(awaitCtx, a.logMaskSeedDone, a.logger, "sender.seedLogMasksRetry") // §95: nil, если сид удался сразу
}

// closeStorageResources сбрасывает буфер логов в ClickHouse и закрывает
// соединения. Порядок важен: сначала flush накопленных батчей, потом закрытие.
func (a *App) closeStorageResources(ctx context.Context) {
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
}

// shutdownTelemetry гасит экспортёр трассировки: последний шаг остановки,
// ошибка не фатальна (сервис уже завершается, терять из-за неё код выхода незачем).
func (a *App) shutdownTelemetry(ctx context.Context) error {
	if a.otelShutdown == nil {
		return nil
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := a.otelShutdown(shutdownCtx); err != nil {
		a.logger.Warn("otel tracer shutdown returned error", a.logger.Err(err))
	}
	return nil
}
