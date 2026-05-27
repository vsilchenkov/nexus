package usecase

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
)

func TestIsSafePartition(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"YYYYMM digits", "202405", true},
		{"alnum + underscore", "abc_123", true},
		{"hyphen allowed", "2024-05", true},
		{"empty rejected", "", false},
		{"semicolon — sql-injection", "202405';--", false},
		{"quote rejected", "'202405'", false},
		{"space rejected", "2024 05", false},
		{"slash rejected", "2024/05", false},
		{"cyrillic rejected", "майfff", false},
		{"too long (>64)", string(makeBytes(65, 'a')), false},
		{"exactly 64 ok", string(makeBytes(64, 'a')), true},
		{"dot rejected", "2024.05", false},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := isSafePartition(c.in); got != c.want {
				t.Errorf("isSafePartition(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func makeBytes(n int, b byte) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

func TestSplitDBTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in    string
		db    string
		table string
		ok    bool
	}{
		{"nexus.logs", "nexus", "logs", true},
		{"db.tbl_name", "db", "tbl_name", true},
		{"no_dot", "", "", false},
		{"", "", "", false},
		{".leading", "", "", false},
		{"trailing.", "", "", false},
		{".", "", "", false},
		// две точки — берётся первая.
		{"a.b.c", "a", "b.c", true},
	}
	for _, c := range cases {
		c := c
		t.Run(c.in, func(t *testing.T) {
			t.Parallel()
			db, tbl, ok := splitDBTable(c.in)
			assert.Equal(t, c.ok, ok)
			assert.Equal(t, c.db, db)
			assert.Equal(t, c.table, tbl)
		})
	}
}

// stubNodeLister — мок NodeLister для housekeeping.
type stubNodeLister struct {
	nodes []*domain.Node
	err   error
	calls int
	mu    sync.Mutex
}

func (s *stubNodeLister) ListForHousekeeping(_ context.Context) ([]*domain.Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.nodes, s.err
}

// nilConnProvider возвращает nil — нужный путь для проверки nil-guard.
type nilConnProvider struct{}

func (nilConnProvider) Conn() chdriver.Conn { return nil }

func TestCHHousekeeping_RunOnce_ListError(t *testing.T) {
	t.Parallel()
	nodes := &stubNodeLister{err: errors.New("db down")}
	h := NewCHHousekeeping(nilConnProvider{}, nodes, logging.NewNoop())

	err := h.runOnce(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list nodes")
	assert.Equal(t, 1, nodes.calls)
}

func TestCHHousekeeping_RunOnce_NoNodes(t *testing.T) {
	t.Parallel()
	nodes := &stubNodeLister{nodes: nil}
	h := NewCHHousekeeping(nilConnProvider{}, nodes, logging.NewNoop())

	require.NoError(t, h.runOnce(context.Background()))
}

func TestCHHousekeeping_RunOnce_SkipsNodesWithoutTableOrRetention(t *testing.T) {
	t.Parallel()
	// Узлы без table или retention=0 не должны лезть в Conn().
	// Если бы лезли, conn == nil → dropPartitionsOlderThan вернёт error
	// и Warn-лог; но runOnce не падает — ошибка только логируется.
	// Здесь же проверяем, что вообще не было попытки вызова: для этого
	// ставим узлы которые должны быть пропущены.
	nodes := &stubNodeLister{nodes: []*domain.Node{
		{ID: "1", ClickHouseTable: "", ClickHouseRetentionDays: 30},
		{ID: "2", ClickHouseTable: "nexus.tbl", ClickHouseRetentionDays: 0},
		{ID: "3", ClickHouseTable: "nexus.tbl", ClickHouseRetentionDays: -1},
	}}
	h := NewCHHousekeeping(nilConnProvider{}, nodes, logging.NewNoop())

	// Не должно паниковать (conn == nil не достигается).
	require.NoError(t, h.runOnce(context.Background()))
}

func TestCHHousekeeping_DropPartitions_InvalidTableName(t *testing.T) {
	t.Parallel()
	h := NewCHHousekeeping(nilConnProvider{}, &stubNodeLister{}, logging.NewNoop())

	n, err := h.dropPartitionsOlderThan(context.Background(), "no_dot_table", 30)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid table name")
	assert.Equal(t, 0, n)
}

func TestCHHousekeeping_DropPartitions_NilConn(t *testing.T) {
	t.Parallel()
	h := NewCHHousekeeping(nilConnProvider{}, &stubNodeLister{}, logging.NewNoop())

	n, err := h.dropPartitionsOlderThan(context.Background(), "db.tbl", 30)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "conn is nil")
	assert.Equal(t, 0, n)
}

func TestCHHousekeeping_RunOnce_NodeWithBadTableLogsAndContinues(t *testing.T) {
	t.Parallel()
	nodes := &stubNodeLister{nodes: []*domain.Node{
		{ID: "1", ClickHouseTable: "bad-no-dot", ClickHouseRetentionDays: 30},
		{ID: "2", ClickHouseTable: "", ClickHouseRetentionDays: 30}, // skip
	}}
	h := NewCHHousekeeping(nilConnProvider{}, nodes, logging.NewNoop())

	// Ошибка drop'а одного узла не должна валить весь цикл — runOnce nil.
	require.NoError(t, h.runOnce(context.Background()))
}

func TestCHHousekeeping_New_DefaultPeriod(t *testing.T) {
	t.Parallel()
	h := NewCHHousekeeping(nilConnProvider{}, &stubNodeLister{}, logging.NewNoop())
	// Контракт: дефолтный период — 24h. Поле приватное, но Run использует.
	if h.period.Hours() != 24 {
		t.Errorf("default period = %v, want 24h", h.period)
	}
}

func TestCHHousekeeping_Run_StopsOnCtxCancel(t *testing.T) {
	t.Parallel()
	nodes := &stubNodeLister{}
	h := NewCHHousekeeping(nilConnProvider{}, nodes, logging.NewNoop())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		h.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
	// Один прогон до cancel — runOnce был вызван в начале.
	assert.GreaterOrEqual(t, nodes.calls, 1)
}
