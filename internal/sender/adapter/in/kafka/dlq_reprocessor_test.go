package kafka

import (
	"context"
	"testing"
	"time"

	kafka "github.com/segmentio/kafka-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/logging"
	"nexus/internal/sender/usecase"
)

// stubDLQProcessor — управляемый dlqProcessor: results задаёт исход каждого
// следующего вызова (последний повторяется, когда список исчерпан).
type stubDLQProcessor struct {
	results  []usecase.ReprocessResult
	calls    int
	onHandle func(call int)
}

func (s *stubDLQProcessor) ProcessMessage(context.Context, []byte, map[string]string) usecase.ReprocessResult {
	s.calls++
	if s.onHandle != nil {
		s.onHandle(s.calls)
	}
	return s.results[min(s.calls-1, len(s.results)-1)]
}

var _ dlqProcessor = (*stubDLQProcessor)(nil)

func newTestDLQReprocessor(c sweepConsumer, p dlqProcessor) *DLQReprocessor {
	return &DLQReprocessor{
		consumer:     c,
		processor:    p,
		interval:     time.Hour,
		maxScan:      10,
		fetchTimeout: time.Millisecond,
		retry:        inPlaceRetry{min: time.Millisecond, max: time.Millisecond},
		logger:       logging.NewNoop(),
		topic:        "nexus.async.dlq",
	}
}

// TestDLQReprocessor_RetryInPlace_NoLoss (§113.2): то же, что у sweeper'а паузы.
// Сбой republish'а на X (Kafka на миг недоступна) не должен дать закоммитить X+1
// раньше финального исхода X — иначе committed offset перешагнёт X.
func TestDLQReprocessor_RetryInPlace_NoLoss(t *testing.T) {
	t.Parallel()

	c := &fakeSweepConsumer{msgs: []kafka.Message{msgWithID(1, "a"), msgWithID(2, "b")}}
	proc := &stubDLQProcessor{results: []usecase.ReprocessResult{usecase.ReprocessRetry, usecase.ReprocessCommit}}
	r := newTestDLQReprocessor(c, proc)

	r.sweepOnce(context.Background(), context.Background())
	r.sweepOnce(context.Background(), context.Background())

	require.NotEmpty(t, c.committed)
	assert.Equal(t, int64(1), c.committed[0].Offset, "первым закоммичен X, а не X+1")
	assert.Equal(t, []int64{1, 2}, c.committedOffsets())
	assert.Equal(t, 3, proc.calls)
}

// TestDLQReprocessor_RetryInPlace_CtxCancel: shutdown посреди повторов — выход
// без commit и без чтения следующего сообщения.
func TestDLQReprocessor_RetryInPlace_CtxCancel(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &fakeSweepConsumer{msgs: []kafka.Message{msgWithID(1, "a"), msgWithID(2, "b")}}
	proc := &stubDLQProcessor{results: []usecase.ReprocessResult{usecase.ReprocessRetry}}
	proc.onHandle = func(call int) {
		if call == 2 {
			cancel()
		}
	}

	newTestDLQReprocessor(c, proc).sweepOnce(ctx, ctx)

	assert.Equal(t, 2, proc.calls)
	assert.Equal(t, 1, c.fetched)
	assert.Empty(t, c.committed)
}

// TestDLQReprocessor_StopsOnWrap: догнав собственный republish-хвост, проход
// завершается; сообщение, замкнувшее круг, закоммичено до выхода (§36.4).
func TestDLQReprocessor_StopsOnWrap(t *testing.T) {
	t.Parallel()

	c := &fakeSweepConsumer{msgs: []kafka.Message{
		msgWithID(1, "a"), msgWithID(2, "b"), msgWithID(3, "a"), msgWithID(4, "c"),
	}}
	proc := &stubDLQProcessor{results: []usecase.ReprocessResult{usecase.ReprocessCommit}}

	newTestDLQReprocessor(c, proc).sweepOnce(context.Background(), context.Background())

	assert.Equal(t, 3, proc.calls)
	assert.Equal(t, []int64{1, 2, 3}, c.committedOffsets())
}
