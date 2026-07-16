package usecase

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
)

// fakeServiceLogReader — фейк port.ServiceLogReader: entries по сервисам,
// опциональные ошибки, фиксация запрошенных сервисов.
type fakeServiceLogReader struct {
	mu      sync.Mutex
	entries map[string][]domain.ServiceLogEntry
	errs    map[string]error
	asked   []string
}

func (f *fakeServiceLogReader) Tail(_ context.Context, service string, limit int) ([]domain.ServiceLogEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, service)
	if err := f.errs[service]; err != nil {
		return nil, err
	}
	out := f.entries[service]
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func slEntry(svc, level string, ts time.Time, msg string) domain.ServiceLogEntry {
	return domain.ServiceLogEntry{TS: ts, Level: level, Service: svc, Msg: msg}
}

var slBase = time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)

func TestServiceLogsTail_MergesByTSDesc(t *testing.T) {
	t.Parallel()
	reader := &fakeServiceLogReader{entries: map[string][]domain.ServiceLogEntry{
		"receiver": {slEntry("receiver", "info", slBase.Add(3*time.Second), "r1"), slEntry("receiver", "info", slBase, "r0")},
		"sender":   {slEntry("sender", "info", slBase.Add(4*time.Second), "s1"), slEntry("sender", "info", slBase.Add(1*time.Second), "s0")},
		"web":      {slEntry("web", "info", slBase.Add(2*time.Second), "w0")},
	}}
	uc := NewServiceLogsUsecase(reader, logging.NewNoop())

	got, err := uc.Tail(context.Background(), nil, 0, "")
	require.NoError(t, err)

	msgs := make([]string, len(got))
	for i, e := range got {
		msgs[i] = e.Msg
	}
	assert.Equal(t, []string{"s1", "r1", "w0", "s0", "r0"}, msgs, "merged strictly by TS desc")
	assert.ElementsMatch(t, []string{"receiver", "sender", "web"}, reader.asked,
		"empty services must read all three")
}

func TestServiceLogsTail_LimitAppliedAfterMerge(t *testing.T) {
	t.Parallel()
	reader := &fakeServiceLogReader{entries: map[string][]domain.ServiceLogEntry{
		"receiver": {slEntry("receiver", "info", slBase.Add(5*time.Second), "r1"), slEntry("receiver", "info", slBase.Add(1*time.Second), "r0")},
		"sender":   {slEntry("sender", "info", slBase.Add(4*time.Second), "s1"), slEntry("sender", "info", slBase.Add(2*time.Second), "s0")},
	}}
	uc := NewServiceLogsUsecase(reader, logging.NewNoop())

	got, err := uc.Tail(context.Background(), []string{"receiver", "sender"}, 3, "")
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, "r1", got[0].Msg)
	assert.Equal(t, "s1", got[1].Msg)
	assert.Equal(t, "s0", got[2].Msg, "cut keeps the freshest across services")
}

func TestServiceLogsTail_MinLevelFilter(t *testing.T) {
	t.Parallel()
	reader := &fakeServiceLogReader{entries: map[string][]domain.ServiceLogEntry{
		"web": {
			slEntry("web", "debug", slBase.Add(4*time.Second), "d"),
			slEntry("web", "info", slBase.Add(3*time.Second), "i"),
			slEntry("web", "warn", slBase.Add(2*time.Second), "w"),
			slEntry("web", "error", slBase.Add(1*time.Second), "e"),
			slEntry("web", "debug-4", slBase, "weird"), // неизвестный ранг — не прячем
		},
	}}
	uc := NewServiceLogsUsecase(reader, logging.NewNoop())

	got, err := uc.Tail(context.Background(), []string{"web"}, 0, "warn")
	require.NoError(t, err)
	msgs := make([]string, len(got))
	for i, e := range got {
		msgs[i] = e.Msg
	}
	assert.Equal(t, []string{"w", "e", "weird"}, msgs)
}

func TestServiceLogsTail_ServiceFilterReadsOneKey(t *testing.T) {
	t.Parallel()
	reader := &fakeServiceLogReader{entries: map[string][]domain.ServiceLogEntry{
		"sender": {slEntry("sender", "info", slBase, "s0")},
	}}
	uc := NewServiceLogsUsecase(reader, logging.NewNoop())

	got, err := uc.Tail(context.Background(), []string{"sender"}, 0, "")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, []string{"sender"}, reader.asked)
}

func TestServiceLogsTail_AllKeywordAndDedup(t *testing.T) {
	t.Parallel()
	reader := &fakeServiceLogReader{entries: map[string][]domain.ServiceLogEntry{}}
	uc := NewServiceLogsUsecase(reader, logging.NewNoop())

	_, err := uc.Tail(context.Background(), []string{"sender", "all"}, 0, "")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"receiver", "sender", "web"}, reader.asked)

	reader.asked = nil
	_, err = uc.Tail(context.Background(), []string{"web", "web"}, 0, "")
	require.NoError(t, err)
	assert.Equal(t, []string{"web"}, reader.asked, "duplicates must be deduped")
}

func TestServiceLogsTail_InvalidParams(t *testing.T) {
	t.Parallel()
	uc := NewServiceLogsUsecase(&fakeServiceLogReader{}, logging.NewNoop())

	_, err := uc.Tail(context.Background(), []string{"oops"}, 0, "")
	assert.ErrorIs(t, err, domain.ErrServiceLogInvalidService)

	_, err = uc.Tail(context.Background(), nil, 0, "verbose")
	assert.ErrorIs(t, err, domain.ErrServiceLogInvalidLevel)
}

func TestServiceLogsTail_LimitClamped(t *testing.T) {
	t.Parallel()
	many := make([]domain.ServiceLogEntry, ServiceLogsMaxLimit+100)
	for i := range many {
		many[i] = slEntry("web", "info", slBase.Add(time.Duration(i)*time.Millisecond), "m")
	}
	reader := &fakeServiceLogReader{entries: map[string][]domain.ServiceLogEntry{"web": many}}
	uc := NewServiceLogsUsecase(reader, logging.NewNoop())

	got, err := uc.Tail(context.Background(), []string{"web"}, ServiceLogsMaxLimit+500, "")
	require.NoError(t, err)
	assert.Len(t, got, ServiceLogsMaxLimit)
}

func TestServiceLogsTail_PartialOnReaderError(t *testing.T) {
	t.Parallel()
	reader := &fakeServiceLogReader{
		entries: map[string][]domain.ServiceLogEntry{
			"receiver": {slEntry("receiver", "info", slBase.Add(time.Second), "r0")},
			"web":      {slEntry("web", "info", slBase, "w0")},
		},
		errs: map[string]error{"sender": errors.New("redis down")},
	}
	uc := NewServiceLogsUsecase(reader, logging.NewNoop())

	got, err := uc.Tail(context.Background(), nil, 0, "")
	require.NoError(t, err, "one broken service must not fail the console")
	require.Len(t, got, 2)
	assert.Equal(t, "r0", got[0].Msg)
	assert.Equal(t, "w0", got[1].Msg)
}

func TestServiceLogsTail_Empty(t *testing.T) {
	t.Parallel()
	uc := NewServiceLogsUsecase(&fakeServiceLogReader{}, logging.NewNoop())
	got, err := uc.Tail(context.Background(), nil, 0, "")
	require.NoError(t, err)
	assert.Empty(t, got)
}
