package chlog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bus/internal/domain"
	"bus/internal/platform/logging"
)

func TestFallbackStore_SaveAndRestore(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := newFallbackStore(dir, time.Second, logging.NewNoop())
	if !s.Enabled() {
		t.Fatal("expected enabled fallback")
	}

	batch := []*domain.LogRecord{
		{ID: "1", URL: "https://a.com", Method: "POST", Status: 500, Done: false},
		{ID: "2", URL: "https://b.com", Method: "POST", Status: 200, Done: true},
	}
	path, err := s.Save("vika_logs.demo", batch)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file %s: %v", path, err)
	}

	// Реплеер успешно «доставляет» — файл должен удалиться.
	var got []*domain.LogRecord
	replay := func(_ context.Context, table string, b []*domain.LogRecord) error {
		if table != "vika_logs.demo" {
			t.Errorf("table mismatch: %q", table)
		}
		got = append(got, b...)
		return nil
	}
	s.tryRestoreOnce(context.Background(), replay)

	if len(got) != 2 {
		t.Fatalf("restored %d rows, want 2", len(got))
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file should be deleted after successful restore, stat=%v", err)
	}
}

func TestFallbackStore_RestoreKeepsOnFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := newFallbackStore(dir, time.Second, logging.NewNoop())

	batch := []*domain.LogRecord{{ID: "x", URL: "https://x.com", Method: "POST"}}
	path, err := s.Save("vika_logs.demo", batch)
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	failReplay := func(_ context.Context, _ string, _ []*domain.LogRecord) error {
		return errors.New("CH down")
	}
	s.tryRestoreOnce(context.Background(), failReplay)

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file must be kept on failure: %v", err)
	}
}

func TestFallbackStore_EnabledFalseWhenDirEmpty(t *testing.T) {
	t.Parallel()
	s := newFallbackStore("", time.Second, logging.NewNoop())
	if s.Enabled() {
		t.Fatal("empty dir = disabled fallback")
	}
	if _, err := s.Save("any", []*domain.LogRecord{{}}); err == nil {
		t.Fatal("Save must return error when disabled")
	}
}

// Smoke-check на формат имени: префикс ch- и расширение .ndjson.
func TestFallbackStore_SaveFileNamingFormat(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := newFallbackStore(dir, time.Second, logging.NewNoop())
	path, err := s.Save("t.t", []*domain.LogRecord{{ID: "1"}})
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Base(path)
	if got := base[:3]; got != "ch-" {
		t.Errorf("filename prefix: %q", base)
	}
	if filepath.Ext(base) != ".ndjson" {
		t.Errorf("filename ext: %q", base)
	}
}
