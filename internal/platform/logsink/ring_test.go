package logsink

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestHandler(t *testing.T, level slog.Level, opts ...RingOption) (*RingHandler, *slog.LevelVar) {
	t.Helper()
	lv := new(slog.LevelVar)
	lv.Set(level)
	return NewRingHandler(lv, "web", opts...), lv
}

func TestRingHandler_EnabledFollowsLevelVar(t *testing.T) {
	t.Parallel()
	h, lv := newTestHandler(t, slog.LevelInfo)
	ctx := context.Background()

	assert.False(t, h.Enabled(ctx, slog.LevelDebug))
	assert.True(t, h.Enabled(ctx, slog.LevelInfo))
	assert.True(t, h.Enabled(ctx, slog.LevelWarn))
	assert.True(t, h.Enabled(ctx, slog.LevelError))

	lv.Set(slog.LevelError)
	assert.False(t, h.Enabled(ctx, slog.LevelInfo))
	assert.False(t, h.Enabled(ctx, slog.LevelWarn))
	assert.True(t, h.Enabled(ctx, slog.LevelError))

	lv.Set(slog.LevelDebug)
	assert.True(t, h.Enabled(ctx, slog.LevelDebug))
}

func TestRingHandler_HandleAppendsToRing(t *testing.T) {
	t.Parallel()
	h, _ := newTestHandler(t, slog.LevelDebug)
	logger := slog.New(h)

	logger.Info("hello", slog.String("node", "n1"), slog.Int("status", 200))

	got := h.Snapshot()
	require.Len(t, got, 1)
	e := got[0]
	assert.Equal(t, "info", e.Level)
	assert.Equal(t, "web", e.Service)
	assert.Equal(t, "hello", e.Msg)
	assert.False(t, e.TS.IsZero())
	assert.Equal(t, "n1", e.Attrs["node"])
	assert.Equal(t, int64(200), e.Attrs["status"])
}

func TestRingHandler_RingOverwritesOldest(t *testing.T) {
	t.Parallel()
	h, _ := newTestHandler(t, slog.LevelDebug, WithRingCapacity(4))
	logger := slog.New(h)

	for _, msg := range []string{"m1", "m2", "m3", "m4", "m5", "m6"} {
		logger.Info(msg)
	}

	got := h.Snapshot()
	require.Len(t, got, 4)
	msgs := make([]string, len(got))
	for i, e := range got {
		msgs[i] = e.Msg
	}
	assert.Equal(t, []string{"m3", "m4", "m5", "m6"}, msgs)
}

func TestRingHandler_MasksSensitiveTopLevel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		key    string
		masked bool
	}{
		{"password", "password", true},
		{"authorization header", "Authorization", true},
		{"contains token", "x-api-token", true},
		{"plain node", "node", false},
		{"plain status", "status", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, _ := newTestHandler(t, slog.LevelDebug)
			slog.New(h).Info("m", slog.String(tc.key, "value-1"))

			got := h.Snapshot()
			require.Len(t, got, 1)
			if tc.masked {
				assert.Equal(t, "***", got[0].Attrs[tc.key])
			} else {
				assert.Equal(t, "value-1", got[0].Attrs[tc.key])
			}
		})
	}
}

func TestRingHandler_MasksSensitiveInGroups(t *testing.T) {
	t.Parallel()
	h, _ := newTestHandler(t, slog.LevelDebug)
	logger := slog.New(h)

	logger.Info("login",
		slog.Group("auth", slog.String("token", "abc"), slog.String("user", "bob")))
	logger.WithGroup("req").With("secret", "s3cr3t").Info("call", slog.String("path", "/x"))

	got := h.Snapshot()
	require.Len(t, got, 2)

	auth, ok := got[0].Attrs["auth"].(map[string]any)
	require.True(t, ok, "auth group must be nested map")
	assert.Equal(t, "***", auth["token"])
	assert.Equal(t, "bob", auth["user"])

	req, ok := got[1].Attrs["req"].(map[string]any)
	require.True(t, ok, "req group must be nested map")
	assert.Equal(t, "***", req["secret"])
	assert.Equal(t, "/x", req["path"])
}

func TestRingHandler_WithAttrsAppliedToEveryRecord(t *testing.T) {
	t.Parallel()
	h, _ := newTestHandler(t, slog.LevelDebug)
	logger := slog.New(h).With(slog.String("svc_ver", "1.0"))

	logger.Info("a")
	logger.Warn("b", slog.String("extra", "x"))

	got := h.Snapshot()
	require.Len(t, got, 2)
	assert.Equal(t, "1.0", got[0].Attrs["svc_ver"])
	assert.Equal(t, "1.0", got[1].Attrs["svc_ver"])
	assert.Equal(t, "x", got[1].Attrs["extra"])
	// Клон с With не должен влиять на исходный хендлер.
	slog.New(h).Info("c")
	got = h.Snapshot()
	require.Len(t, got, 3)
	assert.NotContains(t, got[2].Attrs, "svc_ver")
}

func TestRingHandler_TruncatesHugeAttrValues(t *testing.T) {
	t.Parallel()
	h, _ := newTestHandler(t, slog.LevelDebug)
	huge := strings.Repeat("x", maxAttrValueBytes+100)

	slog.New(h).Info("big", slog.String("body", huge))

	got := h.Snapshot()
	require.Len(t, got, 1)
	v, ok := got[0].Attrs["body"].(string)
	require.True(t, ok)
	assert.Less(t, len(v), len(huge))
	assert.Contains(t, v, "truncated")
}

func TestRingHandler_DropsWhenChannelFull(t *testing.T) {
	t.Parallel()
	const chanCap = 8
	h, _ := newTestHandler(t, slog.LevelDebug, WithChannelCapacity(chanCap))
	logger := slog.New(h)

	const total = chanCap + 5
	for range total {
		logger.Info("m")
	}

	assert.Equal(t, uint64(total-chanCap), h.Dropped())
	assert.Len(t, h.Entries(), chanCap)
}

func TestRingHandler_HandleNonBlockingWithoutShipper(t *testing.T) {
	t.Parallel()
	h, _ := newTestHandler(t, slog.LevelDebug, WithChannelCapacity(4), WithRingCapacity(16))
	logger := slog.New(h)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 10_000 {
			logger.Info("m", slog.String("k", "v"))
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Handle blocked: 10k records did not complete without a shipper")
	}
	assert.Positive(t, h.Dropped())
}

func TestRingHandler_ConcurrentHandleSetSnapshot(t *testing.T) {
	t.Parallel()
	h, lv := newTestHandler(t, slog.LevelDebug, WithRingCapacity(64))
	logger := slog.New(h)

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for j := range 200 {
				logger.Info("m", slog.Int("i", i), slog.Int("j", j))
			}
		})
	}
	wg.Go(func() {
		for _, l := range []slog.Level{slog.LevelWarn, slog.LevelDebug, slog.LevelError, slog.LevelInfo} {
			lv.Set(l)
			_ = h.Snapshot()
		}
	})
	wg.Wait()

	snap := h.Snapshot()
	assert.Len(t, snap, 64)
}
