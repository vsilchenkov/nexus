package nodecache

import (
	"context"
	"testing"
	"time"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
)

// innerStub — port.NodeReader + Invalidator: считает Get и фиксирует
// делегированные Invalidate.
type innerStub struct {
	getCalls    int
	invalidated []string
	node        *domain.Node
}

func (s *innerStub) Get(_ context.Context, _, _ string) (*domain.Node, error) {
	s.getCalls++
	return s.node, nil
}

func (s *innerStub) Invalidate(_ context.Context, _, path string) error {
	s.invalidated = append(s.invalidated, path)
	return nil
}

// L2Reader.Invalidate выселяет из L1 и делегирует внутрь: после инвалидации
// следующий Get снова идёт в downstream (L1 промахнулся).
func TestL2Reader_Invalidate_EvictsAndDelegates(t *testing.T) {
	t.Parallel()
	inner := &innerStub{node: &domain.Node{Path: "p"}}
	reader := NewL2(inner, L2Config{Enabled: true, Size: 10, TTL: time.Minute}, logging.NewNoop(), nil)
	ctx := context.Background()

	// Первый Get кладёт в L1, второй — fresh-hit (downstream не трогается).
	if _, err := reader.Get(ctx, "team", "p"); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Get(ctx, "team", "p"); err != nil {
		t.Fatal(err)
	}
	if inner.getCalls != 1 {
		t.Fatalf("L1 fresh-hit ожидался, downstream Get вызван %d раз", inner.getCalls)
	}

	inv, ok := reader.(Invalidator)
	if !ok {
		t.Fatal("L2Reader должен реализовывать Invalidator")
	}
	if err := inv.Invalidate(ctx, "team", "p"); err != nil {
		t.Fatal(err)
	}
	if len(inner.invalidated) != 1 || inner.invalidated[0] != "p" {
		t.Fatalf("инвалидация должна делегироваться внутрь: %+v", inner.invalidated)
	}

	// После выселения L1 следующий Get снова идёт в downstream.
	if _, err := reader.Get(ctx, "team", "p"); err != nil {
		t.Fatal(err)
	}
	if inner.getCalls != 2 {
		t.Fatalf("после Invalidate ожидался повторный downstream Get, got %d", inner.getCalls)
	}
}

// Reader.Invalidate без Redis — no-op без ошибки (best-effort).
func TestReader_Invalidate_NilRedis(t *testing.T) {
	t.Parallel()
	r := &Reader{}
	if err := r.Invalidate(context.Background(), "", "p"); err != nil {
		t.Fatalf("nil redis должен быть no-op, got %v", err)
	}
}
