package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// logReaderMock — отдаёт фиксированные записи на каждый Subscribe-tick.
type logReaderMock struct {
	calls       int
	rows        []*domain.LogRecord
	err         error
	getRow      *domain.LogRecord
	getErr      error
	failedCount uint64
	countErr    error
}

func (m *logReaderMock) GetByID(_ context.Context, _, _ string) (*domain.LogRecord, error) {
	if m.getRow == nil && m.getErr == nil {
		return nil, domain.ErrNotFound
	}
	return m.getRow, m.getErr
}

func (m *logReaderMock) ListSince(_ context.Context, _, _ string, _ int64, _ int) ([]*domain.LogRecord, error) {
	m.calls++
	return m.rows, m.err
}

func (m *logReaderMock) Search(_ context.Context, _ port.LogQuery) ([]*domain.LogRecord, error) {
	return m.rows, m.err
}

func (m *logReaderMock) CountErrors(_ context.Context, _, _ string, _, _ int64) (uint64, error) {
	return 0, nil
}

func (m *logReaderMock) CountFailed(_ context.Context, _, _ string, _, _ int64) (uint64, error) {
	return m.failedCount, m.countErr
}
func (m *logReaderMock) FailedIDs(_ context.Context, _, _ string, _, _ int64, _ int) ([]string, bool, error) {
	return nil, false, nil
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

	rows, err := uc.ListSince(context.Background(), "n1", "", 0, 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
}

func TestLogs_CountFailed_ForwardsToReader(t *testing.T) {
	t.Parallel()
	r := &logReaderMock{failedCount: 42}
	nodes := &stubNodeRepo{nodes: map[string]*domain.Node{
		"n1": {ID: "n1", ClickHouseTable: "t.t", TeamID: "team1", Status: domain.NodeStatusEnabled},
	}}
	uc := NewLogsUsecase(r, nodes, logging.NewNoop())

	n, err := uc.CountFailed(context.Background(), "n1", "team1", 0, 0)
	if err != nil {
		t.Fatalf("count failed: %v", err)
	}
	if n != 42 {
		t.Fatalf("want 42, got %d", n)
	}
}

func TestLogs_CountFailed_TeamScopeAndNoTable(t *testing.T) {
	t.Parallel()
	nodes := &stubNodeRepo{nodes: map[string]*domain.Node{
		"n1": {ID: "n1", ClickHouseTable: "", TeamID: "team1", Status: domain.NodeStatusEnabled},
	}}
	uc := NewLogsUsecase(&logReaderMock{failedCount: 5}, nodes, logging.NewNoop())

	// Чужая команда → 404.
	if _, err := uc.CountFailed(context.Background(), "n1", "other", 0, 0); !errors.Is(err, domain.ErrNodeNotFound) {
		t.Fatalf("want ErrNodeNotFound, got %v", err)
	}
	// Нет таблицы → ErrNodeLogsNotConfigured.
	if _, err := uc.CountFailed(context.Background(), "n1", "team1", 0, 0); !errors.Is(err, domain.ErrNodeLogsNotConfigured) {
		t.Fatalf("want ErrNodeLogsNotConfigured, got %v", err)
	}
}

func TestLogs_GetByID_ReturnsFullRecord(t *testing.T) {
	t.Parallel()
	want := &domain.LogRecord{ID: "log-1", Request: `{"big":"body"}`, Response: `{"ok":true}`}
	r := &logReaderMock{getRow: want}
	nodes := &stubNodeRepo{nodes: map[string]*domain.Node{
		"n1": {ID: "n1", ClickHouseTable: "t.t", Status: domain.NodeStatusEnabled},
	}}
	uc := NewLogsUsecase(r, nodes, logging.NewNoop())

	got, err := uc.GetByID(context.Background(), "n1", "", "log-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Request != want.Request || got.Response != want.Response {
		t.Fatalf("bodies not returned: %+v", got)
	}
}

func TestLogs_GetByID_NoCHTable(t *testing.T) {
	t.Parallel()
	nodes := &stubNodeRepo{nodes: map[string]*domain.Node{
		"n1": {ID: "n1", ClickHouseTable: "", Status: domain.NodeStatusEnabled},
	}}
	uc := NewLogsUsecase(&logReaderMock{}, nodes, logging.NewNoop())
	_, err := uc.GetByID(context.Background(), "n1", "", "log-1")
	if !errors.Is(err, domain.ErrNodeLogsNotConfigured) {
		t.Fatalf("want ErrNodeLogsNotConfigured, got %v", err)
	}
}

func TestLogs_GetByID_NodeNotFound(t *testing.T) {
	t.Parallel()
	uc := NewLogsUsecase(&logReaderMock{}, &stubNodeRepo{nodes: map[string]*domain.Node{}}, logging.NewNoop())
	_, err := uc.GetByID(context.Background(), "nope", "", "log-1")
	if !errors.Is(err, domain.ErrNodeNotFound) {
		t.Fatalf("want ErrNodeNotFound, got %v", err)
	}
}

func TestLogs_GetByID_RecordNotFound(t *testing.T) {
	t.Parallel()
	nodes := &stubNodeRepo{nodes: map[string]*domain.Node{
		"n1": {ID: "n1", ClickHouseTable: "t.t", Status: domain.NodeStatusEnabled},
	}}
	uc := NewLogsUsecase(&logReaderMock{getErr: domain.ErrNotFound}, nodes, logging.NewNoop())
	_, err := uc.GetByID(context.Background(), "n1", "", "missing")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestLogs_ListSince_NoCHTable(t *testing.T) {
	t.Parallel()
	nodes := &stubNodeRepo{nodes: map[string]*domain.Node{
		"n1": {ID: "n1", ClickHouseTable: "", Status: domain.NodeStatusEnabled},
	}}
	uc := NewLogsUsecase(&logReaderMock{}, nodes, logging.NewNoop())

	_, err := uc.ListSince(context.Background(), "n1", "", 0, 100)
	if !errors.Is(err, domain.ErrNodeLogsNotConfigured) {
		t.Fatalf("want ErrNodeLogsNotConfigured, got %v", err)
	}
}

func TestLogs_Subscribe_NoCHTable(t *testing.T) {
	t.Parallel()
	nodes := &stubNodeRepo{nodes: map[string]*domain.Node{
		"n1": {ID: "n1", ClickHouseTable: "", Status: domain.NodeStatusEnabled},
	}}
	uc := NewLogsUsecase(&logReaderMock{}, nodes, logging.NewNoop())

	_, _, err := uc.Subscribe(context.Background(), "n1", "", port.LogQuery{})
	if !errors.Is(err, domain.ErrNodeLogsNotConfigured) {
		t.Fatalf("want ErrNodeLogsNotConfigured, got %v", err)
	}
}

func TestLogs_ListSince_NodeNotFound(t *testing.T) {
	t.Parallel()
	uc := NewLogsUsecase(&logReaderMock{}, &stubNodeRepo{nodes: map[string]*domain.Node{}}, logging.NewNoop())
	_, err := uc.ListSince(context.Background(), "nope", "", 0, 100)
	if !errors.Is(err, domain.ErrNodeNotFound) {
		t.Fatalf("want ErrNodeNotFound, got %v", err)
	}
}

func TestLogs_Subscribe_NodeMissing(t *testing.T) {
	t.Parallel()
	uc := NewLogsUsecase(&logReaderMock{}, &stubNodeRepo{nodes: map[string]*domain.Node{}}, logging.NewNoop())
	_, _, err := uc.Subscribe(context.Background(), "nope", "", port.LogQuery{})
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
	ch, _, err := uc.Subscribe(ctx, "n1", "", port.LogQuery{})
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
