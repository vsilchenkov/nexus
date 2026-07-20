package kafka

import (
	"context"
	"errors"
	"testing"
	"time"

	kafka "github.com/segmentio/kafka-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/logging"
	"nexus/internal/sender/usecase"
)

// fakeSweepConsumer отдаёт заранее заданные сообщения, затем io-подобную ошибку
// (как реальный reader по таймауту пустого топика) и записывает коммиты.
type fakeSweepConsumer struct {
	msgs      []kafka.Message
	fetched   int
	committed []kafka.Message
	commitErr error
}

func (c *fakeSweepConsumer) FetchMessage(context.Context) (kafka.Message, error) {
	if c.fetched >= len(c.msgs) {
		return kafka.Message{}, errors.New("fetch timeout: no more messages")
	}
	m := c.msgs[c.fetched]
	c.fetched++
	return m, nil
}

func (c *fakeSweepConsumer) Commit(_ context.Context, msg kafka.Message) error {
	if c.commitErr != nil {
		return c.commitErr
	}
	c.committed = append(c.committed, msg)
	return nil
}

func (c *fakeSweepConsumer) Close() error             { return nil }
func (c *fakeSweepConsumer) Stats() kafka.ReaderStats { return kafka.ReaderStats{} }
func (c *fakeSweepConsumer) committedOffsets() []int64 {
	out := make([]int64, 0, len(c.committed))
	for _, m := range c.committed {
		out = append(out, m.Offset)
	}
	return out
}

var _ sweepConsumer = (*fakeSweepConsumer)(nil)

func msgWithID(offset int64, id string) kafka.Message {
	return kafka.Message{
		Offset:  offset,
		Value:   []byte(`{"id":"` + id + `"}`),
		Headers: []kafka.Header{{Key: "id", Value: []byte(id)}},
	}
}

func newTestSweeper(c sweepConsumer, p messageProcessor, maxScan int) *PausedSweeper {
	return &PausedSweeper{
		consumer:     c,
		processor:    p,
		interval:     time.Hour,
		maxScan:      maxScan,
		fetchTimeout: time.Millisecond,
		logger:       logging.NewNoop(),
		topic:        "nexus.async.paused",
		group:        "nexus-sender-paused",
	}
}

// TestPausedSweeper_CommitsTerminalResults: перенос в хвост (Requeued),
// доставка (Ack) и уход в DLQ — все терминальные, offset коммитится. Именно это
// освобождает партицию delay-топика и не даёт бэклогу одного узла блокировать
// соседей.
func TestPausedSweeper_CommitsTerminalResults(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		result usecase.HandleResult
	}{
		{"Requeued (узел всё ещё на паузе)", usecase.HandleRequeued},
		{"Ack (узел ожил — доставлено)", usecase.HandleAck},
		{"DLQed (ожил, но доставка провалилась)", usecase.HandleDLQed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			c := &fakeSweepConsumer{msgs: []kafka.Message{msgWithID(1, "a"), msgWithID(2, "b")}}
			proc := newStubProcessor(tc.result)
			newTestSweeper(c, proc, 10).sweepOnce(context.Background())

			assert.Equal(t, 2, proc.calls, "проход обрабатывает все доступные сообщения")
			assert.Equal(t, []int64{1, 2}, c.committedOffsets(), "каждое обработанное коммитится")
		})
	}
}

// TestPausedSweeper_RetryBreaksWithoutCommit: транзиентный сбой (PostgreSQL или
// produce недоступны) — проход прерывается БЕЗ коммита. Коммитить нельзя: тогда
// следующий проход не перечитает сообщение и оно потеряется.
func TestPausedSweeper_RetryBreaksWithoutCommit(t *testing.T) {
	t.Parallel()

	c := &fakeSweepConsumer{msgs: []kafka.Message{msgWithID(1, "a"), msgWithID(2, "b")}}
	proc := newStubProcessor(usecase.HandleRetry)

	newTestSweeper(c, proc, 10).sweepOnce(context.Background())

	assert.Equal(t, 1, proc.calls, "после Retry проход прерывается")
	assert.Empty(t, c.committed, "необработанное сообщение не коммитим")
}

// TestPausedSweeper_StopsOnWrap: догнав собственный хвост (тот же id второй раз
// за проход), sweeper завершает проход — иначе бэклог paused-узла крутился бы
// внутри одного прохода бесконечно. Ключевое: сообщение, на котором сработал
// wrap, УЖЕ закоммичено (иначе коммит последующих прокатил бы offset мимо).
func TestPausedSweeper_StopsOnWrap(t *testing.T) {
	t.Parallel()

	c := &fakeSweepConsumer{msgs: []kafka.Message{
		msgWithID(1, "a"),
		msgWithID(2, "b"),
		msgWithID(3, "a"), // круг замкнулся
		msgWithID(4, "c"), // не должно быть прочитано в этом проходе
	}}
	proc := newStubProcessor(usecase.HandleRequeued)

	newTestSweeper(c, proc, 100).sweepOnce(context.Background())

	assert.Equal(t, 3, proc.calls, "проход останавливается на повторе id")
	require.Equal(t, []int64{1, 2, 3}, c.committedOffsets(),
		"сообщение, замкнувшее круг, закоммичено ДО выхода")
}

// TestPausedSweeper_RespectsMaxScan: проход ограничен maxScan — защита брокера
// от вычитывания огромного бэклога за раз.
func TestPausedSweeper_RespectsMaxScan(t *testing.T) {
	t.Parallel()

	msgs := make([]kafka.Message, 0, 10)
	for i := range 10 {
		msgs = append(msgs, msgWithID(int64(i+1), string(rune('a'+i))))
	}
	c := &fakeSweepConsumer{msgs: msgs}
	proc := newStubProcessor(usecase.HandleRequeued)

	newTestSweeper(c, proc, 3).sweepOnce(context.Background())

	assert.Equal(t, 3, proc.calls)
	assert.Len(t, c.committed, 3)
}

// TestPausedSweeper_CommitErrorDoesNotAbortPass: сбой коммита логируется, но
// проход продолжается (сообщение перечитается позже — at-least-once).
func TestPausedSweeper_CommitErrorDoesNotAbortPass(t *testing.T) {
	t.Parallel()

	c := &fakeSweepConsumer{
		msgs:      []kafka.Message{msgWithID(1, "a"), msgWithID(2, "b")},
		commitErr: errors.New("kafka unavailable"),
	}
	proc := newStubProcessor(usecase.HandleRequeued)

	assert.NotPanics(t, func() {
		newTestSweeper(c, proc, 10).sweepOnce(context.Background())
	})
	assert.Equal(t, 2, proc.calls)
}

// TestPausedSweeper_CtxCancelStopsPass: отмена контекста (shutdown) прекращает
// проход немедленно.
func TestPausedSweeper_CtxCancelStopsPass(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c := &fakeSweepConsumer{msgs: []kafka.Message{msgWithID(1, "a")}}
	proc := newStubProcessor(usecase.HandleRequeued)

	newTestSweeper(c, proc, 10).sweepOnce(ctx)

	assert.Zero(t, proc.calls, "при отменённом ctx проход не начинается")
}
