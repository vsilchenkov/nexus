package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
)

func TestNextHop(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		incoming string // значение заголовка X-Nexus-Hops (пусто = отсутствует)
		maxHops  int
		wantOut  int
		wantLoop bool
	}{
		{name: "absent header starts at zero", incoming: "", maxHops: 5, wantOut: 1, wantLoop: false},
		{name: "below limit increments", incoming: "4", maxHops: 5, wantOut: 5, wantLoop: false},
		{name: "at limit is a loop", incoming: "5", maxHops: 5, wantOut: 0, wantLoop: true},
		{name: "above limit is a loop", incoming: "6", maxHops: 5, wantOut: 0, wantLoop: true},
		{name: "garbage treated as zero", incoming: "not-a-number", maxHops: 5, wantOut: 1, wantLoop: false},
		{name: "negative treated as zero", incoming: "-3", maxHops: 5, wantOut: 1, wantLoop: false},
		{name: "disabled never loops", incoming: "999", maxHops: 0, wantOut: 0, wantLoop: false},
		{name: "negative max disables", incoming: "999", maxHops: -1, wantOut: 0, wantLoop: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := http.Header{}
			if tt.incoming != "" {
				h.Set(HeaderHops, tt.incoming)
			}
			out, loop := nextHop(h, tt.maxHops)
			if out != tt.wantOut || loop != tt.wantLoop {
				t.Fatalf("nextHop(%q, %d) = (%d, %v), want (%d, %v)",
					tt.incoming, tt.maxHops, out, loop, tt.wantOut, tt.wantLoop)
			}
		})
	}
}

func loopTestNode() *domain.Node {
	return &domain.Node{
		Path:             "demo/path",
		RootMethod:       domain.RootMethodRequest,
		IncomingMethod:   domain.HTTPMethodPOST,
		OutgoingMethod:   domain.HTTPMethodPOST,
		URLMode:          domain.URLModeStatic,
		TargetURL:        "https://example.com/hook",
		AuthType:         domain.AuthTypeNone,
		IncomingAuthType: domain.IncomingAuthTypeNone,
		Status:           domain.NodeStatusEnabled,
	}
}

func hopHeader(v string) http.Header {
	h := http.Header{}
	h.Set(HeaderHops, v)
	return h
}

// TestRoute_LoopDetected: sync-запрос, пришедший с X-Nexus-Hops == max_hops,
// отклоняется ErrLoopDetected и наружу (в Sender) не уходит.
func TestRoute_LoopDetected(t *testing.T) {
	t.Parallel()
	sender := &capturingSender{}
	u := NewRouteUsecase(stubNodeReader{node: loopTestNode()}, sender, 5, logging.NewNoop())
	_, err := u.Route(context.Background(), RouteInput{
		NodePath: "demo/path", Method: "POST", Header: hopHeader("5"),
	})
	if !errors.Is(err, domain.ErrLoopDetected) {
		t.Fatalf("want ErrLoopDetected, got %v", err)
	}
	if sender.last != nil {
		t.Fatalf("request must not be forwarded to Sender on loop")
	}
}

// TestRoute_HopIncrement: исходящий запрос несёт X-Nexus-Hops = incoming+1.
func TestRoute_HopIncrement(t *testing.T) {
	t.Parallel()
	sender := &capturingSender{}
	u := NewRouteUsecase(stubNodeReader{node: loopTestNode()}, sender, 5, logging.NewNoop())
	_, err := u.Route(context.Background(), RouteInput{
		NodePath: "demo/path", Method: "POST", Header: hopHeader("2"),
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got := sender.last.GetHeaders()[HeaderHops]; got != "3" {
		t.Fatalf("want outgoing X-Nexus-Hops=3, got %q", got)
	}
}

// TestRoute_HopDisabled: при maxHops<=0 защита выключена — петля не детектится
// и служебный заголовок наружу не добавляется.
func TestRoute_HopDisabled(t *testing.T) {
	t.Parallel()
	sender := &capturingSender{}
	u := NewRouteUsecase(stubNodeReader{node: loopTestNode()}, sender, 0, logging.NewNoop())
	_, err := u.Route(context.Background(), RouteInput{
		NodePath: "demo/path", Method: "POST", Header: hopHeader("999"),
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if _, ok := sender.last.GetHeaders()[HeaderHops]; ok {
		t.Fatalf("hop header must not be set when protection is disabled")
	}
}

// capturingProducer запоминает payload последнего Produce для разбора Envelope.
type capturingProducer struct {
	last []byte
}

func (p *capturingProducer) Produce(_ context.Context, _, _ string, value []byte, _ map[string]string) error {
	p.last = value
	return nil
}

// TestRouteAsync_LoopDetected: async-запрос с X-Nexus-Hops == max_hops
// отклоняется и в Kafka ничего не кладётся.
func TestRouteAsync_LoopDetected(t *testing.T) {
	t.Parallel()
	prod := &capturingProducer{}
	u := NewRouteAsyncUsecase(stubNodeReader{node: loopTestNode()}, prod, "nexus.async", 5, logging.NewNoop())
	_, err := u.RouteAsync(context.Background(), RouteInput{
		NodePath: "demo/path", Method: "POST", Header: hopHeader("5"),
	})
	if !errors.Is(err, domain.ErrLoopDetected) {
		t.Fatalf("want ErrLoopDetected, got %v", err)
	}
	if prod.last != nil {
		t.Fatalf("nothing must be produced to Kafka on loop")
	}
}

// TestRouteAsync_HopIncrement: Envelope в Kafka несёт X-Nexus-Hops = incoming+1.
func TestRouteAsync_HopIncrement(t *testing.T) {
	t.Parallel()
	prod := &capturingProducer{}
	u := NewRouteAsyncUsecase(stubNodeReader{node: loopTestNode()}, prod, "nexus.async", 5, logging.NewNoop())
	_, err := u.RouteAsync(context.Background(), RouteInput{
		NodePath: "demo/path", Method: "POST", Header: hopHeader("1"),
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	var env Envelope
	if err := json.Unmarshal(prod.last, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if got := env.Headers[HeaderHops]; got != "2" {
		t.Fatalf("want envelope X-Nexus-Hops=2, got %q", got)
	}
}

var _ AsyncProducer = (*capturingProducer)(nil)
