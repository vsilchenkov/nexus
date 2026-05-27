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
	return NewAsyncProcessor(nr, send, dlq, "nexus.async.dlq", nil, logging.NewNoop())
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
