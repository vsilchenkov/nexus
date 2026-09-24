package sender

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	chdrv "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	chpf "nexus/internal/platform/clickhouse"
	"nexus/internal/platform/config"
	"nexus/internal/platform/logging"
)

// blockingCountConn — ClickHouse, у которого guard-count backfill'а висит, пока
// не отменят контекст: так выглядит полный проход по колонкам тел большой
// таблицы на старте.
type blockingCountConn struct {
	chdrv.Conn
	mu      sync.Mutex
	queries []string
	started chan struct{}
	once    sync.Once
}

func (c *blockingCountConn) QueryRow(ctx context.Context, q string, _ ...any) chdrv.Row {
	c.mu.Lock()
	c.queries = append(c.queries, q)
	c.mu.Unlock()
	c.once.Do(func() { close(c.started) })
	<-ctx.Done()
	return errRow{err: ctx.Err()}
}

func (c *blockingCountConn) Close() error { return nil }

func (c *blockingCountConn) queried() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.queries)
}

type errRow struct {
	chdrv.Row
	err error
}

func (r errRow) Scan(...any) error { return r.err }

// TestStartBodySizeBackfill_DoesNotBlockStart — §109.2 (Sentry NEXUS-T/S):
// backfill размеров тел больше не держит старт — метод возвращается сразу,
// пока guard-count ещё идёт, а остановка сервиса дожидается фоновой задачи и
// не опрашивает оставшиеся таблицы. Проверяется сам запуск в фоне; что Start
// зовёт именно его, а не синхронный BackfillBodySizes, видно на стенде: строки
// «backfill body sizes» идут ПОСЛЕ «sender startup phases».
func TestStartBodySizeBackfill_DoesNotBlockStart(t *testing.T) {
	t.Parallel()

	conn := &blockingCountConn{started: make(chan struct{})}
	a := &App{
		logger: logging.NewNoop(),
		chMgr:  chpf.NewManager(conn, nil, &config.ClickHouseSection{}, logging.NewNoop()),
	}
	ctx, cancel := context.WithCancel(context.Background())

	returned := make(chan struct{})
	go func() {
		a.startBodySizeBackfill(ctx, []string{"nexus_default.telegram", "nexus_default.orders"})
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("startBodySizeBackfill ждёт guard-count — старт снова заблокирован")
	}

	select {
	case <-conn.started:
	case <-time.After(2 * time.Second):
		t.Fatal("фоновый backfill не начался")
	}

	cancel() // остановка сервиса
	a.awaitBackgroundJobs(context.Background())
	select {
	case <-a.bodySizeBackfillDone:
	default:
		t.Fatal("awaitBackgroundJobs не дождался фонового backfill")
	}
	assert.Equal(t, 1, conn.queried(), "после отмены оставшиеся таблицы не опрашиваются")
}

// TestStartupPhases — строка «sender startup phases»: длительность каждой фазы
// от предыдущей отметки и until_grpc_ms от начала старта.
func TestStartupPhases(t *testing.T) {
	t.Parallel()

	t0 := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	p := newStartupPhases(t0)
	p.mark("clickhouse_producer", t0.Add(150*time.Millisecond))
	p.mark("schema", t0.Add(400*time.Millisecond))

	got := map[string]int64{}
	for _, a := range p.fields(t0.Add(time.Second)) {
		require.Equal(t, slog.KindInt64, a.Value.Kind(), a.Key)
		got[a.Key] = a.Value.Int64()
	}
	assert.Equal(t, map[string]int64{
		"clickhouse_producer_ms": 150,
		"schema_ms":              250,
		"until_grpc_ms":          1000,
	}, got)
}
