package usecase

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/sender/usecase/port"
)

// stubNodeReader — fake реализация NodeReader.
type stubAsyncNodeReader struct {
	node *domain.Node
	err  error
}

func (s *stubAsyncNodeReader) GetByPath(_ context.Context, _ string) (*domain.Node, error) {
	return s.node, s.err
}

// stubDLQProducer — собирает все производства DLQ для проверки.
type stubDLQProducer struct {
	produced []dlqMsg
	err      error
}

type dlqMsg struct {
	topic   string
	key     string
	value   []byte
	headers map[string]string
}

func (s *stubDLQProducer) Produce(_ context.Context, topic, key string, value []byte, headers map[string]string) error {
	if s.err != nil {
		return s.err
	}
	s.produced = append(s.produced, dlqMsg{topic: topic, key: key, value: value, headers: headers})
	return nil
}

func newAsyncProcessorForTest(t *testing.T, nr NodeReader, httpResp *port.HTTPResponse, httpErr error, dlq DLQProducer) *AsyncProcessor {
	t.Helper()
	httpc := &stubHTTPCaller{}
	if httpResp != nil {
		httpc.responses = []*port.HTTPResponse{httpResp}
	}
	if httpErr != nil {
		httpc.errs = []error{httpErr}
	}
	logw := &stubLogWriter{}
	send := NewSendUsecase(httpc, logw, nil, logging.NewNoop())
	return NewAsyncProcessor(nr, send, dlq, nil, "nexus.async.dlq", nil, logging.NewNoop())
}

// stubCancelSet — управляемый CancelSet для тестов §34.4.
type stubCancelSet struct {
	cancelled map[string]bool
	err       error
}

func (s *stubCancelSet) IsCancelled(_ context.Context, id string) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	return s.cancelled[id], nil
}

// newAsyncProcessorWithCancel — как newAsyncProcessorForTest, но с cancel-set и
// доступом к httpc (чтобы проверить, был ли HTTP-вызов).
func newAsyncProcessorWithCancel(t *testing.T, nr NodeReader, httpResp *port.HTTPResponse, dlq DLQProducer, cancel CancelSet) (*AsyncProcessor, *stubHTTPCaller) {
	t.Helper()
	httpc := &stubHTTPCaller{}
	if httpResp != nil {
		httpc.responses = []*port.HTTPResponse{httpResp}
	}
	send := NewSendUsecase(httpc, &stubLogWriter{}, nil, logging.NewNoop())
	return NewAsyncProcessor(nr, send, dlq, cancel, "nexus.async.dlq", nil, logging.NewNoop()), httpc
}

// TestAsync_Cancelled_Ack (§34.4): отменённое сообщение → Ack без HTTP-вызова и
// без DLQ. Узел enabled и ответ был бы 5xx (→ DLQ), но cancel короткозамыкает.
func TestAsync_Cancelled_Ack(t *testing.T) {
	t.Parallel()

	node := &domain.Node{Path: "partner/echo", Status: domain.NodeStatusEnabled, TimeoutMs: 1000, ClickHouseTable: "nexus.log_x"}
	dlq := &stubDLQProducer{}
	p, httpc := newAsyncProcessorWithCancel(t,
		&stubAsyncNodeReader{node: node},
		&port.HTTPResponse{StatusCode: 502, Body: []byte("bad")},
		dlq,
		&stubCancelSet{cancelled: map[string]bool{"id-1": true}},
	)
	got := p.Handle(context.Background(), makeEnvelope(t, "partner/echo"), nil)
	assert.Equal(t, HandleAck, got, "отменённое сообщение → Ack")
	assert.Equal(t, 0, httpc.calls, "отменённое — HTTP-вызов не идёт")
	assert.Empty(t, dlq.produced, "отменённое — в DLQ не уходит")
}

// TestAsync_CancelledPausedNode_Ack (§34.4): проверка отмены идёт ДО paused —
// бэклог paused-узла можно чистить.
func TestAsync_CancelledPausedNode_Ack(t *testing.T) {
	t.Parallel()

	node := &domain.Node{Path: "partner/echo", Status: domain.NodeStatusPaused}
	p, httpc := newAsyncProcessorWithCancel(t,
		&stubAsyncNodeReader{node: node},
		nil, &stubDLQProducer{},
		&stubCancelSet{cancelled: map[string]bool{"id-1": true}},
	)
	p.pausedRetryAfter = 10 * time.Second // если бы дошли до paused — зависли бы
	got := p.Handle(context.Background(), makeEnvelope(t, "partner/echo"), nil)
	assert.Equal(t, HandleAck, got, "отменённое сообщение paused-узла → Ack (не Retry)")
	assert.Equal(t, 0, httpc.calls)
}

// TestAsync_CancelSetError_Delivers (§34.4): ошибка проверки cancel-set →
// fail-open, сообщение доставляется (2xx → Ack, HTTP вызван).
func TestAsync_CancelSetError_Delivers(t *testing.T) {
	t.Parallel()

	node := &domain.Node{Path: "partner/echo", Status: domain.NodeStatusEnabled, TimeoutMs: 1000, ClickHouseTable: "nexus.log_x"}
	p, httpc := newAsyncProcessorWithCancel(t,
		&stubAsyncNodeReader{node: node},
		&port.HTTPResponse{StatusCode: 200, Body: []byte("ok")},
		&stubDLQProducer{},
		&stubCancelSet{err: assertSomeError()},
	)
	got := p.Handle(context.Background(), makeEnvelope(t, "partner/echo"), nil)
	assert.Equal(t, HandleAck, got, "ошибка cancel-set → доставляем (fail-open), 2xx → Ack")
	assert.Equal(t, 1, httpc.calls, "fail-open: HTTP-вызов идёт")
}

func makeEnvelope(t *testing.T, nodePath string) []byte {
	t.Helper()
	env := Envelope{
		ID:         "id-1",
		NodePath:   nodePath,
		Method:     "POST",
		TargetURL:  "https://api.example.com/x",
		Body:       []byte(`{"k":"v"}`),
		ReceivedAt: time.Now().UTC(),
	}
	b, err := json.Marshal(env)
	require.NoError(t, err)
	return b
}

func TestAsync_BrokenEnvelope_Ack(t *testing.T) {
	t.Parallel()

	p := newAsyncProcessorForTest(t,
		&stubAsyncNodeReader{}, // не должен быть дёрнут
		nil, nil,
		&stubDLQProducer{},
	)
	got := p.Handle(context.Background(), []byte("not-json"), nil)
	assert.Equal(t, HandleAck, got, "битое сообщение → Ack (не повторяем)")
}

func TestAsync_UnknownNode_Ack(t *testing.T) {
	t.Parallel()

	p := newAsyncProcessorForTest(t,
		&stubAsyncNodeReader{err: domain.ErrNodeNotFound},
		nil, nil,
		&stubDLQProducer{},
	)
	got := p.Handle(context.Background(), makeEnvelope(t, "partner/echo"), nil)
	assert.Equal(t, HandleAck, got, "узел удалён → Ack, без бесконечного ретрая")
}

func TestAsync_NodeReadError_Retry(t *testing.T) {
	t.Parallel()

	p := newAsyncProcessorForTest(t,
		&stubAsyncNodeReader{err: assertSomeError()},
		nil, nil,
		&stubDLQProducer{},
	)
	got := p.Handle(context.Background(), makeEnvelope(t, "partner/echo"), nil)
	assert.Equal(t, HandleRetry, got, "временная ошибка чтения узла → Retry без commit'а")
}

func TestAsync_NodeDisabled_Ack(t *testing.T) {
	t.Parallel()

	node := &domain.Node{
		Path:   "partner/echo",
		Status: domain.NodeStatusDisabled,
	}
	p := newAsyncProcessorForTest(t,
		&stubAsyncNodeReader{node: node},
		nil, nil,
		&stubDLQProducer{},
	)
	got := p.Handle(context.Background(), makeEnvelope(t, "partner/echo"), nil)
	assert.Equal(t, HandleAck, got, "disabled-узел → дропаем сообщение")
}

func TestAsync_NodePaused_Retry(t *testing.T) {
	t.Parallel()

	node := &domain.Node{
		Path:   "partner/echo",
		Status: domain.NodeStatusPaused,
	}
	p := newAsyncProcessorForTest(t,
		&stubAsyncNodeReader{node: node},
		nil, nil,
		&stubDLQProducer{},
	)
	// pausedRetryAfter по умолчанию 30s — для теста сокращаем.
	p.pausedRetryAfter = 5 * time.Millisecond

	got := p.Handle(context.Background(), makeEnvelope(t, "partner/echo"), nil)
	assert.Equal(t, HandleRetry, got, "paused-узел → не коммитим offset, sleep+retry (§3.6)")
}

func TestAsync_NodePaused_CtxCancelInterruptsWait(t *testing.T) {
	t.Parallel()

	node := &domain.Node{
		Path:   "partner/echo",
		Status: domain.NodeStatusPaused,
	}
	p := newAsyncProcessorForTest(t,
		&stubAsyncNodeReader{node: node},
		nil, nil,
		&stubDLQProducer{},
	)
	p.pausedRetryAfter = 10 * time.Second // долгое ожидание — прервём ctx'ом

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	got := p.Handle(ctx, makeEnvelope(t, "partner/echo"), nil)
	assert.Equal(t, HandleRetry, got)
	assert.Less(t, time.Since(start), 2*time.Second,
		"отмена ctx (shutdown) должна прерывать ожидание paused-узла, а не висеть pausedRetryAfter")
}

func TestAsync_Enabled_2xx_Ack(t *testing.T) {
	t.Parallel()

	node := &domain.Node{
		Path:            "partner/echo",
		Status:          domain.NodeStatusEnabled,
		TimeoutMs:       1000,
		ClickHouseTable: "nexus.log_partner_echo",
	}
	p := newAsyncProcessorForTest(t,
		&stubAsyncNodeReader{node: node},
		&port.HTTPResponse{StatusCode: 200, Body: []byte("ok")},
		nil,
		&stubDLQProducer{},
	)
	got := p.Handle(context.Background(), makeEnvelope(t, "partner/echo"), nil)
	assert.Equal(t, HandleAck, got, "2xx → Ack, без DLQ")
}

func TestAsync_Enabled_5xx_DLQ(t *testing.T) {
	t.Parallel()

	node := &domain.Node{
		Path:            "partner/echo",
		Status:          domain.NodeStatusEnabled,
		TimeoutMs:       1000,
		RetryCount:      0, // не ретраим внутри Send для скорости
		ClickHouseTable: "nexus.log_partner_echo",
	}
	dlq := &stubDLQProducer{}
	p := newAsyncProcessorForTest(t,
		&stubAsyncNodeReader{node: node},
		&port.HTTPResponse{StatusCode: 502, Body: []byte("bad")},
		nil,
		dlq,
	)
	got := p.Handle(context.Background(), makeEnvelope(t, "partner/echo"), nil)
	assert.Equal(t, HandleDLQed, got, "после исчерпания retry → DLQ + Ack offset")

	require.Len(t, dlq.produced, 1)
	msg := dlq.produced[0]
	assert.Equal(t, "nexus.async.dlq", msg.topic)
	assert.Equal(t, "partner/echo", msg.key)
	assert.Contains(t, msg.headers["reason"], "status=502",
		"DLQ-headers должны содержать причину и статус")
	assert.NotEmpty(t, msg.headers["last_attempt_at"])
}

func TestAsync_DLQProduceFails_Retry(t *testing.T) {
	t.Parallel()

	node := &domain.Node{
		Path:            "partner/echo",
		Status:          domain.NodeStatusEnabled,
		TimeoutMs:       1000,
		ClickHouseTable: "nexus.log_partner_echo",
	}
	dlq := &stubDLQProducer{err: assertSomeError()}
	p := newAsyncProcessorForTest(t,
		&stubAsyncNodeReader{node: node},
		&port.HTTPResponse{StatusCode: 502, Body: []byte("bad")},
		nil,
		dlq,
	)
	got := p.Handle(context.Background(), makeEnvelope(t, "partner/echo"), nil)
	assert.Equal(t, HandleRetry, got,
		"если DLQ не записался — не теряем сообщение, оставляем для следующей попытки")
}

func assertSomeError() error {
	return &someErr{msg: "boom"}
}

type someErr struct{ msg string }

func (e *someErr) Error() string { return e.msg }
