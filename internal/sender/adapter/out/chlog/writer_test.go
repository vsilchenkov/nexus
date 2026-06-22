package chlog_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/config"
	"nexus/internal/platform/logging"
	"nexus/internal/sender/adapter/out/chlog"
)

// stubRetrier — собирает все батчи, которые Writer пытался переотправить
// (insertBatch упал, т.к. provider.Conn()==nil). Заменяет прежний NDJSON-stub.
type stubRetrier struct {
	mu    sync.Mutex
	rows  int
	calls int
}

func (s *stubRetrier) Retry(_ context.Context, _ string, batch []*domain.LogRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.rows += len(batch)
	return nil
}

func (s *stubRetrier) Rows() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rows
}

var _ chlog.BatchRetrier = (*stubRetrier)(nil)

// TestWriter_Stop_DrainsPendingJobs — записи, оставшиеся в канале на момент
// Stop, не теряются: Stop дренирует канал и flush'ит остаток. Conn==nil →
// insertBatch падает → батч уходит в retrier (§38), по нему и проверяем, что
// все записи дошли до финального flush.
func TestWriter_Stop_DrainsPendingJobs(t *testing.T) {
	t.Parallel()

	cfg := &config.ClickHouseSection{
		BatchSize:        100,
		FlushIntervalSec: 60, // тик не сработает в тесте
		BufferMaxSize:    64,
		Workers:          1,
	}
	retrier := &stubRetrier{}
	w := chlog.NewWithRetrier(&stubProvider{}, cfg, retrier, nil, logging.NewNoop())

	const n = 20
	for i := range n {
		w.Write(context.Background(), "test_table", &domain.LogRecord{ID: fmt.Sprintf("rec-%d", i)})
	}
	w.Stop(context.Background())

	require.Equal(t, n, retrier.Rows(),
		"все записи должны попасть в финальный flush (через дренаж канала) и уйти в retry")
}
