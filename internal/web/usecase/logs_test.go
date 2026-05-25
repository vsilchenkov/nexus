package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"bus/internal/domain"
	"bus/internal/platform/logging"
)

// logReaderMock — отдаёт фиксированные записи на каждый Subscribe-tick.
type logReaderMock struct {
	calls int
	rows  []*domain.LogRecord
	err   error
}

func (m *logReaderMock) GetByID(_ context.Context, _, _ string) (*domain.LogRecord, error) {
	return nil, domain.ErrNotFound
}

func (m *logReaderMock) ListSince(_ context.Context, _ string, _ int64, _ int) ([]*domain.LogRecord, error) {
	m.calls++
	return m.rows, m.err
}

func TestLogs_ListSinceForwardsToReader(t *testing.T) {
	t.Parallel()
	r := &logReaderMock{rows: []*domain.LogRecord{
		{ID: "1", DateRequest: time.Now()},
		{ID: "2", DateRequest: time.Now()},
	}}
	nodes := &stubNodeRepo{nodes: map[string]*domain.Node{
		"n1": {ID: "n1", ClickHouseTable: "t.t", Status: domain.NodeStatusEnabled},
	}}
	uc := NewLogsUsecase(r, nodes, logging.NewNoop())

	rows, err := uc.ListSince(context.Background(), "n1", 0, 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
}

func TestLogs_ListSince_NoCHTable(t *testing.T) {
	t.Parallel()
	nodes := &stubNodeRepo{nodes: map[string]*domain.Node{
		"n1": {ID: "n1", ClickHouseTable: "", Status: domain.NodeStatusEnabled},
	}}
	uc := NewLogsUsecase(&logReaderMock{}, nodes, logging.NewNoop())

	_, err := uc.ListSince(context.Background(), "n1", 0, 100)
	if err == nil {
		t.Fatal("expected error when node has no ClickHouse table")
	}
}

func TestLogs_ListSince_NodeNotFound(t *testing.T) {
	t.Parallel()
	uc := NewLogsUsecase(&logReaderMock{}, &stubNodeRepo{nodes: map[string]*domain.Node{}}, logging.NewNoop())
	_, err := uc.ListSince(context.Background(), "nope", 0, 100)
	if !errors.Is(err, domain.ErrNodeNotFound) {
		t.Fatalf("want ErrNodeNotFound, got %v", err)
	}
}

func TestLogs_Subscribe_NodeMissing(t *testing.T) {
	t.Parallel()
	uc := NewLogsUsecase(&logReaderMock{}, &stubNodeRepo{nodes: map[string]*domain.Node{}}, logging.NewNoop())
	_, _, err := uc.Subscribe(context.Background(), "nope")
	if !errors.Is(err, domain.ErrNodeNotFound) {
		t.Fatalf("want ErrNodeNotFound, got %v", err)
	}
}

func TestLogs_Subscribe_ClosesOnCtxCancel(t *testing.T) {
	t.Parallel()
	r := &logReaderMock{}
	nodes := &stubNodeRepo{nodes: map[string]*domain.Node{
		"n1": {ID: "n1", ClickHouseTable: "t.t", Status: domain.NodeStatusEnabled},
	}}
	uc := NewLogsUsecase(r, nodes, logging.NewNoop())
	uc.pollInterval = 5 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	ch, _, err := uc.Subscribe(ctx, "n1")
	if err != nil {
		t.Fatal(err)
	}
	cancel()

	// Канал должен закрыться в разумное время после cancel.
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected closed channel")
		}
	case <-time.After(time.Second):
		t.Fatal("channel did not close after ctx cancel")
	}
}
