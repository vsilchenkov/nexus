package usecase

import (
	"context"
	"errors"
	"sync"
	"testing"

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
	err       error
}

func (f *fakeProducer) Produce(_ context.Context, _, _ string, value []byte, _ map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.published = append(f.published, value)
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
