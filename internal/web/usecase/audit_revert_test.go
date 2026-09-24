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

const (
	revertEntryID = "6a1f0c2e-0000-4000-8000-000000000001"
	revertNodeID  = "6a1f0c2e-0000-4000-8000-0000000000aa"
)

// fakeBatchReader — port.NodeBatchReader с записью вызовов.
type fakeBatchReader struct {
	nodes map[string]*domain.Node
	err   error
	calls [][]string
}

func (f *fakeBatchReader) GetByIDs(_ context.Context, ids []string) (map[string]*domain.Node, error) {
	f.calls = append(f.calls, ids)
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]*domain.Node{}
	for _, id := range ids {
		if n, ok := f.nodes[id]; ok {
			cp := *n
			out[id] = &cp
		}
	}
	return out, nil
}

// countingAuditReader — читатель журнала, игнорирующий фильтр (как
// stubAuditRepo), со счётчиком вызовов.
type countingAuditReader struct {
	entries []*domain.AuditEntry
	calls   int
}

func (r *countingAuditReader) List(_ context.Context, _ port.AuditFilter) ([]*domain.AuditEntry, error) {
	r.calls++
	return r.entries, nil
}

// fakeReverter — nodeReverter с записью вызовов.
type fakeReverter struct {
	reverts    []NodeRevert
	previews   []map[string]any
	previewErr error
}

func (f *fakeReverter) Revert(_ context.Context, _ Actor, r NodeRevert) error {
	f.reverts = append(f.reverts, r)
	return nil
}

func (f *fakeReverter) PreviewRevert(_ context.Context, _ *domain.Node, values map[string]any) error {
	f.previews = append(f.previews, values)
	return f.previewErr
}

func revertAuditNode() *domain.Node {
	n := revertTestNode()
	n.ID = revertNodeID
	n.TargetURL = "https://c.example" // перебито позже записи
	return n
}

func revertAuditEntry(mut func(e *domain.AuditEntry)) *domain.AuditEntry {
	e := &domain.AuditEntry{
		ID: revertEntryID, UserLogin: "bob", TeamID: "t1",
		Action: domain.ActionNodeUpdate, TargetType: "node", TargetID: revertNodeID,
		CreatedAt: time.Date(2026, 9, 23, 18, 36, 22, 0, time.UTC),
		Details: map[string]any{
			"target_url": beforeAfter("https://a.example", "https://b.example"), // overwritten
			"timeout_ms": beforeAfter(1000.0, 2000.0),                           // actual
		},
	}
	if mut != nil {
		mut(e)
	}
	return e
}

type revertFixture struct {
	uc       *AuditRevertUsecase
	entries  *countingAuditReader
	nodes    *fakeBatchReader
	reverter *fakeReverter
}

func newRevertFixture(entries []*domain.AuditEntry, nodes ...*domain.Node) revertFixture {
	f := revertFixture{
		entries:  &countingAuditReader{entries: entries},
		nodes:    &fakeBatchReader{nodes: map[string]*domain.Node{}},
		reverter: &fakeReverter{},
	}
	for _, n := range nodes {
		f.nodes.nodes[n.ID] = n
	}
	f.uc = NewAuditRevertUsecase(f.entries, f.nodes, f.reverter, logging.NewNoop())
	return f
}

// TestAuditRevertPreview — предпросмотр строки журнала (§108.7).
func TestAuditRevertPreview(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("fields, state and default dry-run selection", func(t *testing.T) {
		t.Parallel()
		f := newRevertFixture([]*domain.AuditEntry{revertAuditEntry(nil)}, revertAuditNode())
		p, err := f.uc.Preview(ctx, revertEntryID)
		require.NoError(t, err)
		assert.Equal(t, RevertStateStale, p.State)
		assert.False(t, p.Moved)
		require.NotNil(t, p.Node)
		assert.Equal(t, RevertFieldOverwritten, fieldByName(t, p.Fields, "target_url").Status)
		assert.Equal(t, RevertFieldActual, fieldByName(t, p.Fields, "timeout_ms").Status)
		require.Len(t, f.reverter.previews, 1)
		assert.Equal(t, map[string]any{"timeout_ms": 1000.0}, f.reverter.previews[0],
			"по умолчанию — только поля, совпадающие со «Стало»")
	})

	t.Run("moved to another team", func(t *testing.T) {
		t.Parallel()
		f := newRevertFixture([]*domain.AuditEntry{revertAuditEntry(func(e *domain.AuditEntry) { e.TeamID = "t0" })}, revertAuditNode())
		p, err := f.uc.Preview(ctx, revertEntryID)
		require.NoError(t, err)
		assert.True(t, p.Moved)
	})

	t.Run("dry-run error surfaced", func(t *testing.T) {
		t.Parallel()
		f := newRevertFixture([]*domain.AuditEntry{revertAuditEntry(nil)}, revertAuditNode())
		f.reverter.previewErr = &RevertNotHeldError{Fields: []string{"timeout_ms"}}
		p, err := f.uc.Preview(ctx, revertEntryID)
		require.NoError(t, err)
		assert.ErrorIs(t, p.DryRunErr, domain.ErrAuditRevertNotHeld)
	})

	t.Run("node deleted", func(t *testing.T) {
		t.Parallel()
		f := newRevertFixture([]*domain.AuditEntry{revertAuditEntry(nil)})
		p, err := f.uc.Preview(ctx, revertEntryID)
		require.NoError(t, err)
		assert.Nil(t, p.Node)
		assert.Equal(t, RevertStateGone, p.State)
		assert.Empty(t, f.reverter.previews, "удалённый узел не прогоняется")
		for _, fld := range p.Fields {
			assert.False(t, fld.Revertible(), fld.Name)
		}
	})

	errCases := []struct {
		name      string
		entryID   string
		entries   []*domain.AuditEntry
		wantErr   error
		wantCalls int
	}{
		{"not a uuid", "zzz", []*domain.AuditEntry{revertAuditEntry(nil)}, domain.ErrAuditEntryNotFound, 0},
		{"reader ignores filter and returns another entry", revertEntryID,
			[]*domain.AuditEntry{revertAuditEntry(func(e *domain.AuditEntry) { e.ID = "6a1f0c2e-0000-4000-8000-000000000002" })},
			domain.ErrAuditEntryNotFound, 1},
		{"not found", revertEntryID, nil, domain.ErrAuditEntryNotFound, 1},
		{"node.create is not revertible", revertEntryID,
			[]*domain.AuditEntry{revertAuditEntry(func(e *domain.AuditEntry) { e.Action = domain.ActionNodeCreate })},
			domain.ErrAuditNotRevertible, 1},
		{"target is not a node", revertEntryID,
			[]*domain.AuditEntry{revertAuditEntry(func(e *domain.AuditEntry) { e.TargetType = "user" })},
			domain.ErrAuditNotRevertible, 1},
	}
	for _, tt := range errCases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newRevertFixture(tt.entries, revertAuditNode())
			_, err := f.uc.Preview(ctx, tt.entryID)
			assert.ErrorIs(t, err, tt.wantErr)
			assert.Equal(t, tt.wantCalls, f.entries.calls)
		})
	}

	t.Run("uppercase uuid is normalized", func(t *testing.T) {
		t.Parallel()
		f := newRevertFixture([]*domain.AuditEntry{revertAuditEntry(nil)}, revertAuditNode())
		_, err := f.uc.Preview(ctx, "6A1F0C2E-0000-4000-8000-000000000001")
		assert.NoError(t, err)
	})
}

// TestAuditRevertApply — применение выбранных полей (§108.7).
func TestAuditRevertApply(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	actor := Actor{UserLogin: "admin"}

	t.Run("passes before values and source entry", func(t *testing.T) {
		t.Parallel()
		f := newRevertFixture([]*domain.AuditEntry{revertAuditEntry(nil)}, revertAuditNode())
		require.NoError(t, f.uc.Apply(ctx, actor, revertEntryID, []string{"target_url", "timeout_ms"}, revertTestUpdatedAt))
		require.Len(t, f.reverter.reverts, 1)
		r := f.reverter.reverts[0]
		assert.Equal(t, revertNodeID, r.NodeID)
		assert.Equal(t, map[string]any{"target_url": "https://a.example", "timeout_ms": 1000.0}, r.Values)
		assert.Equal(t, revertEntryID, r.SourceEntryID)
		assert.True(t, r.SourceEntryAt.Equal(time.Date(2026, 9, 23, 18, 36, 22, 0, time.UTC)))
		assert.True(t, r.ExpectedUpdatedAt.Equal(revertTestUpdatedAt))
	})

	t.Run("node.revert entry can be reverted (redo)", func(t *testing.T) {
		t.Parallel()
		e := revertAuditEntry(func(e *domain.AuditEntry) {
			e.Action = domain.ActionNodeRevert
			e.Details = map[string]any{
				"timeout_ms":       beforeAfter(1000.0, 2000.0),
				"reverted_from":    "6a1f0c2e-0000-4000-8000-000000000009",
				"reverted_from_at": "2026-09-22T10:00:00Z",
			}
		})
		f := newRevertFixture([]*domain.AuditEntry{e}, revertAuditNode())
		require.NoError(t, f.uc.Apply(ctx, actor, revertEntryID, []string{"timeout_ms"}, revertTestUpdatedAt))
		require.Len(t, f.reverter.reverts, 1)
	})

	errCases := []struct {
		name     string
		fields   []string
		expected time.Time
		noNode   bool
		wantErr  error
	}{
		{"node changed since preview", []string{"timeout_ms"}, revertTestUpdatedAt.Add(time.Second), false, domain.ErrAuditRevertConflict},
		{"node deleted", []string{"timeout_ms"}, revertTestUpdatedAt, true, domain.ErrNodeNotFound},
		{"no fields", nil, revertTestUpdatedAt, false, domain.ErrAuditRevertFieldInvalid},
		{"field not in entry", []string{"status"}, revertTestUpdatedAt, false, domain.ErrAuditRevertFieldInvalid},
	}
	for _, tt := range errCases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var nodes []*domain.Node
			if !tt.noNode {
				nodes = append(nodes, revertAuditNode())
			}
			f := newRevertFixture([]*domain.AuditEntry{revertAuditEntry(nil)}, nodes...)
			err := f.uc.Apply(ctx, actor, revertEntryID, tt.fields, tt.expected)
			assert.ErrorIs(t, err, tt.wantErr)
			assert.Empty(t, f.reverter.reverts)
		})
	}
}

// TestAuditRevertAnnotate — разметка страницы выдачи одним запросом узлов,
// fail-open при ошибке (§108.7).
func TestAuditRevertAnnotate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	goneID := "6a1f0c2e-0000-4000-8000-0000000000bb"
	actualNode := revertAuditNode()
	actualNode.TargetURL = "https://b.example"
	entry := func(id, action, target string, details map[string]any) *domain.AuditEntry {
		return &domain.AuditEntry{ID: id, Action: action, TargetType: "node", TargetID: target, Details: details}
	}
	page := []*domain.AuditEntry{
		entry("e1", domain.ActionNodeUpdate, revertNodeID, map[string]any{"target_url": beforeAfter("https://a.example", "https://b.example")}),
		entry("e2", domain.ActionNodeUpdate, revertNodeID, map[string]any{"target_url": beforeAfter("https://x.example", "https://y.example")}),
		entry("e3", domain.ActionNodeUpdate, goneID, map[string]any{"status": beforeAfter("paused", "enabled")}),
		entry("e4", domain.ActionNodeCreate, revertNodeID, map[string]any{"path": "svc/x"}),
		entry("e5", domain.ActionNodeUpdate, revertNodeID, map[string]any{"auth_credentials": "changed"}),
		entry("e6", domain.ActionNodeUpdate, "not-a-uuid", map[string]any{"status": beforeAfter("paused", "enabled")}),
		{ID: "e7", Action: domain.ActionUserLogin, TargetType: "user", TargetID: revertNodeID},
	}

	t.Run("marks page with one lookup", func(t *testing.T) {
		t.Parallel()
		f := newRevertFixture(nil, actualNode)
		marks := f.uc.Annotate(ctx, page)

		require.Len(t, f.nodes.calls, 1, "один запрос узлов на страницу")
		assert.ElementsMatch(t, []string{revertNodeID, goneID}, f.nodes.calls[0], "без дублей и невалидных id")

		assert.Equal(t, RevertMark{State: RevertStateActual, Revertible: true}, marks["e1"])
		assert.Equal(t, RevertMark{State: RevertStateStale, Revertible: true,
			Overwritten: map[string]any{"target_url": "https://b.example"}}, marks["e2"])
		assert.Equal(t, RevertStateGone, marks["e3"].State)
		assert.Equal(t, RevertStateGone, marks["e6"].State, "невалидный id узла — узла нет")
		for _, id := range []string{"e4", "e5", "e7"} {
			_, ok := marks[id]
			assert.False(t, ok, "%s не размечается", id)
		}
	})

	t.Run("lookup error fails open", func(t *testing.T) {
		t.Parallel()
		f := newRevertFixture(nil, actualNode)
		f.nodes.err = errors.New("pg down")
		assert.Empty(t, f.uc.Annotate(ctx, page))
	})

	t.Run("nothing revertible — no lookup", func(t *testing.T) {
		t.Parallel()
		f := newRevertFixture(nil, actualNode)
		assert.Empty(t, f.uc.Annotate(ctx, []*domain.AuditEntry{page[3], page[6]}))
		assert.Empty(t, f.nodes.calls)
	})
}

// TestAuditRevert_WithNodeUsecase — сквозь настоящий NodeUsecase: предпросмотр
// показывает несохраняемое значение, применение возвращает поле и пишет
// node.revert.
func TestAuditRevert_WithNodeUsecase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	n := revertAuditNode()
	nodeUC, repo, audit := newRevertNodeUC(t, n)
	e := revertAuditEntry(func(e *domain.AuditEntry) {
		e.Details = map[string]any{
			"target_url":  beforeAfter("https://a.example", "https://c.example"),
			"sse_decline": beforeAfter(true, false),
		}
	})
	uc := NewAuditRevertUsecase(&countingAuditReader{entries: []*domain.AuditEntry{e}},
		&fakeBatchReader{nodes: map[string]*domain.Node{n.ID: n}}, nodeUC, logging.NewNoop())

	p, err := uc.Preview(ctx, revertEntryID)
	require.NoError(t, err)
	var nh *RevertNotHeldError
	require.ErrorAs(t, p.DryRunErr, &nh, "sse_decline у узла с входящим POST не удержится")
	assert.Equal(t, []string{"sse_decline"}, nh.Fields)

	require.NoError(t, uc.Apply(ctx, Actor{UserLogin: "admin"}, revertEntryID, []string{"target_url"}, n.UpdatedAt))
	assert.Equal(t, "https://a.example", repo.items[revertNodeID].TargetURL)
	require.Len(t, audit.entries, 1)
	assert.Equal(t, domain.ActionNodeRevert, audit.entries[0].Action)
	assert.Equal(t, revertEntryID, audit.entries[0].Details["reverted_from"])
}
