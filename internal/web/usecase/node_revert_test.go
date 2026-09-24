package usecase

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
)

var revertTestUpdatedAt = time.Date(2026, 9, 24, 10, 0, 0, 123456000, time.UTC)

// revertTestNode — валидный sync-узел с входящим POST.
func revertTestNode() *domain.Node {
	return &domain.Node{
		ID: "n1", Path: "svc/x", TeamID: "t1", RootMethod: domain.RootMethodRequest,
		TargetURL: "https://b.example", TimeoutMs: 2000, Status: domain.NodeStatusEnabled,
		AuthType: domain.AuthTypeNone, IncomingAuthType: domain.IncomingAuthTypeNone,
		IncomingMethod: domain.HTTPMethodPOST, LoggingEnabled: true, ClickHouseTable: "nexus.svc_x",
		UpdatedAt: revertTestUpdatedAt,
	}
}

func newRevertNodeUC(t *testing.T, n *domain.Node) (*NodeUsecase, *memNodeRepo, *stubAuditRepo) {
	t.Helper()
	repo := newMemNodeRepo()
	if n != nil {
		repo.items[n.ID] = n
	}
	audit := &stubAuditRepo{}
	uc := NewNodeUsecase(repo, nopNodeCache{}, NewAuditUsecase(audit, logging.NewNoop()),
		nil, nil, nil, nil, time.Minute, 0, "default-team", nil, logging.NewNoop())
	return uc, repo, audit
}

func revertOf(values map[string]any) NodeRevert {
	return NodeRevert{
		NodeID: "n1", Values: values, ExpectedUpdatedAt: revertTestUpdatedAt,
		SourceEntryID: "e-1", SourceEntryAt: time.Date(2026, 9, 23, 18, 36, 22, 0, time.UTC),
	}
}

// TestNodeRevert_RestoresFieldsAndWritesRevert — полный путь: поля вернулись,
// в журнал ушла node.revert с diff и ссылкой на отменённую строку, в команду
// УЗЛА, а не актора (§108.6).
func TestNodeRevert_RestoresFieldsAndWritesRevert(t *testing.T) {
	t.Parallel()
	uc, repo, audit := newRevertNodeUC(t, revertTestNode())
	actor := Actor{UserLogin: "admin", TeamID: "other-team"}

	require.NoError(t, uc.Revert(context.Background(), actor,
		revertOf(map[string]any{"target_url": "https://a.example", "timeout_ms": 1000.0})))

	got := repo.items["n1"]
	assert.Equal(t, "https://a.example", got.TargetURL)
	assert.Equal(t, int32(1000), got.TimeoutMs)
	assert.Equal(t, "admin", got.UpdatedBy)

	require.Len(t, audit.entries, 1)
	e := audit.entries[0]
	assert.Equal(t, domain.ActionNodeRevert, e.Action)
	assert.Equal(t, "node", e.TargetType)
	assert.Equal(t, "n1", e.TargetID)
	assert.Equal(t, "t1", e.TeamID, "строка отмены — в команде узла")
	assert.Equal(t, beforeAfter("https://b.example", "https://a.example"), e.Details["target_url"])
	assert.Equal(t, beforeAfter(int32(2000), int32(1000)), e.Details["timeout_ms"])
	assert.Equal(t, "e-1", e.Details["reverted_from"])
	assert.Equal(t, "2026-09-23T18:36:22Z", e.Details["reverted_from_at"])
}

// TestNodeRevert_StatusOnlyLightPath — выбран один статус: лёгкий путь без
// валидации. Узел, не проходящий сегодняшние правила (адрес без схемы), всё
// равно снимается с паузы — как кнопкой.
func TestNodeRevert_StatusOnlyLightPath(t *testing.T) {
	t.Parallel()
	n := revertTestNode()
	n.Status = domain.NodeStatusPaused
	n.TargetURL = "not-a-url"
	require.Error(t, n.Validate(), "предусловие: узел не проходит валидацию")
	uc, repo, audit := newRevertNodeUC(t, n)

	require.NoError(t, uc.Revert(context.Background(), Actor{UserLogin: "admin"},
		revertOf(map[string]any{"status": "enabled"})))

	assert.Equal(t, domain.NodeStatusEnabled, repo.items["n1"].Status)
	require.Len(t, audit.entries, 1)
	assert.Equal(t, domain.ActionNodeRevert, audit.entries[0].Action)
	assert.Equal(t, map[string]string{"before": "paused", "after": "enabled"}, audit.entries[0].Details["status"])
	assert.Equal(t, "e-1", audit.entries[0].Details["reverted_from"])
}

// TestNodeRevert_Rejects — отказы без записи в узел и журнал.
func TestNodeRevert_Rejects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(n *domain.Node)
		revert  func(r *NodeRevert)
		wantErr error
		notHeld []string
	}{
		{"node changed since preview", nil,
			func(r *NodeRevert) { r.ExpectedUpdatedAt = revertTestUpdatedAt.Add(time.Microsecond) },
			domain.ErrAuditRevertConflict, nil},
		{"node missing", nil, func(r *NodeRevert) { r.NodeID = "gone" }, domain.ErrNodeNotFound, nil},
		{"status already equal", nil,
			func(r *NodeRevert) { r.Values = map[string]any{"status": "enabled"} },
			domain.ErrAuditRevertFieldInvalid, nil},
		{"status invalid", nil,
			func(r *NodeRevert) { r.Values = map[string]any{"status": "bogus"} },
			domain.ErrAuditRevertFieldInvalid, nil},
		{"unknown field", nil,
			func(r *NodeRevert) { r.Values = map[string]any{"comment": "x"} },
			domain.ErrAuditRevertFieldInvalid, nil},
		{"pull field on request node", nil,
			func(r *NodeRevert) { r.Values = map[string]any{"rmq_host": "h"} },
			domain.ErrAuditRevertFieldInvalid, nil},
		{"sse_decline on POST node is normalized away", nil,
			func(r *NodeRevert) { r.Values = map[string]any{"sse_decline": true} },
			domain.ErrAuditRevertNotHeld, []string{"sse_decline"}},
		{"zero timeout replaced by default",
			nil,
			func(r *NodeRevert) { r.Values = map[string]any{"timeout_ms": 0.0} },
			domain.ErrAuditRevertNotHeld, []string{"timeout_ms"}},
		{"zero rmq_port on pull node replaced by default",
			func(n *domain.Node) {
				n.RootMethod = domain.RootMethodRabbitMQAsync
				n.RMQHost, n.RMQQueue, n.RMQPort, n.RMQVHost = "rmq", "q", 5672, "/"
				n.PullIntervalSec, n.PullBatchSize, n.PullPrefetch = 5, 100, 100
			},
			func(r *NodeRevert) { r.Values = map[string]any{"rmq_port": 0.0} },
			domain.ErrAuditRevertNotHeld, []string{"rmq_port"}},
		{"webhook signature without secret fails validation", nil,
			func(r *NodeRevert) {
				r.Values = map[string]any{"incoming_auth_type": "webhook_signature", "webhook_signature_header": "X-Sig"}
			},
			domain.ErrNodeWebhookSigSecretRequired, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			n := revertTestNode()
			if tt.mutate != nil {
				tt.mutate(n)
			}
			before := *n
			uc, repo, audit := newRevertNodeUC(t, n)
			r := revertOf(map[string]any{"target_url": "https://a.example"})
			tt.revert(&r)

			err := uc.Revert(context.Background(), Actor{UserLogin: "admin"}, r)
			require.ErrorIs(t, err, tt.wantErr)
			if tt.notHeld != nil {
				var nh *RevertNotHeldError
				require.ErrorAs(t, err, &nh)
				assert.Equal(t, tt.notHeld, nh.Fields)
			}
			assert.Equal(t, before, *repo.items["n1"], "узел не должен меняться")
			assert.Empty(t, audit.entries, "журнал не должен пополняться")
		})
	}
}

// TestNodeRevert_PreviewDoesNotMutate — предпросмотр идёт по копии: исходный
// узел (включая *AsyncAck, который SetDefaults правит на месте) не меняется.
func TestNodeRevert_PreviewDoesNotMutate(t *testing.T) {
	t.Parallel()
	n := revertTestNode()
	n.AsyncAck = auditSpec(`{"ok":true}`)
	n.AsyncAck.ContentType = "" // SetDefaults заполнил бы его на месте
	n.ForwardHeaders = []string{"X-A"}
	uc, _, audit := newRevertNodeUC(t, n)
	snapshot := *n
	ackSnapshot := *n.AsyncAck

	require.NoError(t, uc.PreviewRevert(context.Background(), n, map[string]any{"target_url": "https://a.example"}))
	assert.Equal(t, snapshot, *n)
	assert.Equal(t, ackSnapshot, *n.AsyncAck)
	assert.Empty(t, audit.entries)

	err := uc.PreviewRevert(context.Background(), n, map[string]any{"sse_decline": true})
	assert.ErrorIs(t, err, domain.ErrAuditRevertNotHeld)
	assert.NoError(t, uc.PreviewRevert(context.Background(), n, nil), "пустой выбор — нечего проверять")
	assert.ErrorIs(t, uc.PreviewRevert(context.Background(), n, map[string]any{"status": 1.0}),
		domain.ErrAuditRevertFieldInvalid)
}
