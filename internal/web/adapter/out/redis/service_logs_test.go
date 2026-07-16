package redis

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/logging"
)

func TestParseEntries_FieldsMapped(t *testing.T) {
	t.Parallel()
	lines := []string{
		`{"ts":"2026-07-15T12:00:01Z","level":"warn","service":"sender","msg":"redirect","attrs":{"node":"n1","status":301}}`,
	}
	got := parseEntries(lines, "sender", logging.NewNoop())
	require.Len(t, got, 1)
	e := got[0]
	assert.Equal(t, time.Date(2026, 7, 15, 12, 0, 1, 0, time.UTC), e.TS.UTC())
	assert.Equal(t, "warn", e.Level)
	assert.Equal(t, "sender", e.Service)
	assert.Equal(t, "redirect", e.Msg)
	assert.Equal(t, "n1", e.Attrs["node"])
	assert.InDelta(t, 301, e.Attrs["status"], 0) // JSON-число → float64
}

func TestParseEntries_SkipsMalformedKeepsValid(t *testing.T) {
	t.Parallel()
	lines := []string{
		`{"ts":"2026-07-15T12:00:01Z","level":"info","service":"web","msg":"ok1"}`,
		`{"ts":`, // обрубок
		`not json at all`,
		`{"ts":"2026-07-15T12:00:03Z","level":"info","service":"web","msg":"ok2"}`,
	}
	got := parseEntries(lines, "web", logging.NewNoop())
	require.Len(t, got, 2)
	assert.Equal(t, "ok1", got[0].Msg)
	assert.Equal(t, "ok2", got[1].Msg)
}

func TestParseEntries_EmptyAndServiceFallback(t *testing.T) {
	t.Parallel()
	assert.Empty(t, parseEntries(nil, "web", logging.NewNoop()))
	assert.Empty(t, parseEntries([]string{}, "web", logging.NewNoop()))

	// Записи без service получают имя ключа.
	got := parseEntries([]string{`{"ts":"2026-07-15T12:00:01Z","level":"info","msg":"m"}`}, "receiver", logging.NewNoop())
	require.Len(t, got, 1)
	assert.Equal(t, "receiver", got[0].Service)
}
