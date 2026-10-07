package usecase

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/domain/logsearch"
	"nexus/internal/platform/clock"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// exportReaderFake — журнал из n записей; строки отдаются по ключам, кроме
// «пропавших». Ошибка чтения строк — на батче с номером failAt (1-based).
type exportReaderFake struct {
	n         int
	vanished  map[string]bool
	bodyRunes int64 // полная длина тел каждой записи
	keysErr   error
	rowsErr   error
	failAt    int
	cancel    context.CancelFunc // зовётся перед ошибкой failAt (уход клиента)

	keysLimit  int
	batches    []int
	bodyArgs   []int
	statsCap   int64
	lastQuery  port.LogQuery
	statsQuery port.LogQuery
}

func (f *exportReaderFake) ExportStats(_ context.Context, q port.LogQuery, capBytes int64) (port.LogExportStats, error) {
	f.statsQuery, f.statsCap = q, capBytes
	return port.LogExportStats{Records: uint64(f.n), Rows: uint64(f.n)}, nil
}

func (f *exportReaderFake) ExportKeys(_ context.Context, q port.LogQuery, limit int) ([]port.LogExportKey, error) {
	f.lastQuery, f.keysLimit = q, limit
	if f.keysErr != nil {
		return nil, f.keysErr
	}
	keys := make([]port.LogExportKey, 0, min(f.n, limit))
	for i := range min(f.n, limit) {
		keys = append(keys, port.LogExportKey{ID: fmt.Sprintf("id-%03d", i), TsMs: int64(1000 - i)})
	}
	return keys, nil
}

func (f *exportReaderFake) ExportRows(ctx context.Context, _ port.LogQuery, keys []port.LogExportKey, bodyRunes int) ([]port.LogExportRow, error) {
	f.batches = append(f.batches, len(keys))
	f.bodyArgs = append(f.bodyArgs, bodyRunes)
	if f.failAt == len(f.batches) {
		if f.cancel != nil {
			f.cancel()
			return nil, ctx.Err()
		}
		return nil, f.rowsErr
	}
	rows := make([]port.LogExportRow, 0, len(keys))
	for _, k := range keys {
		if f.vanished[k.ID] {
			continue
		}
		rows = append(rows, port.LogExportRow{
			Rec:      domain.LogRecord{ID: k.ID, Request: strings.Repeat("r", 10)},
			ReqRunes: f.bodyRunes, RespRunes: f.bodyRunes,
		})
	}
	return rows, nil
}

// exportSinkFake — запоминает поток; ошибка Row — на строке failRow (1-based).
type exportSinkFake struct {
	begun    bool
	meta     LogsExportMeta
	ids      []string
	finished *LogsExportSummary
	beginErr error
	failRow  int
}

func (s *exportSinkFake) Begin(_ context.Context, m LogsExportMeta) error {
	s.begun, s.meta = true, m
	return s.beginErr
}

func (s *exportSinkFake) Row(_ context.Context, r port.LogExportRow) error {
	if s.failRow == len(s.ids)+1 {
		return errors.New("write: broken pipe")
	}
	s.ids = append(s.ids, r.Rec.ID)
	return nil
}

func (s *exportSinkFake) Finish(_ context.Context, sum LogsExportSummary) error {
	s.finished = &sum
	return nil
}

type exportAuditFake struct {
	entries []map[string]any
	ctxErr  []error
}

func (a *exportAuditFake) Log(ctx context.Context, _ Actor, action, targetType, _ string, details map[string]any) {
	if action == domain.ActionNodeLogsExport && targetType == "node" {
		a.entries = append(a.entries, details)
		a.ctxErr = append(a.ctxErr, ctx.Err())
	}
}

type exportLimiterFake struct {
	allow bool
	err   error
	keys  []string
}

func (l *exportLimiterFake) Allow(_ context.Context, key string, _ int) (bool, error) {
	l.keys = append(l.keys, key)
	return l.allow, l.err
}

type exportFixture struct {
	uc      *LogsExportUsecase
	reader  *exportReaderFake
	audit   *exportAuditFake
	limiter *exportLimiterFake
}

func newExportFixture(n int, mutate func(*LogsExportLimits)) exportFixture {
	limits := LogsExportLimits{
		MaxRows: 1000, MaxBytes: 1 << 30, MaxBodyChars: 100,
		MaxConcurrent: 1, RatePerMinute: 5, QueryTimeout: time.Minute,
	}
	if mutate != nil {
		mutate(&limits)
	}
	f := exportFixture{
		reader:  &exportReaderFake{n: n},
		audit:   &exportAuditFake{},
		limiter: &exportLimiterFake{allow: true},
	}
	f.uc = NewLogsExportUsecase(LogsExportDeps{
		Reader: f.reader, Audit: f.audit, Limiter: f.limiter,
		Nodes:  &fakeNodeRepo{node: &domain.Node{ID: "n1", TeamID: "t1", ClickHouseTable: "db.logs"}},
		Clock:  clock.Fixed(time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)),
		Limits: limits, Logger: logging.NewNoop(),
	})
	return f
}

var exportActor = Actor{UserID: "u1", UserLogin: "alice"}

func (f exportFixture) export(t *testing.T, ctx context.Context, opt LogsExportOptions, sink *exportSinkFake) LogsExportSummary {
	t.Helper()
	sum, err := f.uc.Export(ctx, exportActor, "n1", "t1", port.LogQuery{}, opt, sink)
	require.NoError(t, err)
	return sum
}

func TestLogsExport_Complete(t *testing.T) {
	t.Parallel()
	f := newExportFixture(3, nil)
	sink := &exportSinkFake{}
	q := port.LogQuery{Status: "err", Unresolved: true, BeforeID: "cursor", Limit: 100}

	sum, err := f.uc.Export(context.Background(), exportActor, "n1", "t1", q, LogsExportOptions{Bodies: true}, sink)
	require.NoError(t, err)

	assert.Equal(t, LogsExportComplete, sum.Status)
	assert.Equal(t, 3, sum.Written)
	assert.Equal(t, []string{"id-000", "id-001", "id-002"}, sink.ids, "порядок ключей сохраняется")
	require.NotNil(t, sink.finished)
	assert.Equal(t, 3, sink.meta.Selected)
	assert.False(t, sink.meta.More)
	assert.Equal(t, 100, sink.meta.BodyChars)
	assert.Equal(t, 1001, f.reader.keysLimit, "ключей просят на один больше предела — видно, что есть ещё")

	// Параметры, которые в выгрузке смысла не имеют, сняты; узел подставлен.
	got := f.reader.lastQuery
	assert.Equal(t, "db.logs", got.Table)
	assert.Equal(t, "n1", got.NodeID)
	assert.True(t, got.DateCreateAligned)
	assert.Equal(t, "err", got.Status)
	assert.False(t, got.Unresolved)
	assert.Empty(t, got.BeforeID)
	assert.Zero(t, got.Limit)

	require.Len(t, f.audit.entries, 1)
	assert.Equal(t, "complete", f.audit.entries[0]["result"])
	assert.Equal(t, []string{"logs_export:u1"}, f.limiter.keys)
}

func TestLogsExport_LimitedRows(t *testing.T) {
	t.Parallel()
	f := newExportFixture(10, nil)
	sink := &exportSinkFake{}
	sum := f.export(t, context.Background(), LogsExportOptions{Rows: 4}, sink)

	assert.Equal(t, LogsExportLimitedRows, sum.Status)
	assert.Equal(t, 4, sum.Written)
	assert.True(t, sink.meta.More)
	assert.Equal(t, 4, sink.meta.Selected)
	assert.Equal(t, 5, f.reader.keysLimit)
}

func TestLogsExport_RowsClampedToMax(t *testing.T) {
	t.Parallel()
	for _, rows := range []int{0, -1, 5000} {
		f := newExportFixture(1, func(l *LogsExportLimits) { l.MaxRows = 50 })
		sink := &exportSinkFake{}
		f.export(t, context.Background(), LogsExportOptions{Rows: rows}, sink)
		assert.Equal(t, 50, sink.meta.Rows, rows)
	}
}

func TestLogsExport_LimitedBytes(t *testing.T) {
	t.Parallel()
	// Строка фейка — 6 байт ID + 10 байт тела + надбавка 64 = 80 байт.
	f := newExportFixture(300, func(l *LogsExportLimits) { l.MaxBytes = 80*120 + 10 })
	sink := &exportSinkFake{}
	sum := f.export(t, context.Background(), LogsExportOptions{}, sink)

	assert.Equal(t, LogsExportLimitedBytes, sum.Status)
	assert.Equal(t, 120, sum.Written, "строка, превышающая бюджет, не пишется")
	assert.LessOrEqual(t, sum.Bytes, int64(80*120+10))
	assert.Equal(t, []int{100, 100}, f.reader.batches, "после исчерпания бюджета батчи не читаются")
	require.NotNil(t, sink.finished)
	assert.Equal(t, LogsExportLimitedBytes, sink.finished.Status)
}

func TestLogsExport_BatchSizes(t *testing.T) {
	t.Parallel()
	f := newExportFixture(120, nil)
	f.export(t, context.Background(), LogsExportOptions{}, &exportSinkFake{})
	assert.Equal(t, []int{100, 20}, f.reader.batches)
	assert.Equal(t, []int{0, 0}, f.reader.bodyArgs, "без тел ячейки тел не читаются")

	g := newExportFixture(120, nil)
	g.export(t, context.Background(), LogsExportOptions{Bodies: true}, &exportSinkFake{})
	assert.Equal(t, []int{50, 50, 20}, g.reader.batches)
	assert.Equal(t, []int{100, 100, 100}, g.reader.bodyArgs)
}

func TestLogsExport_VanishedAndTruncated(t *testing.T) {
	t.Parallel()
	f := newExportFixture(5, nil)
	f.reader.vanished = map[string]bool{"id-001": true, "id-003": true}
	f.reader.bodyRunes = 101 // длиннее MaxBodyChars=100 — оба тела урезаны
	sink := &exportSinkFake{}
	sum := f.export(t, context.Background(), LogsExportOptions{Bodies: true}, sink)

	assert.Equal(t, LogsExportComplete, sum.Status)
	assert.Equal(t, 3, sum.Written)
	assert.Equal(t, 2, sum.Vanished)
	assert.Equal(t, 6, sum.TruncatedBodies)
	assert.Equal(t, []string{"id-000", "id-002", "id-004"}, sink.ids)
}

func TestLogsExport_IncompleteOnClickHouseFailure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		err    error
		reason string
	}{
		{"timeout", fmt.Errorf("export rows: %w", context.DeadlineExceeded), "timeout"},
		{"unavailable", errors.Join(errors.New("dial"), domain.ErrLogsBackendUnavailable), "unavailable"},
		{"other", errors.New("code: 241, memory limit"), "query_failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newExportFixture(250, nil)
			f.reader.failAt, f.reader.rowsErr = 2, tt.err
			sink := &exportSinkFake{}
			sum := f.export(t, context.Background(), LogsExportOptions{}, sink)

			assert.Equal(t, LogsExportIncomplete, sum.Status)
			assert.Equal(t, tt.reason, sum.Reason)
			assert.Equal(t, 100, sum.Written)
			require.NotNil(t, sink.finished, "файл закрывается корректно")
			assert.Equal(t, LogsExportIncomplete, sink.finished.Status)
			require.Len(t, f.audit.entries, 1)
			assert.Equal(t, tt.reason, f.audit.entries[0]["reason"])
		})
	}
}

func TestLogsExport_AbortedWithoutFinish(t *testing.T) {
	t.Parallel()
	t.Run("client gone during read", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		f := newExportFixture(250, nil)
		f.reader.failAt, f.reader.cancel = 2, cancel
		sink := &exportSinkFake{}
		sum := f.export(t, ctx, LogsExportOptions{}, sink)

		assert.Equal(t, LogsExportAborted, sum.Status)
		assert.Equal(t, "read", sum.Reason)
		assert.Nil(t, sink.finished, "ушедшему клиенту файл не дописывается")
		require.Len(t, f.audit.entries, 1, "аудит пишется и при обрыве")
		assert.NoError(t, f.audit.ctxErr[0], "аудит идёт в контексте без отмены")
	})
	t.Run("write fails", func(t *testing.T) {
		t.Parallel()
		f := newExportFixture(5, nil)
		sink := &exportSinkFake{failRow: 3}
		sum := f.export(t, context.Background(), LogsExportOptions{}, sink)
		assert.Equal(t, LogsExportAborted, sum.Status)
		assert.Equal(t, "write", sum.Reason)
		assert.Equal(t, 2, sum.Written)
		assert.Nil(t, sink.finished)
	})
	t.Run("begin fails", func(t *testing.T) {
		t.Parallel()
		f := newExportFixture(5, nil)
		sink := &exportSinkFake{beginErr: errors.New("conn reset")}
		sum := f.export(t, context.Background(), LogsExportOptions{}, sink)
		assert.Equal(t, LogsExportAborted, sum.Status)
		assert.Empty(t, f.reader.batches)
	})
}

func TestLogsExport_ErrorsBeforeStream(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		prepare func(*exportFixture) (teamID string, q port.LogQuery)
		wantErr error
	}{
		{"bad search query", func(*exportFixture) (string, port.LogQuery) {
			return "t1", port.LogQuery{Q: "(", QRegex: true}
		}, logsearch.ErrBadQuery},
		{"foreign team", func(*exportFixture) (string, port.LogQuery) {
			return "t2", port.LogQuery{}
		}, domain.ErrNodeNotFound},
		{"keys failed", func(f *exportFixture) (string, port.LogQuery) {
			f.reader.keysErr = domain.ErrLogsBackendUnavailable
			return "t1", port.LogQuery{}
		}, domain.ErrLogsBackendUnavailable},
		{"rate limited", func(f *exportFixture) (string, port.LogQuery) {
			f.limiter.allow = false
			return "t1", port.LogQuery{}
		}, ErrLogsExportRateLimited},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newExportFixture(3, nil)
			teamID, q := tt.prepare(&f)
			sink := &exportSinkFake{}
			_, err := f.uc.Export(context.Background(), exportActor, "n1", teamID, q, LogsExportOptions{}, sink)
			require.ErrorIs(t, err, tt.wantErr)
			assert.False(t, sink.begun, "до ошибки в ответ ничего не ушло")
			assert.Empty(t, f.audit.entries, "нечего фиксировать — ничего не выгружено")
			assert.Empty(t, f.uc.slots, "слот освобождён")
		})
	}
}

func TestLogsExport_Slots(t *testing.T) {
	t.Parallel()
	f := newExportFixture(3, nil)
	f.uc.slots <- struct{}{} // другая выгрузка занимает единственный слот
	_, err := f.uc.Export(context.Background(), exportActor, "n1", "t1", port.LogQuery{}, LogsExportOptions{}, &exportSinkFake{})
	require.ErrorIs(t, err, ErrLogsExportBusy)
	assert.Empty(t, f.limiter.keys, "попытка без слота не расходует частоту (nginx повторит её на соседней реплике)")

	<-f.uc.slots
	f.export(t, context.Background(), LogsExportOptions{}, &exportSinkFake{})
	assert.Empty(t, f.uc.slots, "слот освобождается по завершении")
}

func TestLogsExport_RateLimiterFailOpen(t *testing.T) {
	t.Parallel()
	f := newExportFixture(1, nil)
	f.limiter.allow, f.limiter.err = false, errors.New("redis down")
	sum := f.export(t, context.Background(), LogsExportOptions{}, &exportSinkFake{})
	assert.Equal(t, LogsExportComplete, sum.Status)
}

func TestLogsExport_Estimate(t *testing.T) {
	t.Parallel()
	f := newExportFixture(7, nil)
	st, err := f.uc.Estimate(context.Background(), "n1", "t1", port.LogQuery{Unresolved: true, Status: "ok"})
	require.NoError(t, err)
	assert.EqualValues(t, 7, st.Records)
	assert.EqualValues(t, 200, f.reader.statsCap, "кап тела = символы ячейки × 2 байта")
	assert.Equal(t, "db.logs", f.reader.statsQuery.Table)
	assert.False(t, f.reader.statsQuery.Unresolved)
	assert.Empty(t, f.limiter.keys, "оценка частоту не расходует")

	_, err = f.uc.Estimate(context.Background(), "n1", "t1", port.LogQuery{Q: "(", QRegex: true})
	require.ErrorIs(t, err, logsearch.ErrBadQuery)
}

func TestExportAuditDetails(t *testing.T) {
	t.Parallel()
	meta := LogsExportMeta{
		Query: port.LogQuery{
			Q: strings.Repeat("я", 250), QRegex: true, Status: "err", Method: "v1/x", SinceMs: 1, UntilMs: 2,
		},
		Bodies: true, Selected: 10,
	}
	d := exportAuditDetails(meta, LogsExportSummary{Status: LogsExportLimitedBytes, Written: 9, Bytes: 100})

	assert.Equal(t, "limited_bytes", d["result"])
	assert.Equal(t, 9, d["rows"])
	assert.Equal(t, int64(100), d["bytes"])
	assert.Equal(t, true, d["bodies"])
	assert.Equal(t, "err", d["status"])
	assert.Equal(t, "v1/x", d["method"])
	assert.Equal(t, true, d["q_regex"])
	assert.Equal(t, strings.Repeat("я", 200), d["q"], "поисковая строка урезана по символам")
	assert.NotContains(t, d, "ip", "пустые фильтры не пишутся")
	assert.NotContains(t, d, "reason")
}
