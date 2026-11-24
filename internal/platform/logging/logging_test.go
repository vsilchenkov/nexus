package logging_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/logging"
)

// captureTransport — транспорт Sentry, складывающий события в память.
type captureTransport struct {
	mu     sync.Mutex
	events []*sentry.Event
}

func (*captureTransport) Configure(sentry.ClientOptions)        {}
func (*captureTransport) Flush(time.Duration) bool              { return true }
func (*captureTransport) FlushWithContext(context.Context) bool { return true }
func (*captureTransport) Close()                                {}

func (t *captureTransport) SendEvent(e *sentry.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, e)
}

func (t *captureTransport) captured() []*sentry.Event {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]*sentry.Event(nil), t.events...)
}

// hubContext — контекст с собственным hub'ом, как у запроса после
// sentry-middleware: глобальный CurrentHub тест не трогает.
func hubContext(t *testing.T) (context.Context, *captureTransport) {
	t.Helper()
	tr := &captureTransport{}
	client, err := sentry.NewClient(sentry.ClientOptions{Dsn: "http://public@127.0.0.1:1/1", Transport: tr})
	require.NoError(t, err)
	return sentry.SetHubOnContext(context.Background(), sentry.NewHub(client, sentry.NewScope())), tr
}

// TestSentryHandler_ErrorLogBecomesEvent — ошибки сервисов попадают в Sentry
// только событием этого хендлера (safego.Recover, recovery-middleware,
// logger.Error). sentry-go/slog с v0.48 событий не создаёт: при логгере
// v1.7.9 обновление sentry-go молча выключило бы Sentry целиком. Атрибуты —
// теги события: их маскирует platform/sentry (BeforeSend).
func TestSentryHandler_ErrorLogBecomesEvent(t *testing.T) {
	t.Parallel()
	ctx, tr := hubContext(t)
	log := logging.NewLogger(slog.New(logging.SentryHandler(slog.LevelError))).WithContext(ctx)

	log.Warn("below threshold", log.Str("node", "orders"))
	log.Error("send failed", log.Err(errors.New("connection refused")), log.Str("node", "orders"), log.Op("sender.send"))

	events := tr.captured()
	require.Len(t, events, 1, "only the error record must become an event")
	e := events[0]
	assert.Equal(t, sentry.LevelError, e.Level)
	assert.Equal(t, "send failed", e.Message)
	require.NotEmpty(t, e.Exception)
	assert.Equal(t, "connection refused", e.Exception[len(e.Exception)-1].Value)
	assert.Equal(t, "orders", e.Tags["node"])
	assert.Equal(t, "sender.send", e.Tags["op"])
}
