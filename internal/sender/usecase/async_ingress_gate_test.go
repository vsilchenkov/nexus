package usecase

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"nexus/internal/domain"
	"nexus/internal/sender/usecase/port"
)

func gateNode(root domain.RootMethod, status domain.NodeStatus) *domain.Node {
	return &domain.Node{
		ID:              "n1",
		Path:            "acs_sigur",
		RootMethod:      root,
		Status:          status,
		URLMode:         domain.URLModeStatic,
		TargetURL:       "https://receiver.example/api",
		OutgoingMethod:  domain.HTTPMethodPOST,
		TimeoutMs:       1000,
		ClickHouseTable: "nexus_default.logs",
	}
}

func gateEnvelope(t *testing.T, ingress domain.RootMethod) []byte {
	t.Helper()
	env := Envelope{
		ID:            "msg-1",
		NodePath:      "acs_sigur",
		Method:        http.MethodPost,
		TargetURL:     "https://receiver.example/api",
		Body:          []byte(`{"logs":[{"logId":1}]}`),
		IngressMethod: string(ingress),
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestAsyncGate_RevokedNodeGoesToDLQ — §83.7: узел переведён обратно в sync,
// накопленное в очереди НЕ доставляется. До §83.7 сообщение уходило приёмнику
// как ни в чём не бывало — клиент об этом не знает и шлёт те же данные сам.
func TestAsyncGate_RevokedNodeGoesToDLQ(t *testing.T) {
	t.Parallel()

	dlq := &stubDLQProducer{}
	p := newAsyncProcessorForTest(t,
		&stubAsyncNodeReader{node: gateNode(domain.RootMethodRequest, domain.NodeStatusEnabled)},
		&port.HTTPResponse{StatusCode: 200}, nil, dlq)

	res := p.Handle(context.Background(), gateEnvelope(t, domain.RootMethodRequestAsync), nil)

	if res != HandleDLQed {
		t.Fatalf("result = %v, want HandleDLQed", res)
	}
	if len(dlq.produced) != 1 {
		t.Fatalf("want message in DLQ, got %d", len(dlq.produced))
	}
	reason := dlq.produced[0].headers["reason"]
	if !strings.Contains(reason, ReasonNodeNotAsync) {
		t.Errorf("reason = %q, want it to mention %q", reason, ReasonNodeNotAsync)
	}
	// В журнале ClickHouse тип пишется константой requestAsync, поэтому
	// реальный режим узла обязан быть в reason — иначе отбой по гейту не
	// отличить от обычной ошибки доставки.
	if !strings.Contains(reason, "root_method=request") {
		t.Errorf("reason = %q, want the current root_method in it", reason)
	}
}

// TestAsyncGate_PausedSyncNodeStillDelivers — §3.6 не сломан: сообщение
// sync-узла, попавшее в очередь на паузе, доставляется после её снятия.
// Именно ради этого случая гейт смотрит на режим приёма, а не на текущий.
func TestAsyncGate_PausedSyncNodeStillDelivers(t *testing.T) {
	t.Parallel()

	dlq := &stubDLQProducer{}
	p := newAsyncProcessorForTest(t,
		&stubAsyncNodeReader{node: gateNode(domain.RootMethodRequest, domain.NodeStatusEnabled)},
		&port.HTTPResponse{StatusCode: 200}, nil, dlq)

	res := p.Handle(context.Background(), gateEnvelope(t, domain.RootMethodRequest), nil)

	if res != HandleAck {
		t.Fatalf("result = %v, want HandleAck (§3.6 message must be delivered)", res)
	}
	if len(dlq.produced) != 0 {
		t.Fatalf("§3.6 message must not go to DLQ, got %d", len(dlq.produced))
	}
}

// TestAsyncGate_AsyncNodeUnaffected — узел остался async: поведение прежнее.
func TestAsyncGate_AsyncNodeUnaffected(t *testing.T) {
	t.Parallel()

	dlq := &stubDLQProducer{}
	p := newAsyncProcessorForTest(t,
		&stubAsyncNodeReader{node: gateNode(domain.RootMethodRequestAsync, domain.NodeStatusEnabled)},
		&port.HTTPResponse{StatusCode: 200}, nil, dlq)

	res := p.Handle(context.Background(), gateEnvelope(t, domain.RootMethodRequestAsync), nil)

	if res != HandleAck {
		t.Fatalf("result = %v, want HandleAck", res)
	}
	if len(dlq.produced) != 0 {
		t.Fatalf("no DLQ expected, got %d", len(dlq.produced))
	}
}

// TestAsyncGate_LegacyEnvelopeDelivers — сообщения, принятые ДО §83 (без
// ingress_method), доставляются как раньше: выкат не должен обнулить очередь.
func TestAsyncGate_LegacyEnvelopeDelivers(t *testing.T) {
	t.Parallel()

	dlq := &stubDLQProducer{}
	p := newAsyncProcessorForTest(t,
		&stubAsyncNodeReader{node: gateNode(domain.RootMethodRequest, domain.NodeStatusEnabled)},
		&port.HTTPResponse{StatusCode: 200}, nil, dlq)

	res := p.Handle(context.Background(), gateEnvelope(t, ""), nil)

	if res != HandleAck {
		t.Fatalf("result = %v, want HandleAck for a pre-83 envelope", res)
	}
	if len(dlq.produced) != 0 {
		t.Fatalf("pre-83 envelope must not be dropped, got %d", len(dlq.produced))
	}
}

// TestAsyncGate_RevokedBeforePause — гейт стоит ДО paused-ветки: решение
// терминальное, откладывать в delay-топик нечего.
func TestAsyncGate_RevokedBeforePause(t *testing.T) {
	t.Parallel()

	dlq := &stubDLQProducer{}
	p := newAsyncProcessorForTest(t,
		&stubAsyncNodeReader{node: gateNode(domain.RootMethodRequest, domain.NodeStatusPaused)},
		&port.HTTPResponse{StatusCode: 200}, nil, dlq)

	res := p.Handle(context.Background(), gateEnvelope(t, domain.RootMethodRequestAsync), nil)

	if res != HandleDLQed {
		t.Fatalf("result = %v, want HandleDLQed (gate wins over pause)", res)
	}
	if len(dlq.produced) != 1 {
		t.Fatalf("want exactly one DLQ message, got %d", len(dlq.produced))
	}
	if topic := dlq.produced[0].topic; topic != "nexus.async.dlq" {
		t.Errorf("topic = %q, want the DLQ topic and not the delay topic", topic)
	}
}

// TestDLQGate_RevokedNodeIsNotDelivered — §83.7: репроцессор DLQ обязан
// уважать тот же гейт. Иначе он обошёл бы проверку consumer'а с другой стороны
// и доставил ровно то, что тот отбил.
func TestDLQGate_RevokedNodeIsNotDelivered(t *testing.T) {
	t.Parallel()

	h := newReprocessorForTest(t,
		gateNode(domain.RootMethodRequest, domain.NodeStatusEnabled), nil,
		&port.HTTPResponse{StatusCode: 200}, nil, nil)

	res := h.proc.ProcessMessage(context.Background(),
		gateEnvelope(t, domain.RootMethodRequestAsync), map[string]string{"id": "msg-1"})

	if res != ReprocessCommit {
		t.Fatalf("result = %v, want ReprocessCommit (republished, not delivered)", res)
	}
	if h.httpc.calls != 0 {
		t.Fatalf("receiver must NOT be called, got %d calls", h.httpc.calls)
	}
	if len(h.dlq.produced) != 1 {
		t.Fatalf("message must stay in DLQ, got %d", len(h.dlq.produced))
	}
	if reason := h.dlq.produced[0].headers["reason"]; !strings.Contains(reason, ReasonNodeNotAsync) {
		t.Errorf("reason = %q, want it to mention %q", reason, ReasonNodeNotAsync)
	}
}

// TestDLQGate_PausedSyncNodeIsUnaffected — §3.6-сообщение sync-узла из DLQ
// доставляется как раньше.
func TestDLQGate_PausedSyncNodeIsUnaffected(t *testing.T) {
	t.Parallel()

	h := newReprocessorForTest(t,
		gateNode(domain.RootMethodRequest, domain.NodeStatusEnabled), nil,
		&port.HTTPResponse{StatusCode: 200}, nil, nil)

	res := h.proc.ProcessMessage(context.Background(),
		gateEnvelope(t, domain.RootMethodRequest), map[string]string{"id": "msg-1"})

	if res != ReprocessCommit {
		t.Fatalf("result = %v", res)
	}
	if h.httpc.calls != 1 {
		t.Fatalf("§3.6 message must be delivered, got %d calls", h.httpc.calls)
	}
}
