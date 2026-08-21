package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	send := NewSendUsecase(httpc, logw, nil, logging.NewNoop(), 64<<20)
	return NewAsyncProcessor(nr, send, dlq, nil, nil, "nexus.async.dlq", nil, logging.NewNoop())
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
	send := NewSendUsecase(httpc, &stubLogWriter{}, nil, logging.NewNoop(), 64<<20)
	return NewAsyncProcessor(nr, send, dlq, cancel, nil, "nexus.async.dlq", nil, logging.NewNoop()), httpc
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

// TestAsync_WithPausedRetryAfter: опция переопределяет паузу перед Retry для
// paused-узлов. Нужна integration-тестам — иначе каждое сообщение paused-узла
// держит партицию дефолтные 30s. Некорректное значение игнорируется.
func TestAsync_WithPausedRetryAfter(t *testing.T) {
	t.Parallel()

	node := &domain.Node{Path: "partner/echo", Status: domain.NodeStatusPaused}
	httpc := &stubHTTPCaller{}
	send := NewSendUsecase(httpc, &stubLogWriter{}, nil, logging.NewNoop(), 64<<20)
	p := NewAsyncProcessor(&stubAsyncNodeReader{node: node}, send, &stubDLQProducer{},
		nil, nil, "nexus.async.dlq", nil, logging.NewNoop(),
		WithPausedRetryAfter(5*time.Millisecond),
		WithPausedRetryAfter(0), // некорректное — не затирает предыдущее
	)

	start := time.Now()
	got := p.Handle(context.Background(), makeEnvelope(t, "partner/echo"), nil)

	assert.Equal(t, HandleRetry, got)
	assert.Less(t, time.Since(start), time.Second,
		"опция должна была сократить ожидание с дефолтных 30s")
}

// newPausedRequeueProcessor — processor с включённым delay-топиком (§3.6).
func newPausedRequeueProcessor(t *testing.T, node *domain.Node, dlq DLQProducer) (*AsyncProcessor, *stubHTTPCaller) {
	t.Helper()
	httpc := &stubHTTPCaller{}
	send := NewSendUsecase(httpc, &stubLogWriter{}, nil, logging.NewNoop(), 64<<20)
	p := NewAsyncProcessor(&stubAsyncNodeReader{node: node}, send, dlq, nil, nil,
		"nexus.async.dlq", nil, logging.NewNoop(),
		WithPausedRequeue("nexus.async.paused"))
	return p, httpc
}

// TestAsync_Paused_RequeuedToDelayTopic (§3.6): сообщение paused-узла уезжает в
// delay-топик и offset основного топика коммитится — партиция освобождается для
// соседних узлов (в неё по хешу node_path попадают и другие узлы).
func TestAsync_Paused_RequeuedToDelayTopic(t *testing.T) {
	t.Parallel()

	node := &domain.Node{Path: "partner/echo", Status: domain.NodeStatusPaused}
	dlq := &stubDLQProducer{}
	p, httpc := newPausedRequeueProcessor(t, node, dlq)
	p.pausedRetryAfter = time.Hour // если бы ждали на месте — тест бы завис

	start := time.Now()
	got := p.Handle(context.Background(), makeEnvelope(t, "partner/echo"), nil)

	assert.Equal(t, HandleRequeued, got, "перенос в delay-топик → commit основного offset'а")
	assert.Less(t, time.Since(start), time.Second, "с delay-топиком ожидания на месте нет")
	assert.Zero(t, httpc.calls, "paused-узел не получает трафик")

	require.Len(t, dlq.produced, 1)
	msg := dlq.produced[0]
	assert.Equal(t, "nexus.async.paused", msg.topic)
	assert.Equal(t, "partner/echo", msg.key, "ключ = путь узла: порядок бэклога узла сохраняется")
	assert.Equal(t, "id-1", msg.headers["id"])
	assert.Equal(t, "nexus.async", msg.headers["orig_topic"])
	assert.NotEmpty(t, msg.headers["paused_since"])
	assert.NotEmpty(t, msg.headers["last_requeue_at"])
}

// TestAsync_Paused_KeepsFirstPausedSince: сообщение циркулирует в delay-топике,
// пока узел на паузе. paused_since — метка ПЕРВОГО откладывания, её нельзя
// затирать на каждом круге, иначе возраст бэклога не отследить.
func TestAsync_Paused_KeepsFirstPausedSince(t *testing.T) {
	t.Parallel()

	node := &domain.Node{Path: "partner/echo", Status: domain.NodeStatusPaused}
	dlq := &stubDLQProducer{}
	p, _ := newPausedRequeueProcessor(t, node, dlq)

	const firstSeen = "2026-07-01T10:00:00Z"
	in := map[string]string{"paused_since": firstSeen, "orig_topic": "nexus.async"}
	got := p.Handle(context.Background(), makeEnvelope(t, "partner/echo"), in)

	require.Equal(t, HandleRequeued, got)
	require.Len(t, dlq.produced, 1)
	assert.Equal(t, firstSeen, dlq.produced[0].headers["paused_since"],
		"метка первого откладывания переносится как есть")
	assert.NotEqual(t, firstSeen, dlq.produced[0].headers["last_requeue_at"],
		"last_requeue_at обновляется на каждом круге")
}

// TestAsync_Paused_RequeueFails_Retry: если delay-топик недоступен, offset НЕ
// коммитим — сообщение переобработается на месте (потеря исключена).
func TestAsync_Paused_RequeueFails_Retry(t *testing.T) {
	t.Parallel()

	node := &domain.Node{Path: "partner/echo", Status: domain.NodeStatusPaused}
	p, _ := newPausedRequeueProcessor(t, node, &stubDLQProducer{err: assertSomeError()})

	got := p.Handle(context.Background(), makeEnvelope(t, "partner/echo"), nil)
	assert.Equal(t, HandleRetry, got, "не переложили — не коммитим")
}

// TestAsync_Paused_CancelledBeforeRequeue (§34.4): отменённое оператором
// сообщение paused-узла дропается, а не копится в delay-топике.
func TestAsync_Paused_CancelledBeforeRequeue(t *testing.T) {
	t.Parallel()

	node := &domain.Node{Path: "partner/echo", Status: domain.NodeStatusPaused}
	dlq := &stubDLQProducer{}
	httpc := &stubHTTPCaller{}
	send := NewSendUsecase(httpc, &stubLogWriter{}, nil, logging.NewNoop(), 64<<20)
	p := NewAsyncProcessor(&stubAsyncNodeReader{node: node}, send, dlq,
		&stubCancelSet{cancelled: map[string]bool{"id-1": true}}, nil,
		"nexus.async.dlq", nil, logging.NewNoop(),
		WithPausedRequeue("nexus.async.paused"))

	got := p.Handle(context.Background(), makeEnvelope(t, "partner/echo"), nil)

	assert.Equal(t, HandleAck, got, "отменённое → drop (проверка отмены идёт до paused)")
	assert.Empty(t, dlq.produced, "в delay-топик отменённое не попадает")
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

// stubNodeStatus фиксирует вызовы NodeStatusWriter (§46/§52). Handle синхронен —
// запись происходит на вызывающей горутине, мьютекс не нужен.
type stubNodeStatus struct {
	calls []nodeStatusRec
}

type nodeStatusRec struct {
	path    string
	outcome domain.NodeOutcome
}

// SetLastOutcome возвращает исход как есть: порог подряд идущих отказов (§52-доп)
// живёт в Redis-реализации.
func (s *stubNodeStatus) SetLastOutcome(_ context.Context, path string, outcome domain.NodeOutcome) domain.NodeOutcome {
	s.calls = append(s.calls, nodeStatusRec{path: path, outcome: outcome})
	return outcome
}

// TestAsync_WritesNodeStatus (§46/§52): async-обработка пишет исход последнего
// вызова в NodeStatusWriter — ok на 2xx, degraded на 3xx/4xx (узел жив, §50.4),
// down на 5xx и транспортной ошибке. Решение ack/DLQ от §52 не зависит:
// любой не-2xx по-прежнему уходит в DLQ.
func TestAsync_WritesNodeStatus(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		status      int32
		callErr     error
		wantOutcome domain.NodeOutcome
		wantDLQ     bool
	}{
		{"2xx → ok", 200, nil, domain.NodeOutcomeOK, false},
		{"3xx → degraded", 302, nil, domain.NodeOutcomeDegraded, true},
		{"4xx (422, §50.4) → degraded", 422, nil, domain.NodeOutcomeDegraded, true},
		{"5xx → down", 502, nil, domain.NodeOutcomeDown, true},
		{"транспортная ошибка → down", 0, assertSomeError(), domain.NodeOutcomeDown, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			node := &domain.Node{
				Path:            "partner/echo",
				Status:          domain.NodeStatusEnabled,
				TimeoutMs:       1000,
				RetryCount:      0,
				ClickHouseTable: "nexus.log_partner_echo",
			}
			ns := &stubNodeStatus{}
			httpc := &stubHTTPCaller{}
			if tc.callErr != nil {
				httpc.errs = []error{tc.callErr}
			} else {
				httpc.responses = []*port.HTTPResponse{{StatusCode: tc.status, Body: []byte("x")}}
			}
			dlq := &stubDLQProducer{}
			send := NewSendUsecase(httpc, &stubLogWriter{}, nil, logging.NewNoop(), 64<<20)
			p := NewAsyncProcessor(&stubAsyncNodeReader{node: node}, send, dlq,
				nil, ns, "nexus.async.dlq", nil, logging.NewNoop())

			p.Handle(context.Background(), makeEnvelope(t, "partner/echo"), nil)

			require.Len(t, ns.calls, 1, "§46: SetLastOutcome вызывается ровно раз на сообщение")
			assert.Equal(t, "partner/echo", ns.calls[0].path)
			assert.Equal(t, tc.wantOutcome, ns.calls[0].outcome)
			if tc.wantDLQ {
				assert.Len(t, dlq.produced, 1, "не-2xx уходит в DLQ — §52 это не меняет")
			} else {
				assert.Empty(t, dlq.produced, "успешная доставка не пишет в DLQ")
			}
		})
	}
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

// TestAsync_PausedRequeue_TooLarge_Ack: сообщение, которое НЕ ВЛЕЗАЕТ в
// delay-топик, отпускается (ack), а не крутится вечно.
//
// Так выглядит наследство завышенного async-лимита: тело приняли старой
// версией, оно легло в nexus.async впритык, а при перекладывании добавились
// служебные заголовки — и брокер отвечает «Message Size Too Large» НАВСЕГДА.
// Retry-in-place здесь не «пробует ещё раз», а останавливает всю партицию:
// offset не двигается, и следующие сообщения не обрабатываются вовсе.
func TestAsync_PausedRequeue_TooLarge_Ack(t *testing.T) {
	t.Parallel()

	node := &domain.Node{Path: "partner/echo", Status: domain.NodeStatusPaused}
	dlq := &stubDLQProducer{err: fmt.Errorf("write to nexus.async.paused: %w: broker said no",
		domain.ErrMessageTooLargeForTopic)}
	httpc := &stubHTTPCaller{}
	send := NewSendUsecase(httpc, &stubLogWriter{}, nil, logging.NewNoop(), 64<<20)
	p := NewAsyncProcessor(&stubAsyncNodeReader{node: node}, send, dlq,
		nil, nil, "nexus.async.dlq", nil, logging.NewNoop(),
		WithPausedRequeue("nexus.async.paused"))

	got := p.Handle(context.Background(), makeEnvelope(t, "partner/echo"), nil)
	assert.Equal(t, HandleAck, got,
		"постоянная ошибка размера → отпускаем сообщение, иначе встаёт партиция")
}

// TestAsync_PausedRequeue_TransientError_Retry: временная ошибка брокера
// по-прежнему означает retry — потеря недопустима, когда повтор может помочь.
func TestAsync_PausedRequeue_TransientError_Retry(t *testing.T) {
	t.Parallel()

	node := &domain.Node{Path: "partner/echo", Status: domain.NodeStatusPaused}
	dlq := &stubDLQProducer{err: errors.New("write to nexus.async.paused: broker unavailable")}
	httpc := &stubHTTPCaller{}
	send := NewSendUsecase(httpc, &stubLogWriter{}, nil, logging.NewNoop(), 64<<20)
	p := NewAsyncProcessor(&stubAsyncNodeReader{node: node}, send, dlq,
		nil, nil, "nexus.async.dlq", nil, logging.NewNoop(),
		WithPausedRequeue("nexus.async.paused"))

	got := p.Handle(context.Background(), makeEnvelope(t, "partner/echo"), nil)
	assert.Equal(t, HandleRetry, got, "недоступность брокера — временная, повтор обязан остаться")
}
