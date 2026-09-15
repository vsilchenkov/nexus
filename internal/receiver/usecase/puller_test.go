package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/metrics"
)

// --- fakes -------------------------------------------------------------------

type fakeConsumer struct {
	mu       sync.Mutex
	batches  [][]RMQDelivery
	acked    []uint64
	nacked   []uint64
	rejected []uint64
}

func (f *fakeConsumer) GetBatch(_ context.Context, _ int) ([]RMQDelivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.batches) == 0 {
		return nil, nil
	}
	b := f.batches[0]
	f.batches = f.batches[1:]
	return b, nil
}
func (f *fakeConsumer) Ack(tag uint64) error { f.acked = append(f.acked, tag); return nil }
func (f *fakeConsumer) Nack(tag uint64, _ bool) error {
	f.nacked = append(f.nacked, tag)
	return nil
}
func (f *fakeConsumer) Reject(tag uint64, _ bool) error {
	f.rejected = append(f.rejected, tag)
	return nil
}
func (f *fakeConsumer) QueueStats() (int, int, error) { return 0, 0, nil }
func (f *fakeConsumer) Close() error                  { return nil }

type fakeProducer struct {
	mu        sync.Mutex
	published [][]byte
	headers   []map[string]string
	err       error
}

func (f *fakeProducer) Produce(_ context.Context, _, _ string, value []byte, headers map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.published = append(f.published, value)
	f.headers = append(f.headers, headers)
	return nil
}

func testNode() *domain.Node {
	n := &domain.Node{
		Path:       "billing-events",
		RootMethod: domain.RootMethodRabbitMQAsync,
		TargetURL:  "https://api.partner.com/webhook",
		RMQHost:    "rmq.internal",
		RMQQueue:   "billing.events",
	}
	n.SetDefaults()
	return n
}

func newTestWorker(cons RMQConsumer, prod AsyncProducer, maxBytes int) *PullerWorker {
	w := NewPullerWorker(testNode(), nil, prod, "nexus.async", maxBytes,
		metrics.New("receiver"), nil, logging.NewNoop())
	w.consumer = cons // подставляем уже открытый consumer, минуя connector
	return w
}

// --- tests -------------------------------------------------------------------

func TestPullerWorker_HappyPath_PublishThenAck(t *testing.T) {
	cons := &fakeConsumer{batches: [][]RMQDelivery{{
		{DeliveryTag: 1, Body: []byte(`{"x":1}`), RoutingKey: "order.created", MessageID: "m1"},
	}}}
	prod := &fakeProducer{}
	w := newTestWorker(cons, prod, 0)

	w.pullOnce(context.Background())

	if len(prod.published) != 1 {
		t.Fatalf("want 1 published, got %d", len(prod.published))
	}
	if len(cons.acked) != 1 || cons.acked[0] != 1 {
		t.Fatalf("want ack of tag 1, got %v", cons.acked)
	}
	if len(cons.nacked) != 0 || len(cons.rejected) != 0 {
		t.Fatalf("unexpected nack/reject: %v %v", cons.nacked, cons.rejected)
	}
}

func TestPullerWorker_KafkaDown_NackRequeue(t *testing.T) {
	cons := &fakeConsumer{batches: [][]RMQDelivery{{
		{DeliveryTag: 7, Body: []byte("payload")},
	}}}
	prod := &fakeProducer{err: errors.New("kafka unavailable")}
	w := newTestWorker(cons, prod, 0)

	w.pullOnce(context.Background())

	if len(cons.nacked) != 1 || cons.nacked[0] != 7 {
		t.Fatalf("want nack(requeue) of tag 7, got %v", cons.nacked)
	}
	if len(cons.acked) != 0 {
		t.Fatalf("must not ack when kafka publish failed: %v", cons.acked)
	}
}

func TestPullerWorker_Oversize_Reject(t *testing.T) {
	big := make([]byte, 2048)
	cons := &fakeConsumer{batches: [][]RMQDelivery{{
		{DeliveryTag: 3, Body: big},
	}}}
	prod := &fakeProducer{}
	w := newTestWorker(cons, prod, 512) // лимит меньше payload

	w.pullOnce(context.Background())

	if len(cons.rejected) != 1 || cons.rejected[0] != 3 {
		t.Fatalf("want reject of oversize tag 3, got %v", cons.rejected)
	}
	if len(prod.published) != 0 {
		t.Fatalf("oversize message must not be published")
	}
}

// TestPullerWorker_EnvelopeCarriesNodeIdentity (§101.7) — одноимённые pull-узлы
// двух команд различаются только id и слогом: без них Sender не знает, чей это
// конверт.
func TestPullerWorker_EnvelopeCarriesNodeIdentity(t *testing.T) {
	cons := &fakeConsumer{batches: [][]RMQDelivery{{
		{DeliveryTag: 1, Body: []byte(`{"x":1}`), MessageID: "m1"},
	}}}
	prod := &fakeProducer{}
	node := testNode()
	node.ID = "3c2b1a09-8f7e-4d6c-9b5a-1f2e3d4c5b6a"
	node.TeamSlug = "billing"
	m := metrics.New("receiver")
	w := NewPullerWorker(node, nil, prod, "nexus.async", 0, m, nil, logging.NewNoop())
	w.consumer = cons

	w.pullOnce(context.Background())

	if len(prod.published) != 1 {
		t.Fatalf("want 1 published, got %d", len(prod.published))
	}
	var env Envelope
	if err := json.Unmarshal(prod.published[0], &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.NodeID != node.ID || env.Team != "billing" {
		t.Fatalf("envelope identity = (%q, %q), want (%q, billing)", env.NodeID, env.Team, node.ID)
	}
	if h := prod.headers[0]; h["node_id"] != node.ID || h["team"] != "billing" {
		t.Fatalf("kafka headers identity = (%q, %q), want (%q, billing)", h["node_id"], h["team"], node.ID)
	}
	// §101.3: ряды pull-узла несут слог — одноимённые узлы команд не складываются.
	if v := testutil.ToFloat64(w.metrics.RMQMessagesPulledTotal.WithLabelValues("billing", node.Path, "ok")); v != 1 {
		t.Fatalf("rmq pulled{team=billing} = %v, want 1", v)
	}
}
