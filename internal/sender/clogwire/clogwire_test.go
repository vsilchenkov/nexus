package clogwire_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/sender/clogwire"
)

func TestMarshalUnmarshal_Roundtrip(t *testing.T) {
	t.Parallel()
	logs := []*domain.LogRecord{
		{ID: "1", URL: "https://a", Method: "POST", Status: 500, Done: false},
		{ID: "2", URL: "https://b", Method: "GET", Status: 200, Done: true,
			RequestSize: 123, ResponseSize: 4567}, // §42-доп
	}
	b, err := clogwire.Marshal("nexus_default.t", logs)
	require.NoError(t, err)

	env, err := clogwire.Unmarshal(b)
	require.NoError(t, err)
	assert.Equal(t, "nexus_default.t", env.Table)
	require.Len(t, env.Logs, 2)
	assert.Equal(t, "1", env.Logs[0].ID)
	assert.Equal(t, int32(200), env.Logs[1].Status)
	assert.Equal(t, int64(123), env.Logs[1].RequestSize, "§42-доп: размеры переживают Kafka retry")
	assert.Equal(t, int64(4567), env.Logs[1].ResponseSize)
}

// TestUnmarshal_LegacyEnvelopeWithoutSizes — конверт, записанный бинарём до
// §42-доп (без RequestSize/ResponseSize), читается новым кодом: размеры — 0.
// Rolling-совместимость дренажа nexus.logs.retry при смешанных версиях.
func TestUnmarshal_LegacyEnvelopeWithoutSizes(t *testing.T) {
	t.Parallel()
	legacy := []byte(`{"table":"nexus_default.t","logs":[{"ID":"old-1","Status":200,"Done":true}]}`)
	env, err := clogwire.Unmarshal(legacy)
	require.NoError(t, err)
	require.Len(t, env.Logs, 1)
	assert.Equal(t, "old-1", env.Logs[0].ID)
	assert.Zero(t, env.Logs[0].RequestSize)
	assert.Zero(t, env.Logs[0].ResponseSize)
}

func TestUnmarshal_Bad(t *testing.T) {
	t.Parallel()
	_, err := clogwire.Unmarshal([]byte("{not json"))
	assert.Error(t, err)
}

func TestSplit_BySize(t *testing.T) {
	t.Parallel()
	// 10 записей с телом ~200 байт; лимит ~600 → по ~2-3 записи на под-батч.
	body := strings.Repeat("x", 200)
	logs := make([]*domain.LogRecord, 10)
	for i := range logs {
		logs[i] = &domain.LogRecord{ID: "id", Request: body}
	}
	chunks := clogwire.Split(logs, 600)
	assert.Greater(t, len(chunks), 1, "должно порезаться на несколько под-батчей")

	// Ни одна запись не потеряна.
	total := 0
	for _, c := range chunks {
		total += len(c)
	}
	assert.Equal(t, 10, total)
}

func TestSplit_NoLimit(t *testing.T) {
	t.Parallel()
	logs := []*domain.LogRecord{{ID: "1"}, {ID: "2"}}
	chunks := clogwire.Split(logs, 0)
	require.Len(t, chunks, 1)
	assert.Len(t, chunks[0], 2)
}

func TestSplit_Empty(t *testing.T) {
	t.Parallel()
	assert.Nil(t, clogwire.Split(nil, 100))
}

// TestSplit_OversizeRecord — запись больше лимита всё равно уходит отдельным
// под-батчем (не теряется), а не выбрасывается.
func TestSplit_OversizeRecord(t *testing.T) {
	t.Parallel()
	logs := []*domain.LogRecord{
		{ID: "small"},
		{ID: "big", Request: strings.Repeat("y", 5000)},
		{ID: "small2"},
	}
	chunks := clogwire.Split(logs, 1000)
	total := 0
	for _, c := range chunks {
		total += len(c)
	}
	assert.Equal(t, 3, total, "oversize-запись не должна теряться")
}
