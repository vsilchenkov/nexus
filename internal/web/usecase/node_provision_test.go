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

// memNodeRepo — минимальный in-memory port.NodeRepo для unit-тестов NodeUsecase.
type memNodeRepo struct {
	items map[string]*domain.Node
	seq   int
}

func newMemNodeRepo() *memNodeRepo { return &memNodeRepo{items: map[string]*domain.Node{}} }

func (r *memNodeRepo) Get(_ context.Context, id string) (*domain.Node, error) {
	if n, ok := r.items[id]; ok {
		cp := *n
		return &cp, nil
	}
	return nil, domain.ErrNodeNotFound
}
func (r *memNodeRepo) GetByPath(_ context.Context, _ string) (*domain.Node, error) {
	return nil, domain.ErrNodeNotFound
}
func (r *memNodeRepo) List(_ context.Context, _ port.ListNodesFilter) ([]*domain.Node, error) {
	return nil, nil
}
func (r *memNodeRepo) Count(_ context.Context, _ string) (int, error) { return len(r.items), nil }
func (r *memNodeRepo) Create(_ context.Context, n *domain.Node) error {
	r.seq++
	n.ID = string(rune('a'+r.seq)) + "-node"
	cp := *n
	r.items[n.ID] = &cp
	return nil
}
func (r *memNodeRepo) Update(_ context.Context, n *domain.Node) error {
	if _, ok := r.items[n.ID]; !ok {
		return domain.ErrNodeNotFound
	}
	cp := *n
	r.items[n.ID] = &cp
	return nil
}
func (r *memNodeRepo) Delete(_ context.Context, id string) error {
	delete(r.items, id)
	return nil
}
func (r *memNodeRepo) UpdateAllowedHostsSnapshot(_ context.Context, nodeID string, patterns []string) error {
	n, ok := r.items[nodeID]
	if !ok {
		return domain.ErrNodeNotFound
	}
	n.URLAllowedHosts = patterns
	return nil
}

// nopNodeCache — заглушка port.NodeCache.
type nopNodeCache struct{}

func (nopNodeCache) Get(context.Context, string) (*domain.Node, error) {
	return nil, domain.ErrNotFound
}
func (nopNodeCache) GetByPath(context.Context, string) (*domain.Node, error) {
	return nil, domain.ErrNotFound
}
func (nopNodeCache) Set(context.Context, *domain.Node, time.Duration) error { return nil }
func (nopNodeCache) Invalidate(context.Context, string) error               { return nil }
func (nopNodeCache) InvalidateByPath(context.Context, string) error         { return nil }

func newNodeUC(repo *memNodeRepo, prov *verifyProvisioner, templates *memCHTemplateRepo) *NodeUsecase {
	audit := NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop())
	var p port.TeamProvisioner
	if prov != nil {
		p = prov
	}
	var tr port.CHTemplateRepo
	if templates != nil {
		tr = templates
	}
	// teams=nil (нормализация CH-таблицы выключена), uow=nil (не-tx путь).
	return NewNodeUsecase(repo, nopNodeCache{}, audit, nil, nil, p, tr, time.Minute, 0, "default-team", logging.NewNoop())
}

func nodeWithTemplate(templateID, table string) *domain.Node {
	return &domain.Node{
		Path: "svc/hook", RootMethod: domain.RootMethodRequest, TargetURL: "https://x",
		ClickHouseTable: table, ClickHouseTemplateID: templateID, ClickHouseRetentionDays: 30,
	}
}

func TestNodeUC_Create_ProvisionsTable(t *testing.T) {
	t.Parallel()
	templates := newMemCHTemplateRepo()
	tpl := validTemplate("Std", false)
	require.NoError(t, templates.Create(context.Background(), tpl))
	prov := &verifyProvisioner{}
	uc := newNodeUC(newMemNodeRepo(), prov, templates)

	err := uc.Create(context.Background(), SystemActor(), nodeWithTemplate(tpl.ID, "nexus_default.hook"))
	require.NoError(t, err)
	assert.Equal(t, "nexus_default.hook", prov.createdTable, "CreateTable must be called with node table")
	assert.Contains(t, prov.createdDDL, "CREATE TABLE IF NOT EXISTS nexus_default.hook")
}

func TestNodeUC_Create_NoTemplate_SkipsProvision(t *testing.T) {
	t.Parallel()
	prov := &verifyProvisioner{}
	uc := newNodeUC(newMemNodeRepo(), prov, newMemCHTemplateRepo())

	n := &domain.Node{Path: "svc/hook", RootMethod: domain.RootMethodRequest, TargetURL: "https://x", ClickHouseTable: "nexus_default.hook"}
	require.NoError(t, uc.Create(context.Background(), SystemActor(), n))
	assert.Empty(t, prov.createdTable, "no template → no CreateTable")
}

func TestNodeUC_Create_TemplateButNoProvisioner_Errors(t *testing.T) {
	t.Parallel()
	templates := newMemCHTemplateRepo()
	tpl := validTemplate("Std", false)
	require.NoError(t, templates.Create(context.Background(), tpl))
	// provisioner nil → ErrCHUnavailable, узел не создаётся.
	uc := newNodeUC(newMemNodeRepo(), nil, templates)

	err := uc.Create(context.Background(), SystemActor(), nodeWithTemplate(tpl.ID, "nexus_default.hook"))
	assert.ErrorIs(t, err, ErrCHUnavailable)
}

func TestNodeUC_Update_ProvisionsOnlyOnChange(t *testing.T) {
	t.Parallel()
	templates := newMemCHTemplateRepo()
	tpl := validTemplate("Std", false)
	require.NoError(t, templates.Create(context.Background(), tpl))
	prov := &verifyProvisioner{}
	repo := newMemNodeRepo()
	uc := newNodeUC(repo, prov, templates)
	ctx := context.Background()

	n := nodeWithTemplate(tpl.ID, "nexus_default.hook")
	require.NoError(t, uc.Create(ctx, SystemActor(), n))
	prov.createdTable = "" // сбрасываем после Create

	// Update без смены table/template — provision не вызывается.
	upd := *n
	upd.TimeoutMs = 5000
	require.NoError(t, uc.Update(ctx, SystemActor(), &upd, ""))
	assert.Empty(t, prov.createdTable, "unchanged table/template → no CreateTable")

	// Update со сменой имени таблицы — provision вызывается.
	upd2 := *n
	upd2.ClickHouseTable = "nexus_default.hook2"
	require.NoError(t, uc.Update(ctx, SystemActor(), &upd2, ""))
	assert.Equal(t, "nexus_default.hook2", prov.createdTable)
}
