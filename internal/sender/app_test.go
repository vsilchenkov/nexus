package sender

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// retryUntil (§95) страхует гонку «Sender стартовал раньше миграции 0041»:
// сид маскирования дочитывается в фоне, пока таблица не появится.

func TestRetryUntil_SucceedsAfterTransientFailures(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	ok := retryUntil(context.Background(), time.Millisecond, 10, func(context.Context) error {
		if calls.Add(1) < 3 {
			return errors.New("relation does not exist")
		}
		return nil
	})
	if !ok {
		t.Fatal("retryUntil должен вернуть true после успешной попытки")
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("attempts = %d, want 3 (2 неудачи + успех)", got)
	}
}

func TestRetryUntil_ExhaustsAttempts(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	ok := retryUntil(context.Background(), time.Millisecond, 4, func(context.Context) error {
		calls.Add(1)
		return errors.New("still missing")
	})
	if ok {
		t.Fatal("retryUntil должен вернуть false при исчерпании попыток")
	}
	if got := calls.Load(); got != 4 {
		t.Fatalf("attempts = %d, want 4 (maxAttempts)", got)
	}
}

func TestRetryUntil_StopsOnContextCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // отменён сразу
	var calls atomic.Int32
	ok := retryUntil(ctx, time.Hour, 100, func(context.Context) error {
		calls.Add(1)
		return errors.New("unreached")
	})
	if ok {
		t.Fatal("retryUntil должен вернуть false при отменённом ctx")
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("attempts = %d, want 0 (ctx отменён до первого тика)", got)
	}
}
