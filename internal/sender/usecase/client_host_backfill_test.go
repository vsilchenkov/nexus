package usecase

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	chgo "github.com/ClickHouse/clickhouse-go/v2"
	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/robfig/cron/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/logging"
)

type stubTableLister struct {
	tables []string
	err    error
	calls  atomic.Int32
}

func (s *stubTableLister) ListClickHouseTables(context.Context) ([]string, error) {
	s.calls.Add(1)
	return s.tables, s.err
}

// stubManagedFilter оставляет только keep и запоминает, что ему передали.
type stubManagedFilter struct {
	keep map[string]bool
	got  []string
}

func (f *stubManagedFilter) FilterManagedTables(_ context.Context, tables []string) []string {
	f.got = append([]string(nil), tables...)
	var out []string
	for _, t := range tables {
		if f.keep[t] {
			out = append(out, t)
		}
	}
	return out
}

type stubBlockingResolver struct {
	names map[string]string
	calls atomic.Int32
}

func (s *stubBlockingResolver) ResolveNow(_ context.Context, ip string) string {
	s.calls.Add(1)
	return s.names[ip]
}

// fakeRows — строки SELECT DISTINCT IP; остальные методы driver.Rows не нужны.
type fakeRows struct {
	chdriver.Rows
	ips []string
	i   int
}

func (r *fakeRows) Next() bool { r.i++; return r.i <= len(r.ips) }
func (r *fakeRows) Scan(dest ...any) error {
	*(dest[0].(*string)) = r.ips[r.i-1]
	return nil
}
func (r *fakeRows) Err() error   { return nil }
func (r *fakeRows) Close() error { return nil }

// fakeCHConn отвечает на SELECT по таблице и записывает поставленные мутации.
type fakeCHConn struct {
	chdriver.Conn
	ips      map[string][]string // таблица → IP с пустым client_host
	queryErr map[string]error
	mu       sync.Mutex
	execs    []string
}

// fakeConnProvider отдаёт fakeCHConn как ConnProvider (у самого fakeCHConn
// имя Conn занято встроенным интерфейсом драйвера).
type fakeConnProvider struct{ c chdriver.Conn }

func (p fakeConnProvider) Conn() chdriver.Conn { return p.c }

func (c *fakeCHConn) Query(_ context.Context, q string, _ ...any) (chdriver.Rows, error) {
	for table, err := range c.queryErr {
		if strings.Contains(q, "FROM "+table+" ") {
			return nil, err
		}
	}
	for table, ips := range c.ips {
		if strings.Contains(q, "FROM "+table+" ") {
			return &fakeRows{ips: ips}, nil
		}
	}
	return &fakeRows{}, nil
}

func (c *fakeCHConn) Exec(_ context.Context, q string, _ ...any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.execs = append(c.execs, q)
	return nil
}

func TestClientHostBackfill_RunOnce_ListError(t *testing.T) {
	t.Parallel()
	b := NewClientHostBackfill(nilConnProvider{}, &stubTableLister{err: errors.New("db down")},
		&stubBlockingResolver{}, logging.NewNoop())
	require.ErrorContains(t, b.RunOnce(context.Background()), "db down")
}

func TestClientHostBackfill_RunOnce_NilConn(t *testing.T) {
	t.Parallel()
	b := NewClientHostBackfill(nilConnProvider{}, &stubTableLister{tables: []string{"logs.a"}},
		&stubBlockingResolver{}, logging.NewNoop())
	require.ErrorContains(t, b.RunOnce(context.Background()), "conn is nil")
}

// Кривое имя не доходит ни до гейта владения, ни до SQL; чужая таблица
// (гейт §70.4 её не пропустил) не мутируется.
func TestClientHostBackfill_RunOnce_TableFiltering(t *testing.T) {
	t.Parallel()
	conn := &fakeCHConn{ips: map[string][]string{
		"logs.own":     {"192.168.6.167"},
		"logs.foreign": {"192.168.6.167"},
	}}
	filter := &stubManagedFilter{keep: map[string]bool{"logs.own": true}}
	b := NewClientHostBackfill(fakeConnProvider{conn}, &stubTableLister{tables: []string{"logs.own", "logs.foreign", "bad name; DROP"}},
		&stubBlockingResolver{names: map[string]string{"192.168.6.167": "SRV-D-SQLDEV-HQ"}}, logging.NewNoop()).
		WithManagedFilter(filter)

	require.NoError(t, b.RunOnce(context.Background()))
	assert.Equal(t, []string{"logs.own", "logs.foreign"}, filter.got)
	require.Len(t, conn.execs, 1)
	assert.Contains(t, conn.execs[0], "ALTER TABLE logs.own UPDATE")
}

func TestClientHostBackfill_BackfillTable(t *testing.T) {
	t.Parallel()
	missingColumn := fmt.Errorf("wrap: %w", &chgo.Exception{Code: 47, Message: "Missing columns: 'client_host'"})
	tests := []struct {
		name      string
		conn      *fakeCHConn
		names     map[string]string
		wantN     int
		wantErr   bool
		wantExecs int
	}{
		{
			name:  "resolved_subset",
			conn:  &fakeCHConn{ips: map[string][]string{"logs.t": {"192.168.2.5", "192.168.6.167", "192.168.3.15"}}},
			names: map[string]string{"192.168.6.167": "SRV-D-SQLDEV-HQ.vz78.vozovoz.ru"},
			wantN: 1, wantExecs: 1,
		},
		{name: "nothing_to_fill", conn: &fakeCHConn{}},
		{
			name: "no_names_resolved",
			conn: &fakeCHConn{ips: map[string][]string{"logs.t": {"192.168.2.5"}}},
		},
		// Деплой-окно: колонки §67 ещё нет — не ошибка.
		{name: "missing_column", conn: &fakeCHConn{queryErr: map[string]error{"logs.t": missingColumn}}},
		{name: "other_error", conn: &fakeCHConn{queryErr: map[string]error{"logs.t": errors.New("timeout")}}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			b := NewClientHostBackfill(fakeConnProvider{tt.conn}, &stubTableLister{}, &stubBlockingResolver{names: tt.names}, logging.NewNoop())
			n, err := b.backfillTable(context.Background(), tt.conn, "logs.t")
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantN, n)
			assert.Len(t, tt.conn.execs, tt.wantExecs)
		})
	}
}

func TestClientHostBackfill_ResolveAll_KeepsOnlyResolved(t *testing.T) {
	t.Parallel()
	ips := make([]string, 50)
	names := map[string]string{}
	for i := range ips {
		ips[i] = fmt.Sprintf("10.0.0.%d", i+1)
		if i%2 == 0 {
			names[ips[i]] = fmt.Sprintf("host-%d", i)
		}
	}
	res := &stubBlockingResolver{names: names}
	b := NewClientHostBackfill(nilConnProvider{}, &stubTableLister{}, res, logging.NewNoop())

	got := b.resolveAll(context.Background(), ips)
	assert.Equal(t, names, got)
	assert.EqualValues(t, len(ips), res.calls.Load())
}

func TestBuildClientHostMutation(t *testing.T) {
	t.Parallel()
	stmt, pairs, ok := buildClientHostMutation("nexus_default.logs_x", map[string]string{
		"192.168.6.167": "SRV-D-SQLDEV-HQ.vz78.vozovoz.ru",
		"192.168.2.5":   "SRV-D-1C",
	})
	require.True(t, ok)
	assert.Equal(t, 2, pairs)
	assert.Equal(t, "ALTER TABLE nexus_default.logs_x UPDATE client_host = transform(IP, "+
		"['192.168.2.5', '192.168.6.167'], ['SRV-D-1C', 'SRV-D-SQLDEV-HQ.vz78.vozovoz.ru'], client_host) "+
		"WHERE client_host = '' AND IP IN ('192.168.2.5', '192.168.6.167')", stmt)

	rejected := []struct {
		name  string
		table string
		hosts map[string]string
	}{
		{name: "empty", table: "logs.t", hosts: nil},
		{name: "bad_ip", table: "logs.t", hosts: map[string]string{"192.168.2.5'); DROP": "x"}},
		{name: "quote_in_host", table: "logs.t", hosts: map[string]string{"10.0.0.1": "a'b"}},
		{name: "space_in_host", table: "logs.t", hosts: map[string]string{"10.0.0.1": "a b"}},
		{name: "backslash_in_host", table: "logs.t", hosts: map[string]string{"10.0.0.1": `a\b`}},
		{name: "cyrillic_host", table: "logs.t", hosts: map[string]string{"10.0.0.1": "сервер"}},
		{name: "host_too_long", table: "logs.t", hosts: map[string]string{"10.0.0.1": strings.Repeat("a", 254)}},
		{name: "bad_table", table: "logs.t; DROP", hosts: map[string]string{"10.0.0.1": "ok"}},
	}
	for _, r := range rejected {
		t.Run(r.name, func(t *testing.T) {
			t.Parallel()
			_, _, ok := buildClientHostMutation(r.table, r.hosts)
			assert.False(t, ok)
		})
	}

	// Негодная пара отбрасывается, годная остаётся.
	stmt, pairs, ok = buildClientHostMutation("logs.t", map[string]string{"10.0.0.1": "ok-host", "10.0.0.2": "bad'host"})
	require.True(t, ok)
	assert.Equal(t, 1, pairs)
	assert.NotContains(t, stmt, "bad")
}

func TestClientHostBackfill_RunCycle_Lock(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		lock      *fakeCycleLock
		wantLists int32
	}{
		{name: "granted", lock: &fakeCycleLock{granted: true}, wantLists: 1},
		{name: "taken_by_other_replica", lock: &fakeCycleLock{}, wantLists: 0},
		{name: "lock_error", lock: &fakeCycleLock{err: errors.New("redis down")}, wantLists: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			lister := &stubTableLister{}
			b := NewClientHostBackfill(fakeConnProvider{&fakeCHConn{}}, lister, &stubBlockingResolver{}, logging.NewNoop()).
				WithCycleLock(tt.lock)
			b.runCycle(context.Background())
			assert.Equal(t, tt.wantLists, lister.calls.Load())
			assert.EqualValues(t, backfillLockTTL, tt.lock.lastTTL.Load())
		})
	}
}

func TestClientHostBackfill_SetSchedule(t *testing.T) {
	t.Parallel()
	b := NewClientHostBackfill(nilConnProvider{}, &stubTableLister{}, &stubBlockingResolver{}, logging.NewNoop())
	t.Cleanup(b.shutdown)
	state := func() (*cron.Cron, string) {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.cron, b.curExpr
	}

	require.NoError(t, b.SetSchedule(""), "выключено без расписания — no-op")
	c, _ := state()
	assert.Nil(t, c)

	require.NoError(t, b.SetSchedule("0 3 * * *"))
	armed, expr := state()
	require.NotNil(t, armed)
	assert.Equal(t, "0 3 * * *", expr)

	require.NoError(t, b.SetSchedule("0 3 * * *"))
	same, _ := state()
	assert.Same(t, armed, same, "то же выражение не пересоздаёт планировщик")

	require.Error(t, b.SetSchedule("* * * * * *"))
	kept, expr := state()
	assert.Same(t, armed, kept, "невалидное выражение оставляет прежнее расписание")
	assert.Equal(t, "0 3 * * *", expr)

	require.NoError(t, b.SetSchedule("*/5 * * * *"))
	changed, expr := state()
	assert.NotSame(t, armed, changed)
	assert.Equal(t, "*/5 * * * *", expr)

	require.NoError(t, b.SetSchedule(""))
	off, expr := state()
	assert.Nil(t, off)
	assert.Empty(t, expr)
}

// Тик расписания запускает цикл в горутине Run; после отмены ctx Run выходит,
// а новое расписание уже не взводится.
func TestClientHostBackfill_Run_TickAndStop(t *testing.T) {
	t.Parallel()
	lister := &stubTableLister{}
	b := NewClientHostBackfill(fakeConnProvider{&fakeCHConn{}}, lister, &stubBlockingResolver{}, logging.NewNoop())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.Run(ctx)
	}()

	b.fire()
	require.Eventually(t, func() bool { return lister.calls.Load() == 1 }, 2*time.Second, 10*time.Millisecond)

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run не завершился после отмены ctx")
	}
	require.NoError(t, b.SetSchedule("0 3 * * *"))
	b.mu.Lock()
	defer b.mu.Unlock()
	assert.Nil(t, b.cron, "после остановки расписание не взводится")
}

// Лишние тики, пока цикл не начат, не копятся: в канале не больше одного.
func TestClientHostBackfill_Fire_DropsExtraTicks(t *testing.T) {
	t.Parallel()
	b := NewClientHostBackfill(nilConnProvider{}, &stubTableLister{}, &stubBlockingResolver{}, logging.NewNoop())
	for range 5 {
		b.fire()
	}
	assert.Len(t, b.trigger, 1)
}
