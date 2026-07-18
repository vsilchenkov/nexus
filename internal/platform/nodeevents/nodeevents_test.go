package nodeevents

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// Publish должен быть nil-safe: nil-Publisher и Publisher с nil-rdb не падают и
// не ошибаются (инвалидация best-effort).
func TestPublisher_NilSafe(t *testing.T) {
	t.Parallel()
	var p *Publisher
	if err := p.Publish(context.Background(), Event{Path: "x"}); err != nil {
		t.Fatalf("nil publisher: %v", err)
	}
	p2 := NewPublisher(nil)
	if err := p2.PublishNodeChange(context.Background(), "t", "p", ""); err != nil {
		t.Fatalf("nil rdb: %v", err)
	}
}

func TestEvent_JSONRoundTrip(t *testing.T) {
	t.Parallel()
	ev := Event{TeamID: "t1", Path: "a/b", OldPath: "a/old"}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var got Event
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got != ev {
		t.Fatalf("round-trip: got %+v want %+v", got, ev)
	}

	// old_path — omitempty: не должен светиться при переименования нет.
	b2, _ := json.Marshal(Event{TeamID: "t", Path: "p"})
	if strings.Contains(string(b2), "old_path") {
		t.Fatalf("old_path must be omitted when empty: %s", b2)
	}
}
