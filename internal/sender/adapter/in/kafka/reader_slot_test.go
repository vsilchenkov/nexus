package kafka

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	kafka "github.com/segmentio/kafka-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/clock"
	"nexus/internal/platform/logging"
)

// blockingReader — reader, чей FetchMessage висит до отмены ctx (модель
// зависшего kafka-go reader'а) либо отдаёт сообщения из msgs.
type blockingReader struct {
	id        string
	msgs      chan kafka.Message
	mu        sync.Mutex
	committed []kafka.Message
	commitErr error // не nil — Commit отвергается (§114.3)
	closed    bool
}

func newBlockingReader(id string) *blockingReader {
	return &blockingReader{id: id, msgs: make(chan kafka.Message, 4)}
}

func (r *blockingReader) FetchMessage(ctx context.Context) (kafka.Message, error) {
	select {
	case <-ctx.Done():
		return kafka.Message{}, ctx.Err()
	case m := <-r.msgs:
		return m, nil
	}
}

func (r *blockingReader) Commit(_ context.Context, msg kafka.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.commitErr != nil {
		return r.commitErr
	}
	r.committed = append(r.committed, msg)
	return nil
}

func (r *blockingReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	return nil
}

func (r *blockingReader) isClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

// readerLog — фабрика, запоминающая созданные reader'ы по порядку.
type readerLog struct {
	mu      sync.Mutex
	readers []*blockingReader
}

func (l *readerLog) factory(id string) slotReader {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := newBlockingReader(id)
	l.readers = append(l.readers, r)
	return r
}

func (l *readerLog) get(i int) *blockingReader {
	l.mu.Lock()
	defer l.mu.Unlock()
	if i >= len(l.readers) {
		return nil
	}
	return l.readers[i]
}

func (l *readerLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.readers)
}

func newTestSlot(log *readerLog) *readerSlot {
	return newTestSlotWith(log, nil)
}

// newTestSlotWith — слот группы nexus-sender реплики sender-1 со счётчиком
// ошибок commit (nil — без метрики).
func newTestSlotWith(log *readerLog, commits commitRecorder) *readerSlot {
	guard := StallGuard{ClientPrefix: "sender-1", Clock: clock.NewFake(time.Unix(1000, 0)), Commits: commits}
	return newReaderSlot("nexus-sender", 0, guard, log.factory, logging.NewNoop())
}

// commitTally — счётчик ошибок commit по группам; inits — какие ряды созданы.
type commitTally struct {
	mu     sync.Mutex
	inits  map[string]bool
	errors map[string]int
}

func (c *commitTally) InitKafkaCommitErrors(group string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inits == nil {
		c.inits = map[string]bool{}
	}
	c.inits[group] = true
}

func (c *commitTally) IncKafkaCommitError(group string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.errors == nil {
		c.errors = map[string]int{}
	}
	c.errors[group]++
}

func (c *commitTally) count(group string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.errors[group]
}

// waitFetching ждёт, пока слот войдёт в FetchMessage.
func waitFetching(t *testing.T, s *readerSlot) {
	t.Helper()
	require.Eventually(t, func() bool { return !s.WaitingSince().IsZero() }, 2*time.Second, 5*time.Millisecond)
}

// TestReaderSlot_RestartRecreatesReaderInsideFetch (§113.6): прерывание сторожем
// не всплывает к вызывающему — слот закрывает старый reader, создаёт новый со
// следующим client.id и продолжает ждать уже на нём. Commit идёт в новый reader.
func TestReaderSlot_RestartRecreatesReaderInsideFetch(t *testing.T) {
	t.Parallel()
	log := &readerLog{}
	s := newTestSlot(log)
	require.Equal(t, "nexus-sender-sender-1-0-g1", s.ClientID())

	type res struct {
		msg kafka.Message
		err error
	}
	out := make(chan res, 1)
	go func() {
		m, err := s.FetchMessage(context.Background())
		out <- res{m, err}
	}()
	waitFetching(t, s)
	assert.Equal(t, time.Unix(1000, 0), s.WaitingSince())

	require.True(t, s.RequestRestart())
	require.Eventually(t, func() bool { return log.count() == 2 }, 2*time.Second, 5*time.Millisecond)
	assert.True(t, log.get(0).isClosed(), "старый reader закрыт")
	assert.Equal(t, "nexus-sender-sender-1-0-g2", s.ClientID())

	log.get(1).msgs <- kafka.Message{Offset: 7}
	r := <-out
	require.NoError(t, r.err)
	assert.EqualValues(t, 7, r.msg.Offset)
	assert.True(t, s.WaitingSince().IsZero(), "сообщение получено — не ждёт")

	require.NoError(t, s.Commit(context.Background(), r.msg))
	assert.Len(t, log.get(1).committed, 1, "commit — в reader, выдавший сообщение")
	require.NoError(t, s.Close())
}

// TestReaderSlot_RestartOnlyWhileFetching: занятый обработкой экземпляр
// (не в fetch) сторож не трогает.
func TestReaderSlot_RestartOnlyWhileFetching(t *testing.T) {
	t.Parallel()
	log := &readerLog{}
	s := newTestSlot(log)
	assert.False(t, s.RequestRestart())
	assert.Equal(t, 1, log.count())
	require.NoError(t, s.Close())
}

// TestReaderSlot_MessageWinsOverRestart: сообщение пришло одновременно с
// запросом перезапуска — reader жив, перезапуск снимается.
func TestReaderSlot_MessageWinsOverRestart(t *testing.T) {
	t.Parallel()
	log := &readerLog{}
	s := newTestSlot(log)
	s.restartReq = true // гонка: запрос пришёл, а fetch уже вернул сообщение
	assert.False(t, s.endFetch(true))
	assert.False(t, s.restartReq)
	require.NoError(t, s.Close())
}

// TestReaderSlot_OuterCancelReturns: отмена внешнего ctx (shutdown) — ошибка
// вызывающему, без пересоздания.
func TestReaderSlot_OuterCancelReturns(t *testing.T) {
	t.Parallel()
	log := &readerLog{}
	s := newTestSlot(log)
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := s.FetchMessage(ctx)
		errc <- err
	}()
	waitFetching(t, s)
	cancel()
	require.ErrorIs(t, <-errc, context.Canceled)
	assert.Equal(t, 1, log.count())
	require.NoError(t, s.Close())
}

// TestReaderSlot_ClosedSlotNotRecreated: после Close пересоздания нет, а
// ожидание прерывается.
func TestReaderSlot_ClosedSlotNotRecreated(t *testing.T) {
	t.Parallel()
	log := &readerLog{}
	s := newTestSlot(log)
	errc := make(chan error, 1)
	go func() {
		_, err := s.FetchMessage(context.Background())
		errc <- err
	}()
	waitFetching(t, s)
	require.NoError(t, s.Close())
	err := <-errc
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled))
	assert.False(t, s.RequestRestart())
	s.Recreate("test")
	assert.Equal(t, 1, log.count())
}

// TestReaderSlot_CommitErrorCounted (§114.3): отказ брокера считается в ряду
// группы и возвращается вызывающему; штатное завершение (отменённый ctx,
// закрытый reader) не считается. Ряд группы создаётся при создании слота.
func TestReaderSlot_CommitErrorCounted(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		commitErr error
		cancelCtx bool
		want      int
	}{
		{name: "успешный commit", want: 0},
		{name: "брокер отверг", commitErr: errors.New("[19] Not Enough Replicas"), want: 1},
		{name: "закрытый reader", commitErr: fmt.Errorf("commit offset: %w", io.ErrClosedPipe), want: 0},
		{name: "shutdown", commitErr: context.Canceled, cancelCtx: true, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			log := &readerLog{}
			tally := &commitTally{}
			s := newTestSlotWith(log, tally)
			assert.True(t, tally.inits["nexus-sender"], "ряд группы создан нулём при старте")
			assert.Equal(t, "nexus-sender-sender-1-0-g1", s.ClientID())

			log.get(0).commitErr = tt.commitErr
			ctx, cancel := context.WithCancel(context.Background())
			if tt.cancelCtx {
				cancel()
			}
			defer cancel()
			err := s.Commit(ctx, kafka.Message{Partition: 2, Offset: 41})
			if tt.commitErr != nil {
				require.ErrorIs(t, err, tt.commitErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.want, tally.count("nexus-sender"))
			require.NoError(t, s.Close())
		})
	}
}
