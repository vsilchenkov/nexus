package chlog_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/config"
	"nexus/internal/platform/logging"
	"nexus/internal/sender/adapter/out/chlog"
)

// fixedMasker — тестовый masker: вырезает подстроку SECRET. Проверяет, что Writer
// применяет masker к нужным полям (§95), не завися от реального regexp-набора.
type fixedMasker struct{}

func (fixedMasker) Mask(s string) string { return strings.ReplaceAll(s, "SECRET", "***") }

var _ chlog.LogMasker = fixedMasker{}

// capturingRetrier сохраняет записи финального flush (Conn==nil → insertBatch
// падает → батч уходит в retrier), чтобы проверить итоговые значения полей.
type capturingRetrier struct {
	mu   sync.Mutex
	recs []*domain.LogRecord
}

func (c *capturingRetrier) Retry(_ context.Context, _ string, batch []*domain.LogRecord) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recs = append(c.recs, batch...)
	return nil
}

func (c *capturingRetrier) records() []*domain.LogRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.recs
}

func maskTestCfg() *config.ClickHouseSection {
	return &config.ClickHouseSection{BatchSize: 100, FlushIntervalSec: 60, BufferMaxSize: 64, Workers: 1}
}

func sampleRecord() *domain.LogRecord {
	return &domain.LogRecord{
		ID:         "rec-1",
		URL:        "https://api.telegram.org/botSECRET/getUpdates",
		Method:     "botSECRET/getUpdates",
		Parameters: "token=SECRET",
		Request:    "payload with SECRET inside",
		Response:   "response with SECRET inside",
		Host:       "SECRET-host",
	}
}

// TestWriter_Masks_URLMethodParameters — masker применяется к url/method/
// parameters ПЕРЕД записью. Red на старом коде (без поля masker url оставался
// сырым).
func TestWriter_Masks_URLMethodParameters(t *testing.T) {
	t.Parallel()

	retrier := &capturingRetrier{}
	w := chlog.NewWithRetrier(&stubProvider{}, maskTestCfg(), retrier, nil, fixedMasker{}, logging.NewNoop())
	w.Write(context.Background(), "test_table", sampleRecord())
	w.Stop(context.Background())

	recs := retrier.records()
	require.Len(t, recs, 1)
	got := recs[0]
	require.Equal(t, "https://api.telegram.org/bot***/getUpdates", got.URL, "url должен быть замаскирован")
	require.Equal(t, "bot***/getUpdates", got.Method, "method (§39 RequestPath) должен быть замаскирован")
	require.Equal(t, "token=***", got.Parameters, "parameters должны быть замаскированы")
}

// TestWriter_Masks_LeavesBodiesAndOtherFields — маскирование НЕ трогает тела и
// прочие поля (граница §95 v1: только url/method/parameters).
func TestWriter_Masks_LeavesBodiesAndOtherFields(t *testing.T) {
	t.Parallel()

	retrier := &capturingRetrier{}
	w := chlog.NewWithRetrier(&stubProvider{}, maskTestCfg(), retrier, nil, fixedMasker{}, logging.NewNoop())
	w.Write(context.Background(), "test_table", sampleRecord())
	w.Stop(context.Background())

	got := retrier.records()[0]
	require.Equal(t, "payload with SECRET inside", got.Request, "тело запроса вне рамок v1 — не трогаем")
	require.Equal(t, "response with SECRET inside", got.Response, "тело ответа вне рамок v1 — не трогаем")
	require.Equal(t, "SECRET-host", got.Host, "прочие поля не маскируются")
}

// TestWriter_NilMasker_NoChange — без masker'а (nil) поля не меняются: обратная
// совместимость и режим «маскирование выключено».
func TestWriter_NilMasker_NoChange(t *testing.T) {
	t.Parallel()

	retrier := &capturingRetrier{}
	w := chlog.NewWithRetrier(&stubProvider{}, maskTestCfg(), retrier, nil, nil, logging.NewNoop())
	w.Write(context.Background(), "test_table", sampleRecord())
	w.Stop(context.Background())

	got := retrier.records()[0]
	require.Equal(t, "https://api.telegram.org/botSECRET/getUpdates", got.URL)
	require.Equal(t, "botSECRET/getUpdates", got.Method)
	require.Equal(t, "token=SECRET", got.Parameters)
}
