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

// fakeUow — порт UnitOfWork без транзакции: просто вызывает fn с переданным
// bag'ом репозиториев (unit-тесты транзакционного пути Copy).
type fakeUow struct{ repos port.Repos }

func (f *fakeUow) Execute(ctx context.Context, fn func(context.Context, port.Repos) error) error {
	return fn(ctx, f.repos)
}

// sourceNode — валидный узел-источник с кредами для тестов Copy (§53).
func sourceNode() *domain.Node {
	return &domain.Node{
		ID: "src-1", Path: "svc/orders", TeamID: "team1",
		RootMethod: domain.RootMethodRequest, TargetURL: "https://backend.example",
		Status:                  domain.NodeStatusEnabled,
		AuthType:                domain.AuthTypeBasic,
		AuthCredentials:         "user:secret",
		IncomingAuthCredentials: "in-secret",
		RMQPassword:             "rmq-secret",
		ForwardHeaders:          []string{"X-Trace-Id"},
		ClickHouseTable:         "nexus_default.orders",
		TimeoutMs:               7000,
		Comment:                 "prod orders hook",
		CreatedAt:               time.Now(),
		UpdatedAt:               time.Now(),
	}
}

func newCopyUC(repo *memNodeRepo, auditRepo *stubAuditRepo, uow port.UnitOfWork, hardLimit int) *NodeUsecase {
	return NewNodeUsecase(repo, nopNodeCache{}, NewAuditUsecase(auditRepo, logging.NewNoop()),
		uow, nil, nil, nil, time.Minute, hardLimit, "default-team", nil, logging.NewNoop())
}

// TestNodeUC_Copy_Happy (§53): клон получает новый ID/path, всегда paused,
// креды и настройки переносятся, источник не изменяется, audit — node.copy.
func TestNodeUC_Copy_Happy(t *testing.T) {
	t.Parallel()
	repo := newMemNodeRepo()
	src := sourceNode()
	repo.items[src.ID] = src
	auditRepo := &stubAuditRepo{}
	uc := newCopyUC(repo, auditRepo, nil, 0)

	clone, err := uc.Copy(context.Background(), SystemActor(), "src-1", "svc/orders-copy", "team1")
	require.NoError(t, err)

	assert.NotEmpty(t, clone.ID)
	assert.NotEqual(t, src.ID, clone.ID)
	assert.Equal(t, "svc/orders-copy", clone.Path)
	assert.Equal(t, domain.NodeStatusPaused, clone.Status, "копия всегда paused, источник был enabled")
	assert.Equal(t, "team1", clone.TeamID)
	assert.True(t, clone.CreatedAt.IsZero(), "таймстемпы сбрасываются (назначает БД)")

	// Креды и настройки переносятся.
	assert.Equal(t, "user:secret", clone.AuthCredentials)
	assert.Equal(t, "in-secret", clone.IncomingAuthCredentials)
	assert.Equal(t, "rmq-secret", clone.RMQPassword)
	assert.Equal(t, "nexus_default.orders", clone.ClickHouseTable, "CH-таблица копируется как есть (§37)")
	assert.Equal(t, int32(7000), clone.TimeoutMs)
	assert.Equal(t, "prod orders hook", clone.Comment)
	assert.Equal(t, []string{"X-Trace-Id"}, clone.ForwardHeaders)

	// Слайс не алиасит источник.
	clone.ForwardHeaders[0] = "mutated"
	assert.Equal(t, "X-Trace-Id", src.ForwardHeaders[0], "ForwardHeaders клонирован, не алиас")

	// Источник в репо не изменён.
	stored := repo.items[src.ID]
	assert.Equal(t, "svc/orders", stored.Path)
	assert.Equal(t, domain.NodeStatusEnabled, stored.Status)

	require.Len(t, auditRepo.entries, 1)
	e := auditRepo.entries[0]
	assert.Equal(t, domain.ActionNodeCopy, e.Action)
	assert.Equal(t, clone.ID, e.TargetID)
	assert.Equal(t, "src-1", e.Details["source_node_id"])
	assert.Equal(t, "svc/orders", e.Details["source_path"])
	assert.Equal(t, string(domain.NodeStatusPaused), e.Details["status"])
}

// TestNodeUC_Copy_TeamScope: узел чужой команды → ErrNodeNotFound, ничего не создано.
func TestNodeUC_Copy_TeamScope(t *testing.T) {
	t.Parallel()
	repo := newMemNodeRepo()
	repo.items["src-1"] = sourceNode()
	auditRepo := &stubAuditRepo{}
	uc := newCopyUC(repo, auditRepo, nil, 0)

	_, err := uc.Copy(context.Background(), SystemActor(), "src-1", "svc/orders-copy", "other-team")
	assert.ErrorIs(t, err, domain.ErrNodeNotFound)
	assert.Len(t, repo.items, 1, "копия не создана")
	assert.Empty(t, auditRepo.entries)
}

// TestNodeUC_Copy_SourceNotFound: несуществующий источник → ErrNodeNotFound.
func TestNodeUC_Copy_SourceNotFound(t *testing.T) {
	t.Parallel()
	uc := newCopyUC(newMemNodeRepo(), &stubAuditRepo{}, nil, 0)
	_, err := uc.Copy(context.Background(), SystemActor(), "nope", "svc/x", "team1")
	assert.ErrorIs(t, err, domain.ErrNodeNotFound)
}

// TestNodeUC_Copy_PathConflict: занятый path → ErrNodeAlreadyExists (UNIQUE(team_id, path)).
func TestNodeUC_Copy_PathConflict(t *testing.T) {
	t.Parallel()
	repo := newMemNodeRepo()
	repo.items["src-1"] = sourceNode()
	uc := newCopyUC(repo, &stubAuditRepo{}, nil, 0)

	_, err := uc.Copy(context.Background(), SystemActor(), "src-1", "svc/orders", "team1")
	assert.ErrorIs(t, err, domain.ErrNodeAlreadyExists)
}

// TestNodeUC_Copy_InvalidPath: невалидный новый path режется Validate до вставки.
func TestNodeUC_Copy_InvalidPath(t *testing.T) {
	t.Parallel()
	repo := newMemNodeRepo()
	repo.items["src-1"] = sourceNode()
	uc := newCopyUC(repo, &stubAuditRepo{}, nil, 0)

	_, err := uc.Copy(context.Background(), SystemActor(), "src-1", "/leading-slash", "team1")
	assert.ErrorIs(t, err, domain.ErrNodePathFormat)
	assert.Len(t, repo.items, 1, "копия не создана")
}

// TestNodeUC_Copy_HardLimit: лимит узлов действует и на копию.
func TestNodeUC_Copy_HardLimit(t *testing.T) {
	t.Parallel()
	repo := newMemNodeRepo()
	repo.items["src-1"] = sourceNode()
	uc := newCopyUC(repo, &stubAuditRepo{}, nil, 1)

	_, err := uc.Copy(context.Background(), SystemActor(), "src-1", "svc/orders-copy", "team1")
	assert.ErrorIs(t, err, domain.ErrLimitReached)
}

// TestNodeUC_Copy_ClonesAllowedHostLinks (§23+§53): в транзакционном пути
// привязки allowlist-хостов источника клонируются на копию, снимок
// url_allowed_hosts пересобирается, число ссылок попадает в audit.
func TestNodeUC_Copy_ClonesAllowedHostLinks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := newMemNodeRepo()
	src := sourceNode()
	repo.items[src.ID] = src

	hosts := newMemHostRepo()
	entry := &domain.HostAllowlistEntry{Pattern: "api.example.com", Kind: domain.HostKindExact}
	require.NoError(t, hosts.Create(ctx, entry))
	require.NoError(t, hosts.Link(ctx, src.ID, entry.ID))

	auditRepo := &stubAuditRepo{}
	uow := &fakeUow{repos: port.Repos{Nodes: repo, Audit: auditRepo, Hosts: hosts}}
	uc := newCopyUC(repo, auditRepo, uow, 0)

	clone, err := uc.Copy(ctx, SystemActor(), src.ID, "svc/orders-copy", "team1")
	require.NoError(t, err)

	assert.True(t, hosts.links[clone.ID][entry.ID], "ссылка хоста склонирована на копию")
	assert.Equal(t, []string{"api.example.com"}, clone.URLAllowedHosts, "снимок пересобран")
	assert.Equal(t, []string{"api.example.com"}, repo.items[clone.ID].URLAllowedHosts)

	require.Len(t, auditRepo.entries, 1)
	assert.Equal(t, domain.ActionNodeCopy, auditRepo.entries[0].Action)
	assert.Equal(t, 1, auditRepo.entries[0].Details["allowed_hosts_cloned"])
}

// TestNodeUC_Copy_KeepsExternalTable (§64): копия узла с внешней таблицей
// остаётся внешней — она ссылается на ту же таблицу, которой Nexus не управляет,
// и провижининг для неё не запускается.
func TestNodeUC_Copy_KeepsExternalTable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	prov := &verifyProvisioner{}
	repo := newMemNodeRepo()
	src := sourceNode()
	src.ExternalTable = true
	repo.items[src.ID] = src

	uc := NewNodeUsecase(repo, nopNodeCache{}, NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()),
		nil, nil, prov, newMemCHTemplateRepo(), time.Minute, 0, "default-team", nil, logging.NewNoop())

	clone, err := uc.Copy(ctx, SystemActor(), src.ID, "svc/orders-copy", "team1")
	require.NoError(t, err)
	assert.True(t, clone.ExternalTable, "признак внешней таблицы копируется")
	assert.Equal(t, "nexus_default.orders", clone.ClickHouseTable)
	assert.Empty(t, prov.createdTable, "внешняя таблица не провижинится")
}

// TestNodeUC_Copy_ProvisionsKeptCHTable (§19.5): у копии с template_id
// вызывается идемпотентный CreateTable на ту же (сохранённую) таблицу.
func TestNodeUC_Copy_ProvisionsKeptCHTable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	templates := newMemCHTemplateRepo()
	tpl := validTemplate("Std", false)
	require.NoError(t, templates.Create(ctx, tpl))
	prov := &verifyProvisioner{}
	repo := newMemNodeRepo()
	src := sourceNode()
	src.ClickHouseTemplateID = tpl.ID
	repo.items[src.ID] = src

	uc := NewNodeUsecase(repo, nopNodeCache{}, NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()),
		nil, nil, prov, templates, time.Minute, 0, "default-team", nil, logging.NewNoop())

	clone, err := uc.Copy(ctx, SystemActor(), src.ID, "svc/orders-copy", "team1")
	require.NoError(t, err)
	assert.Equal(t, "nexus_default.orders", clone.ClickHouseTable)
	assert.Equal(t, "nexus_default.orders", prov.createdTable, "провижининг идемпотентно вызван на ту же таблицу")
}
