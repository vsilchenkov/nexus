package safego_test

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"

	extlog "github.com/vsilchenkov/logging"

	"nexus/internal/platform/safego"
)

// bufLogger строит logging.Logger поверх буфера, чтобы проверить содержимое лога.
func bufLogger(buf *bytes.Buffer) *extlog.Wrappedlogger {
	return extlog.NewLogger(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
}

func TestRecover_LogsPanicAndDoesNotRepanic(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := bufLogger(&buf)

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer safego.Recover(logger, "test.goroutine")
		panic("boom")
	}()
	<-done // если бы Recover делал re-panic — горутина уронила бы процесс

	out := buf.String()
	if !strings.Contains(out, "panic recovered in goroutine") {
		t.Errorf("log missing message; got: %q", out)
	}
	if !strings.Contains(out, "boom") {
		t.Errorf("log missing panic value; got: %q", out)
	}
	if !strings.Contains(out, "test.goroutine") {
		t.Errorf("log missing op; got: %q", out)
	}
}

func TestRecover_NoPanicNoLog(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := bufLogger(&buf)

	func() {
		defer safego.Recover(logger, "test.clean")
		_ = 1 + 1
	}()

	if buf.Len() != 0 {
		t.Errorf("expected no log without panic, got: %q", buf.String())
	}
}

func TestRecover_WorksAfterWgDone(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := bufLogger(&buf)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer safego.Recover(logger, "pool.worker")
		panic("worker down")
	}()
	wg.Wait() // wg.Done должен отработать несмотря на панику

	if !strings.Contains(buf.String(), "pool.worker") {
		t.Errorf("log missing op; got: %q", buf.String())
	}
}
