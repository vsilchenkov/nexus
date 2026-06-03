package usecase

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
)

// memHostRepo — in-memory port.HostAllowlistRepo для unit-тестов.
type memHostRepo struct {
	items map[string]*domain.HostAllowlistEntry
	links map[string]map[string]bool // nodeID -> set(hostID)
	seq   int
}

func newMemHostRepo() *memHostRepo {
	return &memHostRepo{items: map[string]*domain.HostAllowlistEntry{}, links: map[string]map[string]bool{}}
}

func (r *memHostRepo) Get(_ context.Context, id string) (*domain.HostAllowlistEntry, error) {
	if e, ok := r.items[id]; ok {
		cp := *e
		return &cp, nil
	}
	return nil, domain.ErrHostNotFound
}

func (r *memHostRepo) GetByPattern(_ context.Context, pattern string) (*domain.HostAllowlistEntry, error) {
	for _, e := range r.items {
		if strings.EqualFold(e.Pattern, pattern) {
			cp := *e
			return &cp, nil
		}
	}
	return nil, domain.ErrHostNotFound
}

func (r *memHostRepo) Search(_ context.Context, _ string, _ domain.HostKind, _ int) ([]*domain.HostAllowlistEntry, error) {
	out := make([]*domain.HostAllowlistEntry, 0, len(r.items))
	for _, e := range r.items {
		cp := *e
		out = append(out, &cp)
	}
	return out, nil
}

func (r *memHostRepo) Create(_ context.Context, e *domain.HostAllowlistEntry) error {
	for _, x := range r.items {
		if strings.EqualFold(x.Pattern, e.Pattern) {
			return domain.ErrHostAlreadyExists
		}
	}
	r.seq++
	e.ID = string(rune('a'+r.seq)) + "-host"
	cp := *e
	r.items[e.ID] = &cp
	return nil
}

func (r *memHostRepo) UpdateDescription(_ context.Context, id, description string) error {
	e, ok := r.items[id]
	if !ok {
		return domain.ErrHostNotFound
	}
	e.Description = description
	return nil
}

func (r *memHostRepo) UpdatePattern(_ context.Context, id, pattern string, kind domain.HostKind) error {
	e, ok := r.items[id]
	if !ok {
		return domain.ErrHostNotFound
	}
	e.Pattern = pattern
	e.Kind = kind
	return nil
}

func (r *memHostRepo) Delete(_ context.Context, id string) error {
	if _, ok := r.items[id]; !ok {
		return domain.ErrHostNotFound
	}
	delete(r.items, id)
	return nil
}

func (r *memHostRepo) Link(_ context.Context, nodeID, hostID string) error {
	if r.links[nodeID] == nil {
		r.links[nodeID] = map[string]bool{}
	}
	if !r.links[nodeID][hostID] {
		r.links[nodeID][hostID] = true
		if e, ok := r.items[hostID]; ok {
			e.UsageCount++
		}
	}
	return nil
}

func (r *memHostRepo) Unlink(_ context.Context, nodeID, hostID string) error {
	if r.links[nodeID][hostID] {
		delete(r.links[nodeID], hostID)
		if e, ok := r.items[hostID]; ok {
			e.UsageCount--
		}
	}
	return nil
}

func (r *memHostRepo) ListByNode(_ context.Context, nodeID string) ([]*domain.HostAllowlistEntry, error) {
	var out []*domain.HostAllowlistEntry
	for hostID := range r.links[nodeID] {
		if e, ok := r.items[hostID]; ok {
			cp := *e
			out = append(out, &cp)
		}
	}
	return out, nil
}

// recordingCache фиксирует факт вызова Set.
type recordingCache struct {
	setCalls int
	last     *domain.Node
}

func (c *recordingCache) GetByPath(context.Context, string) (*domain.Node, error) {
	return nil, domain.ErrNotFound
}
func (c *recordingCache) Set(_ context.Context, n *domain.Node, _ time.Duration) error {
	c.setCalls++
	c.last = n
	return nil
}
func (c *recordingCache) InvalidateByPath(context.Context, string) error { return nil }

func newHostUC(hosts *memHostRepo, nodes *memNodeRepo, cache *recordingCache) *HostAllowlistUsecase {
	audit := NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop())
	return NewHostAllowlistUsecase(hosts, nodes, cache, nil, audit, time.Minute, logging.NewNoop())
}

func TestHostUC_Create_Idempotent(t *testing.T) {
	t.Parallel()
	hosts := newMemHostRepo()
	uc := newHostUC(hosts, newMemNodeRepo(), &recordingCache{})
	ctx := context.Background()

	e1 := &domain.HostAllowlistEntry{Pattern: "api.partner.com", Kind: domain.HostKindExact}
	require.NoError(t, uc.Create(ctx, SystemActor(), e1))
	require.NotEmpty(t, e1.ID)

	// Повторное создание того же паттерна (другой регистр) → возвращает существующую.
	e2 := &domain.HostAllowlistEntry{Pattern: "API.partner.com", Kind: domain.HostKindExact}
	require.NoError(t, uc.Create(ctx, SystemActor(), e2))
	assert.Equal(t, e1.ID, e2.ID, "идемпотентность: тот же ID")
	assert.Len(t, hosts.items, 1)
}

func TestHostUC_Update_PatternGuardedByUsage(t *testing.T) {
	t.Parallel()
	hosts := newMemHostRepo()
	uc := newHostUC(hosts, newMemNodeRepo(), &recordingCache{})
	ctx := context.Background()

	e := &domain.HostAllowlistEntry{Pattern: "api.partner.com", Kind: domain.HostKindExact}
	require.NoError(t, uc.Create(ctx, SystemActor(), e))
	hosts.items[e.ID].UsageCount = 3

	// Смена паттерна при usage>0 запрещена.
	err := uc.Update(ctx, SystemActor(), e.ID, "api2.partner.com", domain.HostKindExact, "")
	assert.ErrorIs(t, err, domain.ErrHostInUse)

	// Смена только описания — разрешена даже при usage>0.
	require.NoError(t, uc.Update(ctx, SystemActor(), e.ID, "api.partner.com", domain.HostKindExact, "prod"))
	assert.Equal(t, "prod", hosts.items[e.ID].Description)
}

func TestHostUC_Delete_GuardedByUsage(t *testing.T) {
	t.Parallel()
	hosts := newMemHostRepo()
	uc := newHostUC(hosts, newMemNodeRepo(), &recordingCache{})
	ctx := context.Background()

	e := &domain.HostAllowlistEntry{Pattern: "api.partner.com", Kind: domain.HostKindExact}
	require.NoError(t, uc.Create(ctx, SystemActor(), e))
	hosts.items[e.ID].UsageCount = 1
	assert.ErrorIs(t, uc.Delete(ctx, SystemActor(), e.ID), domain.ErrHostInUse)

	hosts.items[e.ID].UsageCount = 0
	require.NoError(t, uc.Delete(ctx, SystemActor(), e.ID))
	assert.Empty(t, hosts.items)
}

func TestHostUC_Preview(t *testing.T) {
	t.Parallel()
	uc := newHostUC(newMemHostRepo(), newMemNodeRepo(), &recordingCache{})
	res, err := uc.Preview(context.Background(), "*.partner.com", domain.HostKindWildcard, []string{
		"https://api.partner.com/v1",
		"https://partner.com/x",
		"not-a-url",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"https://api.partner.com/v1"}, res.Allowed)
	assert.ElementsMatch(t, []string{"https://partner.com/x", "not-a-url"}, res.Blocked)
}

func TestHostUC_Attach_RebuildsSnapshotAndCache(t *testing.T) {
	t.Parallel()
	hosts := newMemHostRepo()
	nodes := newMemNodeRepo()
	cache := &recordingCache{}
	uc := newHostUC(hosts, nodes, cache)
	ctx := context.Background()

	node := &domain.Node{Path: "svc/hook", RootMethod: domain.RootMethodRequest, TargetURL: "https://x", TeamID: "t1"}
	require.NoError(t, nodes.Create(ctx, node))

	exact := &domain.HostAllowlistEntry{Pattern: "api.partner.com", Kind: domain.HostKindExact}
	require.NoError(t, uc.Create(ctx, SystemActor(), exact))
	rgx := &domain.HostAllowlistEntry{Pattern: `^x\.io$`, Kind: domain.HostKindRegex}
	require.NoError(t, uc.Create(ctx, SystemActor(), rgx))

	require.NoError(t, uc.Attach(ctx, SystemActor(), node.ID, exact.ID, "t1"))
	require.NoError(t, uc.Attach(ctx, SystemActor(), node.ID, rgx.ID, "t1"))

	got, err := nodes.Get(ctx, node.ID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"api.partner.com", "re:^x\\.io$"}, got.URLAllowedHosts,
		"снимок пересобран с re:-кодированием")
	assert.GreaterOrEqual(t, cache.setCalls, 2, "кеш обновлён при каждом attach")

	// Чужая команда — ErrNodeNotFound.
	assert.ErrorIs(t, uc.Attach(ctx, SystemActor(), node.ID, exact.ID, "other"), domain.ErrNodeNotFound)
}

func TestHostUC_Detach_RebuildsSnapshot(t *testing.T) {
	t.Parallel()
	hosts := newMemHostRepo()
	nodes := newMemNodeRepo()
	uc := newHostUC(hosts, nodes, &recordingCache{})
	ctx := context.Background()

	node := &domain.Node{Path: "svc/hook", RootMethod: domain.RootMethodRequest, TargetURL: "https://x", TeamID: "t1"}
	require.NoError(t, nodes.Create(ctx, node))
	e := &domain.HostAllowlistEntry{Pattern: "api.partner.com", Kind: domain.HostKindExact}
	require.NoError(t, uc.Create(ctx, SystemActor(), e))
	require.NoError(t, uc.Attach(ctx, SystemActor(), node.ID, e.ID, "t1"))
	require.NoError(t, uc.Detach(ctx, SystemActor(), node.ID, e.ID, "t1"))

	got, err := nodes.Get(ctx, node.ID)
	require.NoError(t, err)
	assert.Empty(t, got.URLAllowedHosts, "после отвязки снимок пуст")
	assert.Equal(t, 0, hosts.items[e.ID].UsageCount)
}
