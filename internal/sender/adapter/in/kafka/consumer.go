// Package kafka — Sender-сторона async-обработки. Запускает N consumer-
// горутин (= partitions), каждая блокирующе читает FetchMessage и
// делегирует Handle в usecase. Commit только после Ack/DLQed.
package kafka

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	kafka "github.com/segmentio/kafka-go"

	"nexus/internal/platform/config"
	kafkapf "nexus/internal/platform/kafka"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/metrics"
	"nexus/internal/platform/safego"
	"nexus/internal/sender/usecase"
)

// headersToMap сворачивает Kafka-заголовки в map[string]string. Kafka в
// принципе допускает несколько значений на один ключ, но для наших служебных
// заголовков (OTel-propagator, id/reason/attempts DLQ) это исключено — берём
// первое значение. Общий helper основного consumer'а и DLQ-sweeper'а.
func headersToMap(hs []kafka.Header) map[string]string {
	m := make(map[string]string, len(hs))
	for _, h := range hs {
		if _, exists := m[h.Key]; !exists {
			m[h.Key] = string(h.Value)
		}
	}
	return m
}

// asyncRetryPause — пауза между повторами обработки ОДНОГО сообщения при
// HandleRetry. Основную выдержку для paused-узлов задаёт usecase
// (pausedRetryAfter, §3.6); здесь — защита от busy-loop в остальных Retry-ветках
// (недоступен PostgreSQL при чтении узла, не записался DLQ).
const asyncRetryPause = time.Second

// messageCommitter — минимальный порт commit'а offset'а одного сообщения.
// Реализуется *kafkapf.Consumer. Выделен, чтобы per-message логика
// (handleMessage) тестировалась юнитами без реальной Kafka.
type messageCommitter interface {
	Commit(ctx context.Context, msg kafka.Message) error
}

// ConsumerGroup — пул из cfg.Kafka.Consumer.Instances consumer-горутин.
type ConsumerGroup struct {
	cfg       *config.Config
	processor messageProcessor
	logger    logging.Logger
	topic     string
	metrics   *metrics.Metrics // §31: nil-safe; nil → in-flight не пишется
	stall     StallGuard       // §113.6: сторож зависшего reader'а

	// newReader строит reader экземпляра по client.id; по умолчанию — настоящий
	// reader Kafka, тесты подставляют свой (withReaderFactory).
	newReader readerFactory

	slots  []*readerSlot
	cancel context.CancelFunc // останавливает ЧТЕНИЕ
	// deliveries — контекст ДОСТАВКИ (§118.8): его отменяет только Shutdown по
	// потолку или Stop, а не остановка чтения.
	deliveries deliveryScope
	// inFlight — сообщения, прочитанные и ещё не обработанные (для лога дренажа).
	inFlight atomic.Int64
	wg       sync.WaitGroup
}

// ConsumerOption — функциональная опция конструктора ConsumerGroup.
type ConsumerOption func(*ConsumerGroup)

// WithMetrics включает метрику nexus_kafka_in_flight (§31).
func WithMetrics(m *metrics.Metrics) ConsumerOption {
	return func(g *ConsumerGroup) { g.metrics = m }
}

// WithStallGuard включает сторожа зависшего reader'а (§113.6).
func WithStallGuard(guard StallGuard) ConsumerOption {
	return func(g *ConsumerGroup) { g.stall = guard }
}

// withReaderFactory подменяет фабрику reader'ов — для юнит-тестов Start и
// Shutdown без настоящей Kafka.
func withReaderFactory(f readerFactory) ConsumerOption {
	return func(g *ConsumerGroup) { g.newReader = f }
}

func NewConsumerGroup(
	cfg *config.Config,
	topic string,
	processor messageProcessor,
	logger logging.Logger,
	opts ...ConsumerOption,
) *ConsumerGroup {
	g := &ConsumerGroup{
		cfg:       cfg,
		processor: processor,
		logger:    logger,
		topic:     topic,
	}
	g.newReader = func(clientID string) slotReader {
		return kafkapf.NewConsumer(g.cfg, g.topic, kafkapf.WithClientID(clientID))
	}
	for _, o := range opts {
		o(g)
	}
	return g
}

// Start запускает горутины. Возвращается сразу — горутины крутятся в фоне.
// Останавливаются через Stop().
//
// Горутины завязаны на собственный производный контекст, который отменяет Stop.
// Иначе после Close() reader'а FetchMessage возвращает не context.Canceled, а
// «reader closed» — цикл уходит в continue и крутит busy-loop до отмены внешнего
// ctx (как у ChLogRetryConsumer, §38).
//
// §118.8: чтение и доставка — на разных контекстах. ctx (контекст приложения)
// останавливает чтение: runner отменяет его первым. Доставка и commit идут на
// отвязанном контексте, который отменяет только Shutdown по потолку (или Stop):
// иначе остановка рвала HTTP-запрос к получателю посреди отправки и рождала
// повтор после старта.
func (g *ConsumerGroup) Start(ctx context.Context) {
	cctx, cancel := context.WithCancel(ctx)
	g.cancel = cancel
	dctx, _ := g.deliveries.begin(ctx) // освобождает Abort в Shutdown/Stop
	group := g.cfg.Kafka.ConsumerGroup
	g.slots = newSlots(g.cfg.Kafka.Consumer.Instances, group, g.stall, g.newReader, g.logger)
	for i, s := range g.slots {
		g.wg.Add(1)
		go g.runOne(cctx, dctx, s, i)
	}
	startStallWatchdog(cctx, &g.wg, group, g.topic, g.stall, g.slots, g.logger)
}

// runOne — цикл экземпляра: ctx останавливает чтение, dctx — доставку (§118.8).
func (g *ConsumerGroup) runOne(ctx, dctx context.Context, c slotReader, idx int) {
	defer g.wg.Done()
	defer safego.Recover(g.logger, "sender.kafkaConsumer")
	g.logger.Info("async consumer started",
		g.logger.Str("topic", g.topic),
		g.logger.Int("idx", idx))

	for {
		if ctx.Err() != nil {
			return
		}
		msg, err := c.FetchMessage(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return
			}
			g.logger.Warn("fetch message failed",
				g.logger.Str("topic", g.topic),
				g.logger.Err(err))
			continue
		}

		g.handleMessage(ctx, dctx, c, msg)
	}
}

// handleMessage обрабатывает одно сообщение и решает судьбу offset'а.
// Выделен из runOne, чтобы маппинг HandleResult → Commit покрывался юнитами
// без реальной Kafka (fetch-цикл остаётся на integration-тестах).
//
// ctx — остановка чтения (прерывает паузы повторов на месте), dctx — доставка:
// обработка и commit идут на нём и доигрываются при остановке (§118.8).
func (g *ConsumerGroup) handleMessage(ctx, dctx context.Context, c messageCommitter, msg kafka.Message) {
	// §31: сообщение прочитано, но ещё не закоммичено — «в полёте».
	g.incInFlight()
	g.inFlight.Add(1)

	// Извлекаем Kafka headers в map[string]string для OTel-propagator'а
	// (Phase 8.4). Несколько значений на ключ Kafka в принципе допускает,
	// но для propagator-keys это исключено — берём первое.
	hdrs := headersToMap(msg.Headers)
	// §51.9: partition/offset доступны только здесь (usecase их не видит) —
	// на debug видна привязка сообщения к позиции в топике.
	g.logger.Debug("kafka: message fetched",
		g.logger.Str("topic", g.topic),
		g.logger.Int("partition", msg.Partition),
		g.logger.Any("offset", msg.Offset),
		g.logger.Str("key", string(msg.Key)),
		g.logger.Int("size", len(msg.Value)))
	done := g.processWithRetry(ctx, dctx, msg, hdrs)
	g.decInFlight() // обработка завершена (committed/retry-left)
	g.inFlight.Add(-1)
	if !done {
		// Прерваны на shutdown — offset не коммитим: сообщение передоставится
		// после рестарта/ребаланса.
		return
	}
	if err := c.Commit(dctx, msg); err != nil {
		g.logger.ErrorWithOp("commit offset failed", err, "kafka.consumer.commit",
			g.logger.Str("topic", g.topic),
			g.logger.Int("partition", msg.Partition))
	}
}

// processWithRetry переобрабатывает ОДНО И ТО ЖЕ сообщение, пока Handle не
// вернёт не-Retry (Ack/DLQed/Requeued). Возвращает false, если прерван ctx.
//
// Почему не «пропустить и вернуться к сообщению позже» (как было до §3.6-fix):
// kafka-go FetchMessage без commit'а двигает внутренний курсор и НЕ
// передоставляет сообщение на том же reader'е — только при rebalance/restart.
// Пропуск означал, что сообщение paused-узла зависало до перезапуска Sender'а, а
// commit СЛЕДУЮЩЕГО сообщения той же партиции прокатывал committed offset мимо
// него — и сообщение терялось навсегда. Retry-in-place сохраняет и сообщение, и
// порядок партиции (ценой head-of-line blocking). Тот же приём, что у
// ChLogRetryConsumer (§38) и проходных reader'ов (§113.2); пауза здесь
// постоянная — основной поток не должен ждать дольше секунды после
// восстановления зависимости.
//
// Пауза между повторами ждёт на ctx (остановка чтения прерывает повторы), сама
// обработка — на dctx (§118.8): начатая доставка доигрывается.
func (g *ConsumerGroup) processWithRetry(ctx, dctx context.Context, msg kafka.Message, hdrs map[string]string) bool {
	retry := inPlaceRetry{min: asyncRetryPause, max: asyncRetryPause}
	return retry.run(ctx, func() bool {
		return g.processor.Handle(dctx, msg.Value, hdrs) != usecase.HandleRetry
	}, func(int, time.Duration) {
		// §51.9: повторная обработка того же offset'а — иначе «залипание»
		// партиции на одном сообщении выглядит как молчание consumer'а.
		g.logger.Debug("kafka: retrying message in place",
			g.logger.Str("topic", g.topic),
			g.logger.Int("partition", msg.Partition),
			g.logger.Any("offset", msg.Offset))
	})
}

// incInFlight/decInFlight — nil-safe изменение gauge сообщений «в полёте».
func (g *ConsumerGroup) incInFlight() {
	if g.metrics != nil {
		g.metrics.KafkaInFlight.WithLabelValues("sender").Inc()
	}
}

func (g *ConsumerGroup) decInFlight() {
	if g.metrics != nil {
		g.metrics.KafkaInFlight.WithLabelValues("sender").Dec()
	}
}

// Stop немедленно завершает все consumer-горутины: останавливает чтение,
// ОБРЫВАЕТ доставки в полёте, закрывает reader'ы и дожидается горутин. Это
// остановка без дренажа (тесты «падения», аварийный путь); штатная остановка
// сервиса — Shutdown.
func (g *ConsumerGroup) Stop() {
	if g.cancel != nil {
		g.cancel()
	}
	g.deliveries.Abort()
	closeSlots(g.slots)
	g.wg.Wait()
}

// Shutdown — мягкая остановка (§118.8): перестать читать, доиграть доставки в
// полёте не дольше ctx, по истечении — оборвать их (прежнее поведение) и
// закрыть reader'ы.
//
// Reader'ы закрываются ПОСЛЕ выхода горутин: commit доставленного сообщения
// идёт через reader, и закрыть его раньше значило бы потерять commit и вернуть
// повтор, ради устранения которого дренаж и сделан.
func (g *ConsumerGroup) Shutdown(ctx context.Context) {
	if g.cancel != nil {
		g.cancel()
	}
	start := time.Now()
	if n := g.inFlight.Load(); n > 0 {
		g.logger.Info("async consumer: draining in-flight deliveries",
			g.logger.Str("topic", g.topic),
			g.logger.Int("in_flight", int(n)))
	}
	if !waitGroupCtx(ctx, &g.wg, g.logger) {
		g.logger.Warn("async consumer: drain ceiling reached, aborting in-flight deliveries",
			g.logger.Str("topic", g.topic),
			g.logger.Int("in_flight", int(g.inFlight.Load())))
		g.deliveries.Abort()
		graceCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drainAbortGrace)
		_ = waitGroupCtx(graceCtx, &g.wg, g.logger)
		cancel()
	}
	g.deliveries.Abort() // освобождает контекст доставки
	closeSlots(g.slots)
	g.wg.Wait()
	g.logger.Debug("async consumer: drain finished",
		g.logger.Str("topic", g.topic),
		g.logger.Int("duration_ms", int(time.Since(start).Milliseconds())))
}
