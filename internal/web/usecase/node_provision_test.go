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
	// Как UNIQUE(team_id, path) в БД (§18) — нужен тестам Copy на конфликт пути.
	for _, x := range r.items {
		if x.TeamID == n.TeamID && x.Path == n.Path {
			return domain.ErrNodeAlreadyExists
		}
	}
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

func (nopNodeCache) GetByPath(context.Context, string, string) (*domain.Node, error) {
	return nil, domain.ErrNotFound
}
func (nopNodeCache) Set(context.Context, string, *domain.Node, time.Duration) error { return nil }
func (nopNodeCache) InvalidateByPath(context.Context, string, string) error         { return nil }

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
	return NewNodeUsecase(repo, nopNodeCache{}, audit, nil, nil, p, tr, time.Minute, 0, "default-team", nil, logging.NewNoop())
}

// TestNodeUC_SetStatus (§35): SetStatus меняет только статус, пишет audit,
// уважает team-scope, валидирует статус, no-op при том же значении.
func TestNodeUC_SetStatus(t *testing.T) {
	t.Parallel()
	repo := newMemNodeRepo()
	repo.items["n1"] = &domain.Node{
		ID: "n1", Path: "svc/x", TeamID: "team1", TargetURL: "https://x",
		Status: domain.NodeStatusEnabled, RootMethod: domain.RootMethodRequestAsync,
	}
	auditRepo := &stubAuditRepo{}
	uc := NewNodeUsecase(repo, nopNodeCache{}, NewAuditUsecase(auditRepo, logging.NewNoop()),
		nil, nil, nil, nil, time.Minute, 0, "default-team", nil, logging.NewNoop())
	ctx := context.Background()

	require.NoError(t, uc.SetStatus(ctx, SystemActor(), "n1", "team1", domain.NodeStatusPaused))
	assert.Equal(t, domain.NodeStatusPaused, repo.items["n1"].Status)
	require.Len(t, auditRepo.entries, 1)
	assert.Equal(t, domain.ActionNodeUpdate, auditRepo.entries[0].Action)

	assert.ErrorIs(t, uc.SetStatus(ctx, SystemActor(), "n1", "team1", domain.NodeStatus("bogus")), domain.ErrNodeInvalidStatus)
	assert.ErrorIs(t, uc.SetStatus(ctx, SystemActor(), "n1", "other", domain.NodeStatusDisabled), domain.ErrNodeNotFound)
	assert.Equal(t, domain.NodeStatusPaused, repo.items["n1"].Status, "чужая команда/невалид не меняют статус")

	// no-op: тот же статус — без ошибки и без новой audit-записи.
	require.NoError(t, uc.SetStatus(ctx, SystemActor(), "n1", "team1", domain.NodeStatusPaused))
	assert.Len(t, auditRepo.entries, 1)
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

// nodeWithTable — узел без template_id, но с заданной таблицей и флагом
// логирования (как приходит со стенда: clickhouse_table без явного шаблона).
func nodeWithTable(table string, logging bool) *domain.Node {
	return &domain.Node{
		Path: "svc/hook", RootMethod: domain.RootMethodRequest, TargetURL: "https://x",
		ClickHouseTable: table, ClickHouseRetentionDays: 30, LoggingEnabled: logging,
	}
}

// Фикс #7: узел без template_id, но с включённым логированием — таблица
// создаётся из дефолтного шаблона каталога (иначе чтение логов/метрик упало бы
// с CH code 60 «Unknown table»).
func TestNodeUC_Create_NoTemplate_UsesDefaultWhenLogging(t *testing.T) {
	t.Parallel()
	templates := newMemCHTemplateRepo()
	require.NoError(t, templates.Create(context.Background(), validTemplate("Standard", true)))
	prov := &verifyProvisioner{}
	uc := newNodeUC(newMemNodeRepo(), prov, templates)

	require.NoError(t, uc.Create(context.Background(), SystemActor(), nodeWithTable("nexus_default.hook", true)))
	assert.Equal(t, "nexus_default.hook", prov.createdTable, "logging enabled + default template → table created")
	assert.Contains(t, prov.createdDDL, "CREATE TABLE IF NOT EXISTS nexus_default.hook")
}

// Нет дефолтного шаблона — деградация: узел создаётся, таблица нет (мягко).
func TestNodeUC_Create_NoTemplate_NoDefault_Degrades(t *testing.T) {
	t.Parallel()
	prov := &verifyProvisioner{}
	uc := newNodeUC(newMemNodeRepo(), prov, newMemCHTemplateRepo())

	require.NoError(t, uc.Create(context.Background(), SystemActor(), nodeWithTable("nexus_default.hook", true)),
		"no default template → node still created, no error")
	assert.Empty(t, prov.createdTable, "no default template → no CreateTable")
}

// Логирование выключено — таблица не создаётся даже при наличии дефолта.
func TestNodeUC_Create_LoggingDisabled_SkipsDefault(t *testing.T) {
	t.Parallel()
	templates := newMemCHTemplateRepo()
	require.NoError(t, templates.Create(context.Background(), validTemplate("Standard", true)))
	prov := &verifyProvisioner{}
	uc := newNodeUC(newMemNodeRepo(), prov, templates)

	require.NoError(t, uc.Create(context.Background(), SystemActor(), nodeWithTable("nexus_default.hook", false)))
	assert.Empty(t, prov.createdTable, "logging disabled → no provisioning even with default template")
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
