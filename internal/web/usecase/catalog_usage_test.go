package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// fakeCatalogLister запоминает аргументы и отдаёт заданную страницу.
type fakeCatalogLister struct {
	page    port.CatalogNodesPage
	err     error
	calls   int
	itemID  string
	teamIDs []string
	limit   int
}

func (f *fakeCatalogLister) NodesUsing(_ context.Context, itemID string, teamIDs []string, limit int) (port.CatalogNodesPage, error) {
	f.calls++
	f.itemID, f.teamIDs, f.limit = itemID, teamIDs, limit
	return f.page, f.err
}

// fakeMemberships — членства пользователя.
type fakeMemberships struct {
	teams []*domain.UserTeam
	err   error
}

func (f fakeMemberships) ListUserTeams(context.Context, string) ([]*domain.UserTeam, error) {
	return f.teams, f.err
}

func membership(id, slug, name string) *domain.UserTeam {
	return &domain.UserTeam{Team: domain.Team{ID: id, Slug: slug, Name: name}}
}

func newCatalogUsageUC(kind CatalogKind, l *fakeCatalogLister, m TeamMembershipLister) *CatalogUsageUsecase {
	return NewCatalogUsageUsecase(map[CatalogKind]port.CatalogNodeLister{kind: l}, m, logging.NewNoop())
}

func TestCatalogUsage_NodesUsing(t *testing.T) {
	t.Parallel()
	two := fakeMemberships{teams: []*domain.UserTeam{
		membership("t1", "logistics", "Логистика"),
		membership("t2", "stock", "Склад"),
	}}
	ref := func(id, team string) port.CatalogNodeRef {
		return port.CatalogNodeRef{ID: id, Path: "svc/" + id, TeamID: team, URLMode: domain.URLModeStatic}
	}

	tests := []struct {
		name        string
		members     fakeMemberships
		page        port.CatalogNodesPage
		wantTeamIDs []string
		want        CatalogNodes
	}{
		{
			name:        "свои и чужие: обогащение командой, hidden = total − visible",
			members:     two,
			page:        port.CatalogNodesPage{Items: []port.CatalogNodeRef{ref("a", "t1"), ref("b", "t2")}, Visible: 2, Total: 5},
			wantTeamIDs: []string{"t1", "t2"},
			want: CatalogNodes{
				Items: []CatalogNode{
					{CatalogNodeRef: ref("a", "t1"), TeamSlug: "logistics", TeamName: "Логистика"},
					{CatalogNodeRef: ref("b", "t2"), TeamSlug: "stock", TeamName: "Склад"},
				},
				Total: 5, Visible: 2, Hidden: 3,
			},
		},
		{
			// Усечение до 500: hidden от visible, а не от длины списка — иначе
			// 598 своих узлов приписались бы чужим командам.
			name:        "усечение: hidden считается от visible",
			members:     two,
			page:        port.CatalogNodesPage{Items: []port.CatalogNodeRef{ref("a", "t1")}, Visible: 600, Total: 612},
			wantTeamIDs: []string{"t1", "t2"},
			want: CatalogNodes{
				Items: []CatalogNode{{CatalogNodeRef: ref("a", "t1"), TeamSlug: "logistics", TeamName: "Логистика"}},
				Total: 612, Visible: 600, Hidden: 12,
			},
		},
		{
			name:        "нет членств: запрос с пустым скоупом, всё — чужое",
			members:     fakeMemberships{},
			page:        port.CatalogNodesPage{Items: []port.CatalogNodeRef{}, Total: 7},
			wantTeamIDs: []string{},
			want:        CatalogNodes{Items: []CatalogNode{}, Total: 7, Hidden: 7},
		},
		{
			name:        "visible > total не даёт отрицательного hidden",
			members:     two,
			page:        port.CatalogNodesPage{Items: []port.CatalogNodeRef{}, Visible: 3, Total: 2},
			wantTeamIDs: []string{"t1", "t2"},
			want:        CatalogNodes{Items: []CatalogNode{}, Total: 2, Visible: 3, Hidden: 0},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			l := &fakeCatalogLister{page: tt.page}
			got, err := newCatalogUsageUC(CatalogKindGroup, l, tt.members).NodesUsing(context.Background(), CatalogKindGroup, "g1", "u1")
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, "g1", l.itemID)
			assert.Equal(t, tt.wantTeamIDs, l.teamIDs)
			assert.Equal(t, maxCatalogNodes, l.limit)
		})
	}
}

func TestCatalogUsage_NodesUsing_Errors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	members := fakeMemberships{teams: []*domain.UserTeam{membership("t1", "a", "A")}}

	t.Run("not found справочника проходит насквозь", func(t *testing.T) {
		t.Parallel()
		for kind, sentinel := range map[CatalogKind]error{
			CatalogKindGroup:  domain.ErrNodeGroupNotFound,
			CatalogKindHeader: domain.ErrHeaderNotFound,
			CatalogKindHost:   domain.ErrHostNotFound,
		} {
			l := &fakeCatalogLister{err: sentinel}
			_, err := newCatalogUsageUC(kind, l, members).NodesUsing(ctx, kind, "x", "u1")
			assert.ErrorIs(t, err, sentinel, kind)
		}
	})

	t.Run("неизвестный вид — ошибка до чтения членств и БД", func(t *testing.T) {
		t.Parallel()
		l := &fakeCatalogLister{}
		uc := newCatalogUsageUC(CatalogKindGroup, l, fakeMemberships{err: errors.New("must not be called")})
		_, err := uc.NodesUsing(ctx, CatalogKind("request_field"), "x", "u1")
		require.ErrorIs(t, err, ErrUnknownCatalogKind)
		assert.Zero(t, l.calls)
	})

	t.Run("ошибка членств — без запроса к справочнику", func(t *testing.T) {
		t.Parallel()
		boom := errors.New("pg down")
		l := &fakeCatalogLister{}
		_, err := newCatalogUsageUC(CatalogKindHost, l, fakeMemberships{err: boom}).NodesUsing(ctx, CatalogKindHost, "x", "u1")
		require.ErrorIs(t, err, boom)
		assert.Zero(t, l.calls)
	})
}

// Карта копируется при сборке: правка исходной карты после NewCatalogUsageUsecase
// не подменяет lister.
func TestCatalogUsage_ListersCopied(t *testing.T) {
	t.Parallel()
	l := &fakeCatalogLister{page: port.CatalogNodesPage{Items: []port.CatalogNodeRef{}}}
	listers := map[CatalogKind]port.CatalogNodeLister{CatalogKindHeader: l}
	uc := NewCatalogUsageUsecase(listers, fakeMemberships{}, logging.NewNoop())
	delete(listers, CatalogKindHeader)

	_, err := uc.NodesUsing(context.Background(), CatalogKindHeader, "h1", "u1")
	require.NoError(t, err)
	assert.Equal(t, 1, l.calls)
}
