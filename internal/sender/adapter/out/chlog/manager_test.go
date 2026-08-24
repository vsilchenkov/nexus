package chlog_test

import (
	"context"
	"testing"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/config"
	"nexus/internal/platform/logging"
	"nexus/internal/sender/adapter/out/chlog"
)

// stubProvider возвращает либо стабильный nil-conn, либо имитированный
// pingable conn. PrepareBatch у нас не вызывается, потому что мы не
// сбрасываем буфер в этих тестах.
type stubProvider struct{ c chdriver.Conn }

func (s *stubProvider) Conn() chdriver.Conn { return s.c }

func defaultCfg() *config.ClickHouseSection {
	return &config.ClickHouseSection{
		BatchSize:        100,
		FlushIntervalSec: 60, // длинный — тик не сработает в тесте
		BufferMaxSize:    16,
		Workers:          1,
	}
}

func TestWriterManager_Reload_Recreates(t *testing.T) {
	t.Parallel()

	cfg := defaultCfg()
	provider := &stubProvider{}
	wm := chlog.NewManagerWithRetrier(provider, cfg, nil, nil, nil, logging.NewNoop())
	t.Cleanup(func() { wm.Stop(context.Background()) })

	// Меняем cfg — Reload должен подхватить новые значения при создании writer'а.
	cfg.BufferMaxSize = 64
	cfg.Workers = 2
	require.NoError(t, wm.Reload(context.Background()))

	// Smoke: Write после Reload не падает; Stop корректно останавливает горутины.
	wm.Write(context.Background(), "test_table", &domain.LogRecord{ID: "x"})
	// flush в Stop не сработает: provider.Conn() == nil → insertBatch вернёт ошибку,
	// retrier == nil → writer просто залогирует — без паники.
}

func TestWriterManager_Stop_IsIdempotent(t *testing.T) {
	t.Parallel()

	cfg := defaultCfg()
	wm := chlog.NewManagerWithRetrier(&stubProvider{}, cfg, nil, nil, nil, logging.NewNoop())

	wm.Stop(context.Background())
	wm.Stop(context.Background())
}

func TestWriterManager_Reload_AfterStop_IsNoop(t *testing.T) {
	t.Parallel()

	cfg := defaultCfg()
	wm := chlog.NewManagerWithRetrier(&stubProvider{}, cfg, nil, nil, nil, logging.NewNoop())
	wm.Stop(context.Background())

	require.NoError(t, wm.Reload(context.Background()))
}

func TestWriterManager_Write_AfterStop_IsNoop(t *testing.T) {
	t.Parallel()

	cfg := defaultCfg()
	wm := chlog.NewManagerWithRetrier(&stubProvider{}, cfg, nil, nil, nil, logging.NewNoop())
	wm.Stop(context.Background())

	// Не должно паниковать (current == nil после Stop).
	wm.Write(context.Background(), "t", &domain.LogRecord{ID: "x"})
}

func TestWriterManager_Reload_FreshWriterUsesNewCfg(t *testing.T) {
	t.Parallel()

	cfg := defaultCfg()
	cfg.Workers = 1
	wm := chlog.NewManagerWithRetrier(&stubProvider{}, cfg, nil, nil, nil, logging.NewNoop())
	t.Cleanup(func() { wm.Stop(context.Background()) })

	// Изменили cfg — это сразу же отражается на manager.cfg (один указатель).
	// После Reload новый Writer должен подняться с workers=3.
	cfg.Workers = 3
	require.NoError(t, wm.Reload(context.Background()))

	// Косвенный признак, что новый writer работает: даём 50мс на старт горутин,
	// затем Stop — он должен дождаться wg(3 worker'ов) без deadlock.
	time.Sleep(50 * time.Millisecond)
	done := make(chan struct{})
	go func() { wm.Stop(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop hung; worker count likely incorrect")
	}
}

// убедимся, что stubProvider удовлетворяет chlog.ConnProvider —
// build-time гарантия структурного совпадения.
var _ chlog.ConnProvider = (*stubProvider)(nil)
