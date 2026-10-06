package usecase

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// newBulkReplayUC — «Повторить все» над узлом с тремя неудачными записями,
// каждая из которых — копия log.
func newBulkReplayUC(node *domain.Node, log *domain.LogRecord, disp *stubDispatcher) (*ReplayUsecase, *stubCancelWriter, *stubFailedCleaner) {
	cancelW := &stubCancelWriter{}
	cleaner := &stubFailedCleaner{deleted: 3}
	uc := NewReplayUsecaseWithCancel(
		&stubLogReader{log: log, failedIDs: []string{"f1", "f2", "f3"}},
		&stubNodeRepo{nodes: map[string]*domain.Node{node.ID: node}},
		disp, nil, NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()), 10,
		cancelW, nil, time.Hour, logging.NewNoop(),
		WithFailedCleaner(cleaner),
	)
	return uc, cancelW, cleaner
}

// TestReplay_ReplayFailed_RejectedKeepsRecords (§113.10): ответ Receiver'а не
// 2xx — неудача повтора: оригинал не отменяется, запись не стирается. До §113
// 404 считался успехом, и записи sync-узла пропадали молча.
func TestReplay_ReplayFailed_RejectedKeepsRecords(t *testing.T) {
	t.Parallel()
	for _, status := range []int{404, 409, 500, 302} {
		node := &domain.Node{ID: "n1", Path: "demo/async", Status: domain.NodeStatusEnabled,
			ClickHouseTable: "test.async", RootMethod: domain.RootMethodRequestAsync}
		log := &domain.LogRecord{ID: "x", Method: "POST", Type: domain.RootMethodRequestAsync,
			Request: `{"a":1}`, DateRequest: time.Now()}
		disp := &stubDispatcher{resp: &port.DispatchResponse{StatusCode: status, Body: []byte(`{"error":"node not found"}`)}}
		uc, cancelW, cleaner := newBulkReplayUC(node, log, disp)

		res, err := uc.ReplayFailed(context.Background(), SystemActor(), "n1", "", time.Time{}, time.Time{})
		require.NoError(t, err)
		assert.Equal(t, 3, res.Failed, "status %d", status)
		assert.Zero(t, res.Replayed, "status %d", status)
		assert.Empty(t, cancelW.gotIDs, "status %d: оригиналы не отменяются", status)
		assert.Zero(t, cleaner.calls, "status %d: записи не стираются", status)
		assert.Zero(t, res.Cleaned, "status %d", status)
	}
}

// TestReplay_ReplayFailed_202Accepted: 202 (узел на паузе поставил повтор в
// очередь) — успех: оригинал отменяется, запись убирается.
func TestReplay_ReplayFailed_202Accepted(t *testing.T) {
	t.Parallel()
	node := &domain.Node{ID: "n1", Path: "demo/sync", Status: domain.NodeStatusPaused,
		ClickHouseTable: "test.sync", RootMethod: domain.RootMethodRequest}
	log := &domain.LogRecord{ID: "x", Method: "POST", Type: domain.RootMethodRequestAsync,
		Request: `{"a":1}`, DateRequest: time.Now()}
	disp := &stubDispatcher{resp: &port.DispatchResponse{StatusCode: 202, Body: []byte(`{"status":"queued"}`)}}
	uc, cancelW, cleaner := newBulkReplayUC(node, log, disp)

	res, err := uc.ReplayFailed(context.Background(), SystemActor(), "n1", "", time.Time{}, time.Time{})
	require.NoError(t, err)
	assert.Equal(t, 3, res.Replayed)
	assert.Len(t, cancelW.gotIDs, 3)
	assert.Equal(t, 1, cleaner.calls)
	assert.False(t, disp.gotReq.Async, "async-запись sync-узла повторяется через /request")
}

// TestReplayViaAsync (§113.10): async-вход — только у узлов с очередью.
func TestReplayViaAsync(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		node     domain.RootMethod
		record   domain.RootMethod
		wantAsyn bool
	}{
		{name: "async-узел, async-запись", node: domain.RootMethodRequestAsync, record: domain.RootMethodRequestAsync, wantAsyn: true},
		// §82.3: внешний /requestAsync у pull-узла — 404; на паузе /request даёт 202.
		{name: "pull-узел, async-запись", node: domain.RootMethodRabbitMQAsync, record: domain.RootMethodRequestAsync},
		{name: "sync-узел, отложенная во время паузы запись", node: domain.RootMethodRequest, record: domain.RootMethodRequestAsync},
		{name: "sync-узел, sync-запись", node: domain.RootMethodRequest, record: domain.RootMethodRequest},
		{name: "async-узел, sync-запись (§3.6)", node: domain.RootMethodRequestAsync, record: domain.RootMethodRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := replayViaAsync(&domain.Node{RootMethod: tt.node}, &domain.LogRecord{Type: tt.record})
			assert.Equal(t, tt.wantAsyn, got)
		})
	}
}

// TestReplayPeriod_RejectedIsFailure (§113.10): «Повторить за период» тоже не
// считает не-2xx ответ повторённым.
func TestReplayPeriod_RejectedIsFailure(t *testing.T) {
	t.Parallel()
	at := time.Now().Add(-30 * time.Minute)
	reader := &periodReader{pages: []replayCandPage{{
		items: []domain.ReplayCandidate{okCandidate("a", at), okCandidate("b", at.Add(time.Second))},
	}}}
	disp := &stubDispatcher{resp: &port.DispatchResponse{StatusCode: 404}}
	uc, _ := newPeriodUC(t, reader, periodNode(), disp)

	res, err := uc.ReplayPeriod(context.Background(), SystemActor(), periodInput())
	require.NoError(t, err)
	assert.Equal(t, 2, res.Failed)
	assert.Zero(t, res.Replayed)
}
