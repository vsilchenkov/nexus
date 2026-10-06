package kafka

import (
	"context"
	"time"

	kafka "github.com/segmentio/kafka-go"

	"nexus/internal/platform/config"
	kafkapf "nexus/internal/platform/kafka"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/metrics"
	"nexus/internal/sender/usecase"
)

// sweepPlanner — решение «проход или пропуск» на тике и итог прохода.
// Реализуется *usecase.PausedSweepPlanner.
type sweepPlanner interface {
	Decide(ctx context.Context) usecase.PausedSweepDecision
	PassDone(processed int)
}

// sweepConsumer — то, что sweeper'у нужно от Kafka-reader'а. Реализуется
// *kafkapf.Consumer; объявлен здесь (CLAUDE.md §3), чтобы логика прохода
// покрывалась юнитами без брокера.
type sweepConsumer interface {
	FetchMessage(ctx context.Context) (kafka.Message, error)
	Commit(ctx context.Context, msg kafka.Message) error
	Close() error
	Stats() kafka.ReaderStats
}

// PausedSweeper — периодический sweeper над delay-топиком nexus.async.paused
// (§3.6). Отдельная consumer-группа "<group>-paused" читает топик независимо от
// основного async-consumer'а.
//
// Зачем отдельный топик и sweeper. Сообщение узла на паузе нельзя удерживать в
// основном топике: kafka-go не передоставляет незакоммиченное сообщение, а
// коммит соседнего по партиции сообщения прокатил бы offset мимо него (потеря,
// см. §4.37 IMPLEMENTATION.md). Удержание же блокирует всю партицию, в которой
// по хешу node_path лежат ДРУГИЕ узлы. Поэтому основной consumer переиздаёт
// сообщение сюда, а этот sweeper обслуживает бэклог отдельно.
//
// Почему проходами, а не непрерывным consumer'ом: пауза после каждого сообщения
// усыпила бы горутину партиции и вернула бы head-of-line blocking внутрь
// delay-топика — узел, снявший паузу, ждал бы бэклог долго-paused соседа.
// Проход идёт на полной скорости, пауза — между проходами (interval), она же
// задаёт максимальную задержку доставки после снятия паузы.
//
// §113.3: тик — момент решения, а не проход. Проход переписывает сообщения
// узлов, всё ещё стоящих на паузе, поэтому планировщик пускает его только после
// события (узлы, отмена, старт, страховка). Без планировщика (nil) — проход на
// каждом тике, как до §113.
type PausedSweeper struct {
	consumer     sweepConsumer
	processor    messageProcessor
	interval     time.Duration
	maxScan      int
	fetchTimeout time.Duration
	retry        inPlaceRetry     // §113.2: повтор сообщения на месте при HandleRetry
	metrics      *metrics.Metrics // nil-safe
	logger       logging.Logger
	topic        string
	group        string
	planner      sweepPlanner // §113.3: nil — проход на каждом тике
}

// PausedSweeperOption — функциональная опция NewPausedSweeper.
type PausedSweeperOption func(*PausedSweeper)

// WithSweepPlanner включает проход по событию (§113.3).
func WithSweepPlanner(p sweepPlanner) PausedSweeperOption {
	return func(s *PausedSweeper) { s.planner = p }
}

// NewPausedSweeper создаёт sweeper. interval/maxScan ≤ 0 → дефолты (30с / 1000).
func NewPausedSweeper(
	cfg *config.Config,
	processor messageProcessor,
	interval time.Duration,
	maxScan int,
	m *metrics.Metrics,
	logger logging.Logger,
	opts ...PausedSweeperOption,
) *PausedSweeper {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if maxScan <= 0 {
		maxScan = 1000
	}
	group := cfg.Kafka.PausedGroup()
	s := &PausedSweeper{
		consumer:     kafkapf.NewConsumerWithGroup(cfg, cfg.Kafka.PausedTopic, group),
		processor:    processor,
		interval:     interval,
		maxScan:      maxScan,
		fetchTimeout: 2 * time.Second,
		retry:        inPlaceRetry{min: passRetryMin, max: passRetryMax},
		metrics:      m,
		logger:       logger,
		topic:        cfg.Kafka.PausedTopic,
		group:        group,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Run — блокирующий периодический цикл: первый проход сразу, далее раз в
// interval. Завершается при отмене ctx. Запускается через safego.Go в app.go.
func (s *PausedSweeper) Run(ctx context.Context) {
	s.logger.Info("paused sweeper started",
		s.logger.Str("topic", s.topic),
		s.logger.Str("group", s.group),
		s.logger.Str("interval", s.interval.String()),
		s.logger.Int("max_scan", s.maxScan))

	tick := time.NewTicker(s.interval)
	defer tick.Stop()

	s.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.tick(ctx)
		}
	}
}

// tick — решение планировщика и, если нужен, проход. Пропущенный тик не делает
// ни одного FetchMessage: инвариант «commit перед break» (4.37) не затронут, а
// очередь узлов на паузе не переписывается вхолостую.
func (s *PausedSweeper) tick(ctx context.Context) {
	if s.planner == nil {
		s.sweepOnce(ctx)
		return
	}
	if !s.planner.Decide(ctx).Run {
		return
	}
	s.planner.PassDone(s.sweepOnce(ctx))
}

// sweepOnce — один проход: обрабатываем до maxScan сообщений, КАЖДОЕ прочитанное
// коммитим, и завершаем проход, когда «догнали» собственный хвост (встретили id,
// уже обработанный В ЭТОМ проходе) — иначе бэклог paused-узла крутился бы
// бесконечно внутри одного прохода.
//
// КЛЮЧЕВОЕ (грабли §36, поймано стендом): проверка wrap'а (seen[id]) и break —
// СТРОГО ПОСЛЕ Commit прочитанного сообщения. kafka-go продвигает курсор за
// каждое прочитанное; если прерваться, НЕ закоммитив его, коммит последующих
// сообщений прокатит offset мимо незакоммиченного → потеря. По той же причине
// транзиентный сбой обработки повторяется на месте, а не прерывает проход
// (§113.2) — до §113 break без commit терял сообщение на следующем проходе.
//
// Возвращает число обработанных (закоммиченных) сообщений.
func (s *PausedSweeper) sweepOnce(ctx context.Context) int {
	seen := make(map[string]bool)
	processed := 0

	for i := 0; i < s.maxScan; i++ {
		if ctx.Err() != nil {
			return processed
		}
		// bounded-ожидание: пустой топик/конец бэклога → таймаут → конец прохода.
		fetchCtx, cancel := context.WithTimeout(ctx, s.fetchTimeout)
		msg, err := s.consumer.FetchMessage(fetchCtx)
		cancel()
		if err != nil {
			break
		}

		hdrs := headersToMap(msg.Headers)
		if !s.handleInPlace(ctx, msg, hdrs) {
			return processed // shutdown посреди повторов: без commit, перечитается после рестарта
		}
		if err := s.consumer.Commit(ctx, msg); err != nil {
			s.logger.ErrorWithOp("paused sweep commit failed", err, "kafka.pausedSweep.commit",
				s.logger.Str("topic", s.topic),
				s.logger.Int("partition", msg.Partition))
		}
		processed++

		if id := hdrs["id"]; id != "" {
			if seen[id] {
				break
			}
			seen[id] = true
		}
	}

	if processed > 0 {
		s.logger.Debug("paused sweep pass done",
			s.logger.Str("topic", s.topic),
			s.logger.Int("processed", processed))
	}
	return processed
}

// handleInPlace обрабатывает сообщение до финального исхода. Handle сам решает
// судьбу: узел всё ещё paused → перенос в хвост этого же топика (Requeued),
// ожил → доставка (Ack/DLQed), отменён или disabled → drop (Ack). Retry —
// транзиентный сбой (PostgreSQL или produce недоступны) → повтор того же
// сообщения (§113.2). false — ctx отменён раньше финального исхода.
func (s *PausedSweeper) handleInPlace(ctx context.Context, msg kafka.Message, hdrs map[string]string) bool {
	return s.retry.run(ctx, func() bool {
		return s.processor.Handle(ctx, msg.Value, hdrs) != usecase.HandleRetry
	}, func(attempt int, wait time.Duration) {
		// §51.9: «залипание» прохода на одном сообщении иначе выглядит как молчание.
		s.logger.Debug("paused sweep: retrying message in place",
			s.logger.Str("topic", s.topic),
			s.logger.Int("partition", msg.Partition),
			s.logger.Any("offset", msg.Offset),
			s.logger.Str("id", hdrs["id"]),
			s.logger.Int("attempt", attempt),
			s.logger.Str("wait", wait.String()))
	})
}

// Stop корректно завершает consumer sweeper'а.
func (s *PausedSweeper) Stop() {
	if s.consumer != nil {
		_ = s.consumer.Close()
	}
}

// Snapshot — lag по delay-топику (для Prometheus, как у основного consumer'а).
// Растущий lag означает, что бэклог паузы не разбирается (sweeper выключен или
// узлы давно на паузе) — по нему строится алерт.
func (s *PausedSweeper) Snapshot() []LagSnapshot {
	if s.consumer == nil {
		return nil
	}
	st := s.consumer.Stats()
	return []LagSnapshot{{Topic: st.Topic, Partition: st.Partition, Lag: st.Lag}}
}

// Group — имя consumer-group (для меток метрик).
func (s *PausedSweeper) Group() string { return s.group }
