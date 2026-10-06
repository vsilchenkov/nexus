package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// Ревизия §113: сигнал прохода очереди паузы — один на операцию и только у
// узла на паузе. До правки его двигала каждая пачка отметок отмены: «Повторить
// все» на N записей давал N сигналов, очистка постороннего включённого узла —
// внеочередной раунд, и каждый сигнал сбрасывал цель раунда.

// countingSignal считает Bump.
type countingSignal struct {
	bumps int
	err   error
}

func (s *countingSignal) Bump(context.Context) (int64, error) {
	s.bumps++
	return int64(s.bumps), s.err
}

func newSignalQueueUC(node *domain.Node, peeker port.AsyncQueuePeeker, failed port.FailedLogsPurger, sig *countingSignal, peekCap int) *AsyncQueueUsecase {
	nodes := &stubNodeRepo{nodes: map[string]*domain.Node{node.ID: node}}
	return NewAsyncQueueUsecase(peeker, &stubCancelWriter{}, failed, nodes, nil,
		NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()),
		"nexus-sender", "nexus.async", "nexus-sender-paused", "nexus.async.paused",
		time.Hour, peekCap, logging.NewNoop(), WithQueueSweepSignal(sig))
}

func pausedAsyncNodeCH() *domain.Node {
	n := asyncNodeCH()
	n.Status = domain.NodeStatusPaused
	return n
}

func TestPausedSweepSignal_PurgeFailed_OncePerOperation(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		node *domain.Node
		want int
	}{
		{name: "sync-узел на паузе: один сигнал на три батча", node: pausedSyncNodeCH(), want: 1},
		{name: "включённый sync-узел: сигнала нет", node: syncNodeCH(), want: 0},
		{name: "включённый async-узел: сигнала нет", node: asyncNodeCH(), want: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sig := &countingSignal{}
			failed := &batchFailedPurger{remaining: []string{"a", "b", "c", "d", "e"}}
			uc := newSignalQueueUC(tt.node, &stubPeeker{}, failed, sig, 2)

			_, err := uc.PurgeFailed(context.Background(), Actor{UserID: "u"}, "n1", "t1", time.Time{}, time.Time{})
			require.NoError(t, err)
			require.Equal(t, 3, failed.delCalls, "очистка прошла тремя батчами")
			assert.Equal(t, tt.want, sig.bumps)
		})
	}
}

func TestPausedSweepSignal_PendingOps(t *testing.T) {
	t.Parallel()
	peeker := &stubPeeker{scan: port.ScanIDsResult{IDs: []string{"m1", "m2"}}}

	sig := &countingSignal{}
	uc := newSignalQueueUC(pausedAsyncNodeCH(), peeker, nil, sig, 100)
	require.NoError(t, uc.DeleteOne(context.Background(), Actor{UserID: "u"}, "n1", "t1", "m1"))
	_, err := uc.PurgePeriod(context.Background(), Actor{UserID: "u"}, "n1", "t1", time.Time{}, time.Time{})
	require.NoError(t, err)
	assert.Equal(t, 2, sig.bumps, "удаление и очистка — по сигналу на операцию")

	enabled := &countingSignal{}
	uc = newSignalQueueUC(asyncNodeCH(), peeker, nil, enabled, 100)
	require.NoError(t, uc.DeleteOne(context.Background(), Actor{UserID: "u"}, "n1", "t1", "m1"))
	assert.Zero(t, enabled.bumps, "сообщения включённого узла в очереди паузы не лежат")
}

// Сигнал best-effort: отметки уже записаны, операция успешна, а отменённое
// снимет страховочный проход.
func TestPausedSweepSignal_ErrorDoesNotFailOperation(t *testing.T) {
	t.Parallel()
	sig := &countingSignal{err: errors.New("redis down")}
	uc := newSignalQueueUC(pausedAsyncNodeCH(), &stubPeeker{}, nil, sig, 100)
	require.NoError(t, uc.DeleteOne(context.Background(), Actor{UserID: "u"}, "n1", "t1", "m1"))
	assert.Equal(t, 1, sig.bumps)
}

func TestPausedSweepSignal_ReplayFailed_OncePerOperation(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		status domain.NodeStatus
		want   int
	}{
		{name: "узел на паузе: один сигнал на три отмены", status: domain.NodeStatusPaused, want: 1},
		{name: "включённый узел: сигнала нет", status: domain.NodeStatusEnabled, want: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			node := &domain.Node{ID: "n1", Path: "demo/sync", Status: tt.status,
				ClickHouseTable: "test.sync", RootMethod: domain.RootMethodRequest}
			log := &domain.LogRecord{ID: "x", Method: "POST", Type: domain.RootMethodRequestAsync,
				Request: `{"a":1}`, DateRequest: time.Now()}
			sig := &countingSignal{}
			cancelW := &stubCancelWriter{}
			uc := NewReplayUsecaseWithCancel(
				&stubLogReader{log: log, failedIDs: []string{"f1", "f2", "f3"}},
				&stubNodeRepo{nodes: map[string]*domain.Node{node.ID: node}},
				&stubDispatcher{resp: &port.DispatchResponse{StatusCode: 202}},
				nil, NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()), 10,
				cancelW, nil, time.Hour, logging.NewNoop(),
				WithReplaySweepSignal(sig),
			)

			res, err := uc.ReplayFailed(context.Background(), SystemActor(), "n1", "", time.Time{}, time.Time{})
			require.NoError(t, err)
			require.Equal(t, 3, res.Replayed)
			require.Len(t, cancelW.gotIDs, 3, "оригиналы отменяются по одному")
			assert.Equal(t, tt.want, sig.bumps)
		})
	}
}
