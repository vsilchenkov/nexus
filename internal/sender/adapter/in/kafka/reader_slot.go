package kafka

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	kafka "github.com/segmentio/kafka-go"

	"nexus/internal/platform/clock"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/safego"
)

// slotReader — reader одного экземпляра группы. Реализуется *kafkapf.Consumer.
type slotReader interface {
	FetchMessage(ctx context.Context) (kafka.Message, error)
	Commit(ctx context.Context, msg kafka.Message) error
	Close() error
}

// readerFactory создаёт reader с заданным client.id.
type readerFactory func(clientID string) slotReader

// commitRecorder считает отвергнутые брокером commit'ы
// (nexus_kafka_commit_errors_total, §114.3).
type commitRecorder interface {
	InitKafkaCommitErrors(group string)
	IncKafkaCommitError(group string)
}

// slotCloseTimeout — сколько ждать Close старого reader'а при пересоздании.
// Зависший kafka-go reader может не закрыться никогда; новый создаётся всё
// равно, а старая горутина Close остаётся висеть (с Warn).
const slotCloseTimeout = 30 * time.Second

// readerSlot — экземпляр consumer-группы, чей reader можно пересоздать на ходу
// (§113.6). Снаружи выглядит как обычный reader (FetchMessage/Commit/Close), а
// сам отмечает, сколько ждёт в FetchMessage, и подменяет reader по запросу.
//
// Подмена происходит только внутри FetchMessage или в Recreate, который зовёт
// владелец между проходами. Поэтому Commit всегда идёт в тот reader, что выдал
// сообщение: между выдачей сообщения и его commit'ом reader не меняется.
//
// client.id = "<имя>-g<поколение>": новое поколение после каждого пересоздания,
// иначе в DescribeGroups не отличить зависшего старого участника от нового.
type readerSlot struct {
	group     string
	name      string
	newReader readerFactory
	clock     clock.Clock
	commits   commitRecorder // nil — без метрики
	logger    logging.Logger

	mu           sync.Mutex
	reader       slotReader
	clientID     string
	gen          int
	cancelFetch  context.CancelFunc // не nil — экземпляр ждёт в FetchMessage
	waitingSince time.Time
	restartReq   bool
	closed       bool
	// fetched — партиция → позиция следующего сообщения (Offset+1 последнего
	// полученного), §114.2. Перезаписывается, а не растёт: после внутреннего
	// ребаланса kafka-go читает заново с committed. Сбрасывается в Recreate.
	fetched map[int]int64
}

// newReaderSlot создаёт слот idx группы и его первый reader. Имя (постоянная
// часть client.id), часы и счётчик ошибок commit берутся из guard.
func newReaderSlot(group string, idx int, guard StallGuard, newReader readerFactory, logger logging.Logger) *readerSlot {
	s := &readerSlot{group: group, name: slotName(group, guard, idx), newReader: newReader,
		clock: guard.clk(), commits: guard.Commits, logger: logger}
	if s.commits != nil {
		s.commits.InitKafkaCommitErrors(group)
	}
	s.gen = 1
	s.clientID = s.clientIDFor(s.gen)
	s.reader = newReader(s.clientID)
	return s
}

func (s *readerSlot) clientIDFor(gen int) string { return fmt.Sprintf("%s-g%d", s.name, gen) }

// ClientID — client.id текущего reader'а.
func (s *readerSlot) ClientID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clientID
}

// WaitingSince — с какого момента экземпляр ждёт в FetchMessage; ноль — не
// ждёт (обрабатывает сообщение или остановлен).
func (s *readerSlot) WaitingSince() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.waitingSince
}

// RequestRestart прерывает FetchMessage, чтобы тот пересоздал reader. Только
// для экземпляра, который сейчас ждёт в fetch: занятый обработкой не трогается
// (false). Вызывается сторожем из своей горутины.
func (s *readerSlot) RequestRestart() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancelFetch == nil || s.closed {
		return false
	}
	s.restartReq = true
	s.cancelFetch()
	return true
}

// FetchMessage ждёт сообщение текущего reader'а. Прерывание сторожем
// (RequestRestart) не возвращается вызывающему: reader пересоздаётся, и
// ожидание продолжается на новом.
func (s *readerSlot) FetchMessage(ctx context.Context) (kafka.Message, error) {
	for {
		if err := s.applyRestart(ctx); err != nil {
			return kafka.Message{}, err
		}
		r, fctx, cancel := s.beginFetch(ctx)
		msg, err := r.FetchMessage(fctx)
		var got *kafka.Message
		if err == nil {
			got = &msg
		}
		restart := s.endFetch(got)
		cancel()
		if err == nil || ctx.Err() != nil || !restart {
			return msg, err
		}
	}
}

// beginFetch отмечает начало ожидания и даёт сторожу способ его прервать.
func (s *readerSlot) beginFetch(ctx context.Context) (slotReader, context.Context, context.CancelFunc) {
	fctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelFetch = cancel
	s.waitingSince = s.clock.Now()
	return s.reader, fctx, cancel
}

// endFetch снимает отметку ожидания. Пришло сообщение (msg != nil) — reader
// жив: запрос на перезапуск (гонка со сторожем) снимается, позиция партиции
// запоминается для сторожа (§114.2). Возвращает, ждёт ли перезапуск.
func (s *readerSlot) endFetch(msg *kafka.Message) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelFetch = nil
	s.waitingSince = time.Time{}
	if msg != nil {
		s.restartReq = false
		if s.fetched == nil {
			s.fetched = map[int]int64{}
		}
		s.fetched[msg.Partition] = msg.Offset + 1
	}
	return s.restartReq
}

// FetchedNext — позиция, с которой экземпляр прочитает партицию дальше
// (Offset+1 последнего полученного сообщения); ok=false — из этой партиции
// текущий reader ещё ничего не получал. Сторож сравнивает её с концом лога:
// дочитавший экземпляр, у которого стоит только commit, не перезапускается
// (§114.2).
func (s *readerSlot) FetchedNext(partition int) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next, ok := s.fetched[partition]
	return next, ok
}

// applyRestart выполняет запрошенный перезапуск перед очередным fetch.
func (s *readerSlot) applyRestart(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	pending := s.restartReq
	s.restartReq = false
	s.mu.Unlock()
	if pending {
		s.Recreate("stalled")
	}
	return nil
}

// Commit подтверждает сообщение в текущем reader'е — том, что его выдал.
//
// Через слот идут commit'ы всех групп Sender'а, поэтому отказ брокера
// считается здесь (§114.3). Штатное завершение — отменённый ctx (shutdown) и
// закрытый reader (io.ErrClosedPipe) — ошибкой брокера не считается. Сама
// ошибка возвращается вызывающему: он пишет ErrorWithOp с топиком и партицией.
func (s *readerSlot) Commit(ctx context.Context, msg kafka.Message) error {
	s.mu.Lock()
	r := s.reader
	s.mu.Unlock()
	err := r.Commit(ctx, msg)
	if err != nil && s.commits != nil && ctx.Err() == nil && !errors.Is(err, io.ErrClosedPipe) {
		s.commits.IncKafkaCommitError(s.group)
	}
	return err
}

// Recreate закрывает текущий reader и создаёт новый со следующим client.id.
// Новый reader читает с committed-офсета группы: всё прочитанное, но не
// подтверждённое, перечитается (at-least-once). Вызывает только владелец
// слота, не во время FetchMessage.
func (s *readerSlot) Recreate(reason string) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	old, oldID := s.reader, s.clientID
	s.mu.Unlock()

	s.closeWithTimeout(old, oldID)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.gen++
	s.clientID = s.clientIDFor(s.gen)
	s.reader = s.newReader(s.clientID)
	s.fetched = nil // новый reader начнёт с committed
	s.logger.Debug("kafka: reader recreated",
		s.logger.Str("slot", s.name),
		s.logger.Str("old_client_id", oldID),
		s.logger.Str("client_id", s.clientID),
		s.logger.Str("reason", reason))
}

// closeWithTimeout закрывает reader, но не дольше slotCloseTimeout: Close
// зависшего reader'а может не вернуться, а слоту нужен новый.
func (s *readerSlot) closeWithTimeout(r slotReader, clientID string) {
	done := safego.Go(s.logger, "sender.kafkaSlotClose", func() {
		if err := r.Close(); err != nil {
			s.logger.Debug("kafka: old reader close failed",
				s.logger.Str("client_id", clientID), s.logger.Err(err))
		}
	})
	timer := time.NewTimer(slotCloseTimeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		s.logger.Warn("kafka: old reader did not close in time; replacing it anyway",
			s.logger.Str("client_id", clientID),
			s.logger.Str("timeout", slotCloseTimeout.String()))
	}
}

// Close останавливает слот: прерывает ожидание и закрывает текущий reader.
// Пересоздания после Close не будет.
func (s *readerSlot) Close() error {
	s.mu.Lock()
	s.closed = true
	if s.cancelFetch != nil {
		s.cancelFetch()
	}
	r := s.reader
	s.mu.Unlock()
	return r.Close()
}
