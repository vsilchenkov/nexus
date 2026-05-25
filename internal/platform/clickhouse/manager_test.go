package clickhouse_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	chpf "bus/internal/platform/clickhouse"
	"bus/internal/platform/config"
	"bus/internal/platform/logging"
)

// fakeConn — stub реализации chdriver.Conn для unit-тестов Manager.
// Реализует только Close (остальные методы паникуют — не используются в тестах).
type fakeConn struct {
	id     int
	closed atomic.Bool
}

func (f *fakeConn) Close() error                                  { f.closed.Store(true); return nil }
func (*fakeConn) Contributors() []string                          { panic("not impl") }
func (*fakeConn) ServerVersion() (*chdriver.ServerVersion, error) { panic("not impl") }
func (*fakeConn) Select(context.Context, any, string, ...any) error {
	panic("not impl")
}
func (*fakeConn) Query(context.Context, string, ...any) (chdriver.Rows, error) {
	panic("not impl")
}
func (*fakeConn) QueryRow(context.Context, string, ...any) chdriver.Row { panic("not impl") }
func (*fakeConn) PrepareBatch(context.Context, string, ...chdriver.PrepareBatchOption) (chdriver.Batch, error) {
	panic("not impl")
}
func (*fakeConn) Exec(context.Context, string, ...any) error        { panic("not impl") }
func (*fakeConn) AsyncInsert(context.Context, string, bool, ...any) error { panic("not impl") }
func (*fakeConn) Ping(context.Context) error                         { return nil }
func (*fakeConn) Stats() chdriver.Stats                              { panic("not impl") }

func TestManager_Conn_ReturnsInitial(t *testing.T) {
	t.Parallel()

	initial := &fakeConn{id: 1}
	cfg := &config.ClickHouseSection{Host: "localhost", Port: 9000, Database: "test"}
	m := chpf.NewManager(initial, nil, cfg, logging.NewNoop())

	assert.Same(t, initial, m.Conn())
}

func TestManager_Reload_SwapsConn(t *testing.T) {
	t.Parallel()

	initial := &fakeConn{id: 1}
	fresh := &fakeConn{id: 2}
	calls := 0
	factory := func(_ context.Context, _ *config.ClickHouseSection) (chdriver.Conn, error) {
		calls++
		return fresh, nil
	}

	cfg := &config.ClickHouseSection{Host: "h", Port: 9000, Database: "d"}
	m := chpf.NewManager(initial, factory, cfg, logging.NewNoop(),
		chpf.WithCloseDelay(0)) // синхронное закрытие старого

	require.NoError(t, m.Reload(context.Background()))

	assert.Equal(t, 1, calls)
	assert.Same(t, fresh, m.Conn())
	assert.True(t, initial.closed.Load(), "old conn must be closed")
	assert.False(t, fresh.closed.Load(), "new conn must stay open")
}

func TestManager_Reload_FactoryFailure_KeepsOldConn(t *testing.T) {
	t.Parallel()

	initial := &fakeConn{id: 1}
	wantErr := errors.New("dial refused")
	factory := func(context.Context, *config.ClickHouseSection) (chdriver.Conn, error) {
		return nil, wantErr
	}

	cfg := &config.ClickHouseSection{Host: "h", Port: 9000}
	m := chpf.NewManager(initial, factory, cfg, logging.NewNoop())

	err := m.Reload(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.Same(t, initial, m.Conn(), "old conn must remain after failed reload")
	assert.False(t, initial.closed.Load())
}

func TestManager_Close_IsIdempotent(t *testing.T) {
	t.Parallel()

	initial := &fakeConn{id: 1}
	cfg := &config.ClickHouseSection{}
	m := chpf.NewManager(initial, nil, cfg, logging.NewNoop())

	require.NoError(t, m.Close(context.Background()))
	require.NoError(t, m.Close(context.Background()))
	assert.True(t, initial.closed.Load())
	assert.Nil(t, m.Conn())
}

func TestManager_Reload_AfterClose_Errors(t *testing.T) {
	t.Parallel()

	initial := &fakeConn{id: 1}
	factory := func(context.Context, *config.ClickHouseSection) (chdriver.Conn, error) {
		t.Fatal("factory must not be called after Close")
		return nil, nil
	}
	cfg := &config.ClickHouseSection{}
	m := chpf.NewManager(initial, factory, cfg, logging.NewNoop())
	require.NoError(t, m.Close(context.Background()))

	err := m.Reload(context.Background())
	require.Error(t, err)
}

func TestManager_Reload_WithDelay_ClosesOldEventually(t *testing.T) {
	t.Parallel()

	initial := &fakeConn{id: 1}
	fresh := &fakeConn{id: 2}
	factory := func(context.Context, *config.ClickHouseSection) (chdriver.Conn, error) {
		return fresh, nil
	}
	cfg := &config.ClickHouseSection{}
	m := chpf.NewManager(initial, factory, cfg, logging.NewNoop(),
		chpf.WithCloseDelay(50*time.Millisecond))

	require.NoError(t, m.Reload(context.Background()))
	assert.Same(t, fresh, m.Conn())
	assert.False(t, initial.closed.Load(), "close is delayed")

	assert.Eventually(t, func() bool { return initial.closed.Load() },
		1*time.Second, 10*time.Millisecond,
		"old conn should be closed after delay")
}
