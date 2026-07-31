package usecase

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/clock"
	"nexus/internal/platform/logging"
	"nexus/internal/sender/usecase/port"
)

// stubBreakerInspector — программируемый BreakerInspector.
type stubBreakerInspector struct {
	open bool
	err  error
}

func (s *stubBreakerInspector) IsOpen(_ context.Context, _ string) (bool, error) {
	return s.open, s.err
}

// reprocessorHarness собирает DLQReprocessor с доступом к stubs для проверок.
type reprocessorHarness struct {
	proc    *DLQReprocessor
	httpc   *stubHTTPCaller
	logw    *stubLogWriter
	dlq     *stubDLQProducer
	breaker *stubBreakerInspector
}

func newReprocessorForTest(t *testing.T, node *domain.Node, nodeErr error, httpResp *port.HTTPResponse, cancel CancelSet, breaker *stubBreakerInspector) *reprocessorHarness {
	t.Helper()
	httpc := &stubHTTPCaller{}
	if httpResp != nil {
		httpc.responses = []*port.HTTPResponse{httpResp}
	}
	logw := &stubLogWriter{}
	dlq := &stubDLQProducer{}
	send := NewSendUsecase(httpc, logw, nil, logging.NewNoop(), 64<<20)
	var bi BreakerInspector
	if breaker != nil {
		bi = breaker
	}
	proc := NewDLQReprocessor(
		&stubAsyncNodeReader{node: node, err: nodeErr},
		send, dlq, logw, cancel, bi, "nexus.async.dlq", nil, logging.NewNoop(),
	)
	return &reprocessorHarness{proc: proc, httpc: httpc, logw: logw, dlq: dlq, breaker: breaker}
}

// makeDLQEnvelope собирает DLQ-сообщение с управляемым received_at.
func makeDLQEnvelope(t *testing.T, nodePath string, receivedAt time.Time) []byte {
	t.Helper()
	env := Envelope{
		ID:         "id-1",
		NodePath:   nodePath,
		Method:     "POST",
		TargetURL:  "https://api.example.com/x?token=secret",
		Body:       []byte(`{"k":"v"}`),
		ReceivedAt: receivedAt,
	}
	b, err := json.Marshal(env)
	require.NoError(t, err)
	return b
}

func enabledNode() *domain.Node {
	return &domain.Node{
		Path:                 "partner/echo",
		Status:               domain.NodeStatusEnabled,
		TimeoutMs:            1000,
		RetryCount:           0,
		ClickHouseTable:      "nexus.log_partner_echo",
		LoggingEnabled:       true,
		DLQTTLSeconds:        86_400,
		DLQRetryDelaySeconds: 300,
	}
}

func TestReprocess_BrokenEnvelope_Commit(t *testing.T) {
	t.Parallel()
	h := newReprocessorForTest(t, enabledNode(), nil, nil, nil, nil)
	got := h.proc.ProcessMessage(context.Background(), []byte("not-json"), nil)
	assert.Equal(t, ReprocessCommit, got, "битое сообщение → commit (не зацикливаем)")
	assert.Equal(t, 0, h.httpc.calls)
	assert.Empty(t, h.dlq.produced)
}

func TestReprocess_UnknownNode_Commit(t *testing.T) {
	t.Parallel()
	h := newReprocessorForTest(t, nil, domain.ErrNodeNotFound, nil, nil, nil)
	got := h.proc.ProcessMessage(context.Background(), makeDLQEnvelope(t, "partner/echo", time.Now()), nil)
	assert.Equal(t, ReprocessCommit, got, "узел удалён → drop (commit)")
	assert.Empty(t, h.dlq.produced)
}

func TestReprocess_NodeReadError_Retry(t *testing.T) {
	t.Parallel()
	h := newReprocessorForTest(t, nil, assertSomeError(), nil, nil, nil)
	got := h.proc.ProcessMessage(context.Background(), makeDLQEnvelope(t, "partner/echo", time.Now()), nil)
	assert.Equal(t, ReprocessRetry, got, "транзиентная ошибка чтения узла → Retry (не коммитим)")
}

func TestReprocess_Cancelled_Commit(t *testing.T) {
	t.Parallel()
	cancel := &stubCancelSet{cancelled: map[string]bool{"id-1": true}}
	h := newReprocessorForTest(t, enabledNode(), nil, &port.HTTPResponse{StatusCode: 200}, cancel, nil)
	got := h.proc.ProcessMessage(context.Background(), makeDLQEnvelope(t, "partner/echo", time.Now()), nil)
	assert.Equal(t, ReprocessCommit, got, "отменённое оператором → drop без попытки")
	assert.Equal(t, 0, h.httpc.calls, "отменённое — HTTP-вызов не идёт")
	assert.Empty(t, h.dlq.produced)
}

func TestReprocess_TTLExpired_FinalLogAndCommit(t *testing.T) {
	t.Parallel()
	node := enabledNode()
	node.DLQTTLSeconds = 3600 // 1ч
	h := newReprocessorForTest(t, node, nil, &port.HTTPResponse{StatusCode: 200}, nil, nil)
	// received_at 2ч назад > TTL 1ч → терминальный отказ.
	received := time.Now().Add(-2 * time.Hour)
	got := h.proc.ProcessMessage(context.Background(), makeDLQEnvelope(t, "partner/echo", received), nil)
	assert.Equal(t, ReprocessCommit, got, "истёк TTL → drop (commit)")
	assert.Equal(t, 0, h.httpc.calls, "по TTL попытка доставки не делается")
	assert.Empty(t, h.dlq.produced, "по TTL republish прекращается")
	require.Len(t, h.logw.written, 1, "по TTL пишется финальная запись в CH")
	rec := h.logw.written[0].rec
	assert.False(t, rec.Done)
	assert.Equal(t, "ttl_expired", rec.Reason)
	assert.Equal(t, received.Unix(), rec.DateCreate.Unix(), "даты записи — от received_at")
}

func TestReprocess_TTLExpired_LoggingDisabled_NoCHWrite(t *testing.T) {
	t.Parallel()
	node := enabledNode()
	node.DLQTTLSeconds = 3600
	node.LoggingEnabled = false
	h := newReprocessorForTest(t, node, nil, nil, nil, nil)
	got := h.proc.ProcessMessage(context.Background(), makeDLQEnvelope(t, "partner/echo", time.Now().Add(-2*time.Hour)), nil)
	assert.Equal(t, ReprocessCommit, got)
	assert.Empty(t, h.logw.written, "logging выключен → финальную запись не пишем")
}

func TestReprocess_ZeroReceivedAt_NoTTLDrop(t *testing.T) {
	t.Parallel()
	node := enabledNode()
	node.DLQTTLSeconds = 60
	h := newReprocessorForTest(t, node, nil, &port.HTTPResponse{StatusCode: 200, Body: []byte("ok")}, nil, nil)
	// received_at нулевой (старое сообщение) → TTL не применяем, доставляем.
	got := h.proc.ProcessMessage(context.Background(), makeDLQEnvelope(t, "partner/echo", time.Time{}), nil)
	assert.Equal(t, ReprocessCommit, got)
	assert.Equal(t, 1, h.httpc.calls, "нулевой received_at → TTL пропущен, попытка доставки идёт")
}

func TestReprocess_Disabled_Commit(t *testing.T) {
	t.Parallel()
	node := enabledNode()
	node.Status = domain.NodeStatusDisabled
	h := newReprocessorForTest(t, node, nil, nil, nil, nil)
	got := h.proc.ProcessMessage(context.Background(), makeDLQEnvelope(t, "partner/echo", time.Now()), nil)
	assert.Equal(t, ReprocessCommit, got, "disabled → drop (commit)")
	assert.Empty(t, h.dlq.produced)
	assert.Equal(t, 0, h.httpc.calls)
}

func TestReprocess_Paused_RepublishSkipped(t *testing.T) {
	t.Parallel()
	node := enabledNode()
	node.Status = domain.NodeStatusPaused
	h := newReprocessorForTest(t, node, nil, nil, nil, nil)
	got := h.proc.ProcessMessage(context.Background(), makeDLQEnvelope(t, "partner/echo", time.Now()), map[string]string{"attempts": "2"})
	assert.Equal(t, ReprocessCommit, got, "paused → republish (commit старой копии)")
	assert.Equal(t, 0, h.httpc.calls, "paused → без попытки доставки")
	require.Len(t, h.dlq.produced, 1)
	msg := h.dlq.produced[0]
	assert.Equal(t, "nexus.async.dlq", msg.topic)
	assert.Equal(t, "partner/echo", msg.key)
	assert.Equal(t, "node_paused", msg.headers["reason"])
	assert.Equal(t, "3", msg.headers["attempts"], "attempts инкрементится")
	assert.NotEmpty(t, msg.headers["last_attempt_at"])
}

func TestReprocess_BreakerOpen_RepublishSkipped(t *testing.T) {
	t.Parallel()
	h := newReprocessorForTest(t, enabledNode(), nil, &port.HTTPResponse{StatusCode: 200}, nil, &stubBreakerInspector{open: true})
	got := h.proc.ProcessMessage(context.Background(), makeDLQEnvelope(t, "partner/echo", time.Now()), nil)
	assert.Equal(t, ReprocessCommit, got, "breaker открыт → republish без попытки")
	assert.Equal(t, 0, h.httpc.calls, "breaker открыт → HTTP-вызов не идёт (нет fast-fail-churn)")
	require.Len(t, h.dlq.produced, 1)
	assert.Equal(t, "circuit_breaker_open", h.dlq.produced[0].headers["reason"])
	assert.Equal(t, "1", h.dlq.produced[0].headers["attempts"], "attempts с нуля → 1")
}

func TestReprocess_BreakerCheckError_Attempts(t *testing.T) {
	t.Parallel()
	// Ошибка проверки breaker → fail-open: пытаемся доставить.
	h := newReprocessorForTest(t, enabledNode(), nil, &port.HTTPResponse{StatusCode: 200, Body: []byte("ok")}, nil, &stubBreakerInspector{err: assertSomeError()})
	got := h.proc.ProcessMessage(context.Background(), makeDLQEnvelope(t, "partner/echo", time.Now()), nil)
	assert.Equal(t, ReprocessCommit, got)
	assert.Equal(t, 1, h.httpc.calls, "ошибка breaker-проверки → fail-open, попытка доставки идёт")
}

func TestReprocess_Delivered_Commit(t *testing.T) {
	t.Parallel()
	h := newReprocessorForTest(t, enabledNode(), nil, &port.HTTPResponse{StatusCode: 200, Body: []byte("ok")}, nil, &stubBreakerInspector{open: false})
	got := h.proc.ProcessMessage(context.Background(), makeDLQEnvelope(t, "partner/echo", time.Now()), nil)
	assert.Equal(t, ReprocessCommit, got, "2xx → успех, commit без republish")
	assert.Equal(t, 1, h.httpc.calls)
	assert.Empty(t, h.dlq.produced, "успех → не republish-им")
}

func TestReprocess_Failed_Republish(t *testing.T) {
	t.Parallel()
	h := newReprocessorForTest(t, enabledNode(), nil, &port.HTTPResponse{StatusCode: 502, Body: []byte("bad")}, nil, nil)
	got := h.proc.ProcessMessage(context.Background(), makeDLQEnvelope(t, "partner/echo", time.Now()), map[string]string{"attempts": "0", "orig_topic": "nexus.async"})
	assert.Equal(t, ReprocessCommit, got, "не-2xx → republish + commit старой копии")
	require.Len(t, h.dlq.produced, 1)
	msg := h.dlq.produced[0]
	assert.Contains(t, msg.headers["reason"], "status=502")
	assert.Equal(t, "1", msg.headers["attempts"])
	assert.Equal(t, "nexus.async", msg.headers["orig_topic"], "orig_topic сохраняется")
	// §36.4: неудача → next_attempt_at = now + dlq_retry_delay_seconds.
	require.NotEmpty(t, msg.headers["next_attempt_at"], "неудача проставляет next_attempt_at")
	next, err := time.Parse(time.RFC3339Nano, msg.headers["next_attempt_at"])
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(300*time.Second), next, 5*time.Second,
		"next_attempt_at ≈ now + 300с (dlq_retry_delay_seconds)")
}

// TestReprocess_NotDueYet_RepublishSkipped (§36.4): сообщение с next_attempt_at
// в будущем не доставляется — republish без попытки, next_attempt_at сохраняется,
// attempts НЕ инкрементится (реальной попытки не было).
func TestReprocess_NotDueYet_RepublishSkipped(t *testing.T) {
	t.Parallel()
	h := newReprocessorForTest(t, enabledNode(), nil, &port.HTTPResponse{StatusCode: 200}, nil, nil)
	future := time.Now().Add(45 * time.Second).UTC().Format(time.RFC3339Nano)
	in := map[string]string{"next_attempt_at": future, "attempts": "3"}
	got := h.proc.ProcessMessage(context.Background(), makeDLQEnvelope(t, "partner/echo", time.Now()), in)
	assert.Equal(t, ReprocessCommit, got, "не наступило время → republish + commit")
	assert.Equal(t, 0, h.httpc.calls, "не наступило время → без попытки доставки")
	require.Len(t, h.dlq.produced, 1)
	msg := h.dlq.produced[0]
	assert.Equal(t, "retry_backoff", msg.headers["reason"])
	assert.Equal(t, "3", msg.headers["attempts"], "дефер не инкрементит attempts")
	assert.Equal(t, future, msg.headers["next_attempt_at"], "next_attempt_at сохраняется")
}

// TestReprocess_DueNow_Attempts (§36.4): next_attempt_at в прошлом → доставляем.
func TestReprocess_DueNow_Attempts(t *testing.T) {
	t.Parallel()
	h := newReprocessorForTest(t, enabledNode(), nil, &port.HTTPResponse{StatusCode: 200, Body: []byte("ok")}, nil, nil)
	past := time.Now().Add(-1 * time.Second).UTC().Format(time.RFC3339Nano)
	in := map[string]string{"next_attempt_at": past, "attempts": "2"}
	got := h.proc.ProcessMessage(context.Background(), makeDLQEnvelope(t, "partner/echo", time.Now()), in)
	assert.Equal(t, ReprocessCommit, got)
	assert.Equal(t, 1, h.httpc.calls, "время наступило → попытка доставки идёт")
	assert.Empty(t, h.dlq.produced, "2xx → без republish")
}

func TestReprocess_RepublishProduceFails_Retry(t *testing.T) {
	t.Parallel()
	h := newReprocessorForTest(t, enabledNode(), nil, &port.HTTPResponse{StatusCode: 502}, nil, nil)
	h.dlq.err = assertSomeError() // republish не записался
	got := h.proc.ProcessMessage(context.Background(), makeDLQEnvelope(t, "partner/echo", time.Now()), nil)
	assert.Equal(t, ReprocessRetry, got, "republish не записался → Retry (не теряем сообщение)")
}

// TestReprocess_NowInjection — TTL считается через инъектируемый now.
func TestReprocess_NowInjection(t *testing.T) {
	t.Parallel()
	node := enabledNode()
	node.DLQTTLSeconds = 3600
	h := newReprocessorForTest(t, node, nil, &port.HTTPResponse{StatusCode: 200, Body: []byte("ok")}, nil, nil)
	received := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	// now = received + 30 мин < TTL 1ч → не истёк, доставляем.
	h.proc.clock = clock.Fixed(received.Add(30 * time.Minute))
	got := h.proc.ProcessMessage(context.Background(), makeDLQEnvelope(t, "partner/echo", received), nil)
	assert.Equal(t, ReprocessCommit, got)
	assert.Equal(t, 1, h.httpc.calls, "в пределах TTL → попытка доставки")
}
