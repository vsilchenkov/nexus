package kafka

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	kafka "github.com/segmentio/kafka-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/config"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/metrics"
	"nexus/internal/sender/usecase"
)

// stubProcessor — управляемый messageProcessor. onHandle (если задан)
// вызывается внутри Handle: позволяет проверить состояние в момент обработки.
type stubProcessor struct {
	result   usecase.HandleResult
	calls    int
	gotValue []byte
	gotHdrs  map[string]string
	onHandle func()
}

func (s *stubProcessor) Handle(_ context.Context, value []byte, headers map[string]string) usecase.HandleResult {
	s.calls++
	s.gotValue = value
	s.gotHdrs = headers
	if s.onHandle != nil {
		s.onHandle()
	}
	return s.result
}

var _ messageProcessor = (*stubProcessor)(nil)

// stubCommitter — фиксирует вызовы Commit. onCommit вызывается до возврата.
type stubCommitter struct {
	calls    int
	gotMsg   kafka.Message
	err      error
	onCommit func()
}

func (s *stubCommitter) Commit(_ context.Context, msg kafka.Message) error {
	s.calls++
	s.gotMsg = msg
	if s.onCommit != nil {
		s.onCommit()
	}
	return s.err
}

var _ messageCommitter = (*stubCommitter)(nil)

func newTestGroup(p messageProcessor, opts ...ConsumerOption) *ConsumerGroup {
	return NewConsumerGroup(&config.Config{}, "nexus.async", p, logging.NewNoop(), opts...)
}

// TestConsumerGroup_HandleMessage — маппинг результата обработки на судьбу
// offset'а: Ack/DLQed коммитим, Retry — нет (сообщение остаётся незакоммиченным).
func TestConsumerGroup_HandleMessage(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		result     usecase.HandleResult
		wantCommit int
	}{
		{"Ack → commit", usecase.HandleAck, 1},
		{"DLQed → commit", usecase.HandleDLQed, 1},
		{"Retry → без commit'а", usecase.HandleRetry, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			proc := &stubProcessor{result: tc.result}
			com := &stubCommitter{}
			msg := kafka.Message{Partition: 3, Offset: 42, Value: []byte(`{"id":"x"}`)}

			newTestGroup(proc).handleMessage(context.Background(), com, msg)

			assert.Equal(t, 1, proc.calls, "Handle вызывается ровно один раз на сообщение")
			assert.Equal(t, tc.wantCommit, com.calls)
			if tc.wantCommit > 0 {
				assert.Equal(t, msg.Offset, com.gotMsg.Offset, "коммитим именно обработанное сообщение")
				assert.Equal(t, msg.Partition, com.gotMsg.Partition)
			}
		})
	}
}

// TestConsumerGroup_HandleMessage_CommitError: сбой commit'а логируется, но не
// роняет consumer-горутину (иначе один сбой Kafka убивал бы обработку партиции).
func TestConsumerGroup_HandleMessage_CommitError(t *testing.T) {
	t.Parallel()

	com := &stubCommitter{err: errors.New("kafka unavailable")}
	g := newTestGroup(&stubProcessor{result: usecase.HandleAck})

	assert.NotPanics(t, func() {
		g.handleMessage(context.Background(), com, kafka.Message{Offset: 1})
	})
	assert.Equal(t, 1, com.calls, "commit пробуется ровно один раз, без внутреннего ретрая")
}

// TestConsumerGroup_HandleMessage_InFlightGauge (§31): сообщение считается «в
// полёте» на время обработки. Фиксируем текущий контракт: dec происходит ДО
// commit'а, то есть commit в gauge уже не входит.
func TestConsumerGroup_HandleMessage_InFlightGauge(t *testing.T) {
	t.Parallel()

	m := metrics.New("sender")
	gauge := m.KafkaInFlight.WithLabelValues("sender")

	var duringHandle, duringCommit float64
	proc := &stubProcessor{result: usecase.HandleAck}
	proc.onHandle = func() { duringHandle = testutil.ToFloat64(gauge) }
	com := &stubCommitter{}
	com.onCommit = func() { duringCommit = testutil.ToFloat64(gauge) }

	g := newTestGroup(proc, WithMetrics(m))
	g.handleMessage(context.Background(), com, kafka.Message{Offset: 7})

	assert.Equal(t, 1.0, duringHandle, "во время Handle сообщение «в полёте»")
	assert.Equal(t, 0.0, duringCommit, "dec до commit'а — commit в in-flight не входит")
	assert.Equal(t, 0.0, testutil.ToFloat64(gauge), "после обработки gauge вернулся к нулю")
}

// TestConsumerGroup_HandleMessage_HeadersPropagated: заголовки сообщения
// доходят до processor'а (OTel-propagator читает traceparent именно оттуда).
func TestConsumerGroup_HandleMessage_HeadersPropagated(t *testing.T) {
	t.Parallel()

	proc := &stubProcessor{result: usecase.HandleAck}
	msg := kafka.Message{
		Value: []byte(`{"id":"x"}`),
		Headers: []kafka.Header{
			{Key: "traceparent", Value: []byte("00-abc-def-01")},
			{Key: "traceparent", Value: []byte("00-second-value-01")},
		},
	}

	newTestGroup(proc).handleMessage(context.Background(), &stubCommitter{}, msg)

	require.NotNil(t, proc.gotHdrs)
	assert.Equal(t, "00-abc-def-01", proc.gotHdrs["traceparent"], "дубль ключа — берётся первое значение")
	assert.Equal(t, `{"id":"x"}`, string(proc.gotValue))
}

func TestHeadersToMap(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   []kafka.Header
		want map[string]string
	}{
		{"nil → пустая map", nil, map[string]string{}},
		{"пустой слайс → пустая map", []kafka.Header{}, map[string]string{}},
		{
			"одиночные ключи",
			[]kafka.Header{{Key: "id", Value: []byte("1")}, {Key: "node_path", Value: []byte("a/b")}},
			map[string]string{"id": "1", "node_path": "a/b"},
		},
		{
			"дубль ключа → первое значение",
			[]kafka.Header{{Key: "id", Value: []byte("first")}, {Key: "id", Value: []byte("second")}},
			map[string]string{"id": "first"},
		},
		{
			"пустое значение сохраняется",
			[]kafka.Header{{Key: "reason", Value: []byte("")}},
			map[string]string{"reason": ""},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, headersToMap(tc.in))
		})
	}
}
