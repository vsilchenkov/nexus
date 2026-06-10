package chlog_test

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/config"
	"nexus/internal/platform/logging"
	"nexus/internal/sender/adapter/out/chlog"
)

// TestWriter_Stop_DrainsPendingJobs — записи, оставшиеся в канале на момент
// Stop, не должны теряться: Stop дренирует канал и flush'ит остаток.
// Conn == nil → insertBatch падает → батч уходит в file-fallback, по которому
// и проверяем, что все записи дошли до финального flush.
func TestWriter_Stop_DrainsPendingJobs(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := &config.ClickHouseSection{
		BatchSize:        100,
		FlushIntervalSec: 60, // тик не сработает в тесте
		BufferMaxSize:    64,
		Workers:          1,
	}
	w := chlog.NewWithFallback(&stubProvider{}, cfg, dir, nil, logging.NewNoop())

	const n = 20
	for i := range n {
		w.Write(context.Background(), "test_table", &domain.LogRecord{ID: fmt.Sprintf("rec-%d", i)})
	}
	w.Stop(context.Background())

	require.Equal(t, n, countFallbackRecords(t, dir),
		"все записи должны попасть в финальный flush (через дренаж канала)")
}

// countFallbackRecords — суммарное число NDJSON-строк во всех fallback-файлах.
func countFallbackRecords(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	total := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ndjson") {
			continue
		}
		f, err := os.Open(filepath.Join(dir, e.Name()))
		require.NoError(t, err)
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1024*1024), 1024*1024)
		for sc.Scan() {
			if strings.TrimSpace(sc.Text()) != "" {
				total++
			}
		}
		require.NoError(t, sc.Err())
		_ = f.Close()
	}
	return total
}
