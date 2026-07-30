package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/logging"
	"nexus/internal/platform/logsink"
)

// captureHandler — записывающий slog.Handler с собственным порогом
// (стаб base/sentry-хендлеров при проверке fan-out цепочки).
type captureHandler struct {
	level slog.Leveler

	mu      sync.Mutex
	records []slog.Record
}

func (h *captureHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level.Level()
}

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

func (h *captureHandler) msgs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.records))
	for i, r := range h.records {
		out[i] = r.Message
	}
	return out
}

func TestSlogLevelFromInt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   int
		want slog.Level
	}{
		{"debug", 5, slog.LevelDebug},
		{"info", 4, slog.LevelInfo},
		{"warn", 3, slog.LevelWarn},
		{"error", 2, slog.LevelError},
		{"below range -> info", 1, slog.LevelInfo},
		{"above range -> info", 6, slog.LevelInfo},
		{"zero -> info", 0, slog.LevelInfo},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, slogLevelFromInt(tc.in))
		})
	}
}

func TestBuildBaseHandler_TintWhenNotFile(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	lv := new(slog.LevelVar)
	lv.Set(slog.LevelInfo)
	cfg := &logging.Config{Level: 4, OutputInFile: false}

	h, closer := buildBaseHandler(cfg, lv, &buf)
	require.Nil(t, closer)
	slog.New(h).Info("tint line", slog.String("node", "n1"))

	out := buf.String()
	require.NotEmpty(t, out)
	// Формат вендора: текст (не JSON) с временем "15:04:05".
	assert.Regexp(t, regexp.MustCompile(`\d{2}:\d{2}:\d{2}`), out)
	assert.Contains(t, out, "tint line")
	assert.False(t, json.Valid(buf.Bytes()), "tint output must not be JSON")
}

func TestBuildBaseHandler_JSONFileWhenOutputInFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	lv := new(slog.LevelVar)
	lv.Set(slog.LevelInfo)
	cfg := &logging.Config{Level: 4, OutputInFile: true, Dir: "logs"}
	cfg.WorkingDir = dir

	h, closer := buildBaseHandler(cfg, lv, os.Stderr)
	require.NotNil(t, closer)
	slog.New(h).Info("json line", slog.String("node", "n1"))
	require.NoError(t, closer.Close()) // Windows: иначе TempDir cleanup не удалит файл

	data, err := os.ReadFile(filepath.Join(dir, "logs", "app.log"))
	require.NoError(t, err)
	var rec map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(data), &rec))
	assert.Equal(t, "json line", rec["msg"])
	assert.Equal(t, "INFO", rec["level"])
	// Parity с вендором: время сериализуется строкой "2006-01-02 15:04:05".
	assert.Regexp(t, regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$`), rec["time"])
}

func TestBuildBaseHandler_JSONFallbackWhenFileUnopenable(t *testing.T) {
	t.Parallel()
	// WorkingDir указывает на ФАЙЛ → MkdirAll внутри OutputLogFile падает →
	// fallback на JSON в console (parity с вендором).
	tmp := t.TempDir()
	blocker := filepath.Join(tmp, "not-a-dir")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))

	var buf bytes.Buffer
	lv := new(slog.LevelVar)
	lv.Set(slog.LevelInfo)
	cfg := &logging.Config{Level: 4, OutputInFile: true, Dir: "logs"}
	cfg.WorkingDir = filepath.Join(blocker, "deeper")

	h, closer := buildBaseHandler(cfg, lv, &buf)
	require.Nil(t, closer)
	slog.New(h).Info("fallback line")

	var rec map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec))
	assert.Equal(t, "fallback line", rec["msg"])
}

func TestAssembleChain_FanOutRespectsPerHandlerLevels(t *testing.T) {
	t.Parallel()
	lv := new(slog.LevelVar)
	lv.Set(slog.LevelInfo)
	base := &captureHandler{level: lv}
	ring := logsink.NewRingHandler(lv, "web")
	sentry := &captureHandler{level: slog.LevelError} // порог Sentry фиксированный

	logger := slog.New(assembleChain(base, ring, sentry))
	logger.Info("info msg")
	logger.Error("error msg")

	assert.Equal(t, []string{"info msg", "error msg"}, base.msgs())
	require.Len(t, ring.Snapshot(), 2)
	// Sentry-стаб получил только Error — Info ниже его порога.
	assert.Equal(t, []string{"error msg"}, sentry.msgs())
}

func TestAssembleChain_NoSentryWhenNil(t *testing.T) {
	t.Parallel()
	lv := new(slog.LevelVar)
	lv.Set(slog.LevelInfo)
	base := &captureHandler{level: lv}
	ring := logsink.NewRingHandler(lv, "web")

	logger := slog.New(assembleChain(base, ring, nil))
	logger.Info("m")

	assert.Equal(t, []string{"m"}, base.msgs())
	assert.Len(t, ring.Snapshot(), 1)
}

func TestLevelVar_RuntimeChangeAffectsBaseAndRingNotSentry(t *testing.T) {
	t.Parallel()
	lv := new(slog.LevelVar)
	lv.Set(slog.LevelInfo)
	base := &captureHandler{level: lv}
	ring := logsink.NewRingHandler(lv, "web")
	sentry := &captureHandler{level: slog.LevelError}
	logger := slog.New(assembleChain(base, ring, sentry))

	logger.Debug("d1") // lv=Info → мимо base и ring
	require.Empty(t, base.msgs())
	require.Empty(t, ring.Snapshot())

	lv.Set(slog.LevelDebug) // runtime-включение debug
	logger.Debug("d2")
	assert.Equal(t, []string{"d2"}, base.msgs())
	assert.Len(t, ring.Snapshot(), 1)

	lv.Set(slog.LevelWarn) // runtime-глушение info ВЕЗДЕ
	logger.Info("i1")
	logger.Error("e1")
	assert.Equal(t, []string{"d2", "e1"}, base.msgs())
	assert.Len(t, ring.Snapshot(), 2)
	// Порог Sentry не зависит от LevelVar: получил только e1.
	assert.Equal(t, []string{"e1"}, sentry.msgs())
}

func TestBuildLogger_ReturnsController(t *testing.T) {
	t.Parallel()
	logCfg := &logging.Config{Level: 3, OutputInFile: false}
	sentryCfg := &logging.SentryConfig{Use: false}

	logger, ctl := buildLogger(logCfg, sentryCfg, "web", "")

	require.NotNil(t, logger)
	require.NotNil(t, ctl)
	require.NotNil(t, ctl.Level)
	require.NotNil(t, ctl.Ring)
	assert.Equal(t, slog.LevelWarn, ctl.Level.Level())
	assert.Equal(t, 3, ctl.FallbackLevel())
	assert.Equal(t, "web", ctl.Ring.Service())
}

func TestLogController_StartRedisShipper_NilSafeAndOnce(t *testing.T) {
	t.Parallel()
	logger := logging.NewNoop()

	// nil-контроллер и nil-redis — no-op с уже закрытым каналом.
	var nilCtl *LogController
	done := nilCtl.StartRedisShipper(context.Background(), nil, logger)
	select {
	case <-done:
	default:
		t.Fatal("nil controller must return a closed channel")
	}

	_, ctl := buildLogger(&logging.Config{Level: 4}, &logging.SentryConfig{}, "web", "")
	done = ctl.StartRedisShipper(context.Background(), nil, logger)
	select {
	case <-done:
	default:
		t.Fatal("nil redis must return a closed channel")
	}

	// Первый вызов с клиентом запускает шиппер; повторный — no-op.
	rdb := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:1"})
	defer rdb.Close()
	ctx, cancel := context.WithCancel(context.Background())
	first := ctl.StartRedisShipper(ctx, rdb, logger)
	second := ctl.StartRedisShipper(ctx, rdb, logger)
	select {
	case <-second:
	default:
		t.Fatal("second StartRedisShipper must be a no-op with a closed channel")
	}

	cancel()
	select {
	case <-first:
	case <-time.After(5 * time.Second):
		t.Fatal("shipper did not stop after ctx cancel")
	}
}
