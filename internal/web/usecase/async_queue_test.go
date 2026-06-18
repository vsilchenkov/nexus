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

// stubPeeker — управляемый AsyncQueuePeeker; фиксирует аргументы ScanIDs.
type stubPeeker struct {
	list     port.PeekListResult
	body     port.QueueMessageBody
	scan     port.ScanIDsResult
	err      error
	scanFrom time.Time
	scanTo   time.Time
	scanPath string
}

func (s *stubPeeker) PeekList(_ context.Context, _, _, _ string, _, _ int) (port.PeekListResult, error) {
	return s.list, s.err
}
func (s *stubPeeker) PeekBody(_ context.Context, _ string, _ int, _ int64) (port.QueueMessageBody, error) {
	return s.body, s.err
}
func (s *stubPeeker) ScanIDs(_ context.Context, _, _, nodePath string, from, to time.Time, _ int) (port.ScanIDsResult, error) {
	s.scanPath, s.scanFrom, s.scanTo = nodePath, from, to
	return s.scan, s.err
}

// stubCancelWriter — фиксирует переданные на отмену ID.
type stubCancelWriter struct {
	gotIDs []string
	gotTTL time.Duration
	err    error
}

func (s *stubCancelWriter) Cancel(_ context.Context, ids []string, ttl time.Duration) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	s.gotIDs = append(s.gotIDs, ids...)
	s.gotTTL = ttl
	return len(ids), nil
}

func newQueueUC(peeker port.AsyncQueuePeeker, cancel port.QueueCancelWriter, node *domain.Node) (*AsyncQueueUsecase, *stubAuditRepo) {
	repo := &stubAuditRepo{}
	nodes := &stubNodeRepo{nodes: map[string]*domain.Node{}}
	if node != nil {
		nodes.nodes[node.ID] = node
	}
	uc := NewAsyncQueueUsecase(peeker, cancel, nodes,
		NewAuditUsecase(repo, logging.NewNoop()),
		"nexus-sender", "nexus.async", time.Hour, 5000, logging.NewNoop())
	return uc, repo
}

func asyncNode() *domain.Node {
	return &domain.Node{ID: "n1", Path: "partner/echo", TeamID: "t1", Status: domain.NodeStatusEnabled, RootMethod: domain.RootMethodRequestAsync}
}

func TestAsyncQueue_List_Degraded(t *testing.T) {
	t.Parallel()
	// peeker == nil → деградация (kafka_available=false, пустой список).
	uc, _ := newQueueUC(nil, &stubCancelWriter{}, asyncNode())
	r, err := uc.List(context.Background(), "n1", "t1")
	require.NoError(t, err)
	assert.False(t, r.KafkaAvailable)
	assert.Empty(t, r.Items)
}

func TestAsyncQueue_List_Happy(t *testing.T) {
	t.Parallel()
	peeker := &stubPeeker{list: port.PeekListResult{Items: []port.QueueMessageMeta{{ID: "id-1"}}, Capped: false}}
	uc, _ := newQueueUC(peeker, &stubCancelWriter{}, asyncNode())
	r, err := uc.List(context.Background(), "n1", "t1")
	require.NoError(t, err)
	assert.True(t, r.KafkaAvailable)
	require.Len(t, r.Items, 1)
	assert.Equal(t, "id-1", r.Items[0].ID)
}

func TestAsyncQueue_TeamScope_NotFound(t *testing.T) {
	t.Parallel()
	uc, _ := newQueueUC(&stubPeeker{}, &stubCancelWriter{}, asyncNode())
	// Чужая команда → 404.
	_, err := uc.List(context.Background(), "n1", "other-team")
	assert.ErrorIs(t, err, domain.ErrNodeNotFound)
}

func TestAsyncQueue_PurgePeriod_ForwardsWindowAndIDs(t *testing.T) {
	t.Parallel()
	peeker := &stubPeeker{scan: port.ScanIDsResult{IDs: []string{"a", "b", "c"}, Capped: true}}
	cancelW := &stubCancelWriter{}
	uc, audit := newQueueUC(peeker, cancelW, asyncNode())

	from := time.Date(2026, 6, 18, 10, 0, 0, 0, time.UTC)
	to := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	r, err := uc.PurgePeriod(context.Background(), Actor{UserID: "u"}, "n1", "t1", from, to)
	require.NoError(t, err)
	assert.Equal(t, 3, r.Cancelled)
	assert.True(t, r.Capped)

	// Окно проброшено в ScanIDs.
	assert.Equal(t, "partner/echo", peeker.scanPath)
	assert.Equal(t, from, peeker.scanFrom)
	assert.Equal(t, to, peeker.scanTo)
	// IDs форвардятся в Cancel с retention-TTL.
	assert.Equal(t, []string{"a", "b", "c"}, cancelW.gotIDs)
	assert.Equal(t, time.Hour, cancelW.gotTTL)
	// Audit-запись purge.
	require.Len(t, audit.entries, 1)
	assert.Equal(t, domain.ActionAsyncQueuePurge, audit.entries[0].Action)
}

func TestAsyncQueue_PurgeAll_ZeroWindow(t *testing.T) {
	t.Parallel()
	peeker := &stubPeeker{scan: port.ScanIDsResult{IDs: []string{"x"}}}
	cancelW := &stubCancelWriter{}
	uc, _ := newQueueUC(peeker, cancelW, asyncNode())

	_, err := uc.PurgeAll(context.Background(), Actor{UserID: "u"}, "n1", "t1")
	require.NoError(t, err)
	assert.True(t, peeker.scanFrom.IsZero(), "purge all → нулевая нижняя граница")
	assert.True(t, peeker.scanTo.IsZero(), "purge all → нулевая верхняя граница")
	assert.Equal(t, []string{"x"}, cancelW.gotIDs)
}

func TestAsyncQueue_DeleteOne(t *testing.T) {
	t.Parallel()
	cancelW := &stubCancelWriter{}
	uc, audit := newQueueUC(&stubPeeker{}, cancelW, asyncNode())

	err := uc.DeleteOne(context.Background(), Actor{UserID: "u"}, "n1", "t1", "msg-7")
	require.NoError(t, err)
	assert.Equal(t, []string{"msg-7"}, cancelW.gotIDs)
	require.Len(t, audit.entries, 1)
	assert.Equal(t, domain.ActionAsyncQueuePurge, audit.entries[0].Action)
}

func TestAsyncQueue_Mutations_Unavailable(t *testing.T) {
	t.Parallel()
	// cancel == nil → DeleteOne недоступен.
	uc, _ := newQueueUC(&stubPeeker{}, nil, asyncNode())
	err := uc.DeleteOne(context.Background(), Actor{UserID: "u"}, "n1", "t1", "m")
	assert.ErrorIs(t, err, ErrAsyncQueueUnavailable)

	// peeker == nil → Body недоступен.
	uc2, _ := newQueueUC(nil, &stubCancelWriter{}, asyncNode())
	_, err = uc2.Body(context.Background(), "n1", "t1", 0, 0)
	assert.ErrorIs(t, err, ErrAsyncQueueUnavailable)
}

func TestAsyncQueue_PurgeCancelError(t *testing.T) {
	t.Parallel()
	peeker := &stubPeeker{scan: port.ScanIDsResult{IDs: []string{"a"}}}
	cancelW := &stubCancelWriter{err: errors.New("redis down")}
	uc, audit := newQueueUC(peeker, cancelW, asyncNode())

	_, err := uc.PurgeAll(context.Background(), Actor{UserID: "u"}, "n1", "t1")
	require.Error(t, err)
	assert.Empty(t, audit.entries, "при ошибке cancel audit не пишется")
}
