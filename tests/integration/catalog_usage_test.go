//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
	pgrepo "nexus/internal/web/adapter/out/postgres"
	"nexus/internal/web/usecase/port"
)

// catalogPaths — пути строк списка по порядку.
func catalogPaths(items []port.CatalogNodeRef) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Path)
	}
	return out
}

// TestCatalogUsage_NodesUsing_E2E — §104: узлы, использующие элемент
// справочника, на реальном PostgreSQL (образ integration-тестов). Проверяет то, что моками не проверяется:
// Total совпадает с usage_count справочника для всех трёх видов (один
// предикат), Visible и Items видят только команды скоупа, заголовок в узле
// в другом регистре считается, не-UUID и несуществующий id — sentinel
// своего справочника, пустой скоуп — fail-closed, limit режет список, но
// не Visible.
func TestCatalogUsage_NodesUsing_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	logger := logging.NewNoop()
	cipher, err := crypto.NewCipher(testEncryptionKey)
	require.NoError(t, err)
	teamRepo := pgrepo.NewTeamRepoPg(pool, logger)
	nodeRepo := pgrepo.NewNodeRepoPg(pool, cipher, logger)
	groups := pgrepo.NewNodeGroupRepoPg(pool, logger)
	headers := pgrepo.NewHeaderCatalogRepoPg(pool, logger)
	hosts := pgrepo.NewHostAllowlistRepoPg(pool, logger)

	// Команды A и B — скоуп смотрящего, C — чужая.
	mkTeam := func(slug string) string {
		tm := &domain.Team{Slug: slug, Name: slug, CHDatabase: domain.CHDatabaseForSlug(slug)}
		require.NoError(t, teamRepo.Create(ctx, tm))
		return tm.ID
	}
	teamA, teamB, teamC := mkTeam("alpha"), mkTeam("beta"), mkTeam("gamma")
	scope := []string{teamA, teamB}

	group := &domain.NodeGroup{Name: "1С Обмен", CreatedBy: "admin"}
	require.NoError(t, groups.Create(ctx, group))
	header := &domain.HeaderCatalogEntry{Name: "X-Request-ID", CreatedBy: "admin"}
	require.NoError(t, headers.Create(ctx, header))
	host := &domain.HostAllowlistEntry{Pattern: "api.partner.com", Kind: domain.HostKindExact, CreatedBy: "admin"}
	require.NoError(t, hosts.Create(ctx, host))

	mkNode := func(teamID, path string, mutate func(n *domain.Node)) *domain.Node {
		n := &domain.Node{Path: path, RootMethod: domain.RootMethodRequest, TargetURL: "https://" + path + ".example"}
		n.SetDefaults()
		n.TeamID = teamID
		mutate(n)
		require.NoError(t, nodeRepo.Create(ctx, n))
		return n
	}
	inGroup := func(n *domain.Node) { n.GroupID = group.ID }
	// Заголовок записан в узле в другом регистре — считается (§104.2).
	withHeader := func(n *domain.Node) { n.ForwardHeaders = []string{"x-request-id", "X-Other"} }
	fromRequest := func(n *domain.Node) { n.URLMode = domain.URLModeFromRequest; n.TargetURL = "" }

	// Группа: 2 узла в A, 1 в B, 2 в C → total 5, visible 3.
	mkNode(teamA, "svc/b-orders", inGroup)
	mkNode(teamA, "svc/a-stock", inGroup)
	mkNode(teamB, "svc/c-billing", inGroup)
	mkNode(teamC, "svc/hidden-1", inGroup)
	mkNode(teamC, "svc/hidden-2", inGroup)
	mkNode(teamA, "svc/no-group", func(*domain.Node) {})

	// Заголовок: A и C → total 2, visible 1.
	mkNode(teamA, "hdr/a", withHeader)
	mkNode(teamC, "hdr/c", withHeader)
	mkNode(teamA, "hdr/other", func(n *domain.Node) { n.ForwardHeaders = []string{"X-Request-Idx"} })

	// Хост: static-узел в A (привязка «не действует»), from_request в B, узел в C.
	for _, n := range []*domain.Node{
		mkNode(teamA, "host/static", func(*domain.Node) {}),
		mkNode(teamB, "host/dynamic", fromRequest),
		mkNode(teamC, "host/hidden", fromRequest),
	} {
		require.NoError(t, hosts.Link(ctx, n.ID, host.ID))
	}

	t.Run("группа: total = usage_count, видимые по скоупу, порядок по path", func(t *testing.T) {
		page, err := groups.NodesUsing(ctx, group.ID, scope, 500)
		require.NoError(t, err)
		g, err := groups.Get(ctx, group.ID)
		require.NoError(t, err)
		assert.Equal(t, g.UsageCount, page.Total)
		assert.Equal(t, 5, page.Total)
		assert.Equal(t, 3, page.Visible)
		assert.Equal(t, []string{"svc/a-stock", "svc/b-orders", "svc/c-billing"}, catalogPaths(page.Items))
		for _, it := range page.Items {
			assert.Contains(t, scope, it.TeamID, "узел чужой команды утёк: %s", it.Path)
			assert.NotEmpty(t, it.ID)
			assert.Equal(t, domain.RootMethodRequest, it.RootMethod)
			assert.Equal(t, domain.NodeStatusEnabled, it.Status)
		}
	})

	t.Run("заголовок: регистр не важен, total = usage_count", func(t *testing.T) {
		page, err := headers.NodesUsing(ctx, header.ID, scope, 500)
		require.NoError(t, err)
		h, err := headers.Get(ctx, header.ID)
		require.NoError(t, err)
		assert.Equal(t, h.UsageCount, page.Total)
		assert.Equal(t, 2, page.Total)
		assert.Equal(t, 1, page.Visible)
		assert.Equal(t, []string{"hdr/a"}, catalogPaths(page.Items))
	})

	t.Run("хост: total = usage_count триггера, url_mode в строке", func(t *testing.T) {
		page, err := hosts.NodesUsing(ctx, host.ID, scope, 500)
		require.NoError(t, err)
		h, err := hosts.Get(ctx, host.ID)
		require.NoError(t, err)
		assert.Equal(t, h.UsageCount, page.Total)
		assert.Equal(t, 3, page.Total)
		assert.Equal(t, 2, page.Visible)
		require.Equal(t, []string{"host/dynamic", "host/static"}, catalogPaths(page.Items))
		assert.Equal(t, domain.URLModeFromRequest, page.Items[0].URLMode)
		assert.Equal(t, domain.URLModeStatic, page.Items[1].URLMode)
		assert.Equal(t, "https://host/static.example", page.Items[1].TargetURL)
	})

	t.Run("limit режет список, но не visible", func(t *testing.T) {
		page, err := groups.NodesUsing(ctx, group.ID, scope, 1)
		require.NoError(t, err)
		assert.Equal(t, []string{"svc/a-stock"}, catalogPaths(page.Items))
		assert.Equal(t, 3, page.Visible)
		assert.Equal(t, 5, page.Total)
	})

	t.Run("пустой скоуп — видимых нет, total считается", func(t *testing.T) {
		for _, sc := range [][]string{nil, {}} {
			page, err := groups.NodesUsing(ctx, group.ID, sc, 500)
			require.NoError(t, err)
			assert.Empty(t, page.Items)
			assert.NotNil(t, page.Items, "items обязан быть [], а не null в JSON")
			assert.Equal(t, 0, page.Visible)
			assert.Equal(t, 5, page.Total)
		}
	})

	t.Run("элемент без узлов — пустая страница, не ошибка", func(t *testing.T) {
		empty := &domain.NodeGroup{Name: "Резерв", CreatedBy: "admin"}
		require.NoError(t, groups.Create(ctx, empty))
		page, err := groups.NodesUsing(ctx, empty.ID, scope, 500)
		require.NoError(t, err)
		assert.Equal(t, port.CatalogNodesPage{Items: []port.CatalogNodeRef{}}, page)
	})

	t.Run("нет элемента или не UUID — sentinel своего справочника", func(t *testing.T) {
		const missing = "00000000-0000-0000-0000-00000000abcd"
		cases := []struct {
			name   string
			lister port.CatalogNodeLister
			want   error
		}{
			{"group", groups, domain.ErrNodeGroupNotFound},
			{"header", headers, domain.ErrHeaderNotFound},
			{"host", hosts, domain.ErrHostNotFound},
		}
		for _, tc := range cases {
			for _, id := range []string{missing, "not-a-uuid"} {
				_, err := tc.lister.NodesUsing(ctx, id, scope, 500)
				assert.ErrorIs(t, err, tc.want, "%s id=%q", tc.name, id)
			}
		}
	})
}
