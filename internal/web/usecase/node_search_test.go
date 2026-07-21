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
	"nexus/internal/web/usecase/port"
)

// capturingNodeRepo — memNodeRepo с настраиваемым List, который запоминает
// переданный фильтр (§62): unit-тест SearchAcrossTeams проверяет и обогащение
// результатов, и корректность фильтра (TeamIDs/Search/Limit), не поднимая PG.
type capturingNodeRepo struct {
	*memNodeRepo
	lastFilter port.ListNodesFilter
	listResult []*domain.Node
	listCalls  int
}

func (r *capturingNodeRepo) List(_ context.Context, f port.ListNodesFilter) ([]*domain.Node, error) {
	r.lastFilter = f
	r.listCalls++
	return r.listResult, nil
}

func newSearchUC(repo port.NodeRepo, teams port.TeamRepo) *NodeUsecase {
	return NewNodeUsecase(repo, nopNodeCache{}, NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()),
		nil, teams, nil, nil, time.Minute, 0, "default-team", nil, logging.NewNoop())
}

// TestNodeUC_SearchAcrossTeams_Enrich (§62): найденные узлы обогащаются командой
// пользователя, а в репозиторий уходит фильтр по всем его командам.
func TestNodeUC_SearchAcrossTeams_Enrich(t *testing.T) {
	t.Parallel()
	repo := &capturingNodeRepo{
		memNodeRepo: newMemNodeRepo(),
		listResult: []*domain.Node{
			{ID: "n1", TeamID: "team1", Path: "svc/parcel", TargetURL: "http://a/x", RootMethod: domain.RootMethodRequest, Status: domain.NodeStatusEnabled},
			{ID: "n2", TeamID: "team2", Path: "svc/parcels", TargetURL: "http://b/y", RootMethod: domain.RootMethodRequestAsync, Status: domain.NodeStatusPaused},
		},
	}
	teams := &membershipTeamRepo{memberships: []*domain.UserTeam{
		{Team: domain.Team{ID: "team1", Slug: "alpha", Name: "Alpha"}, Role: domain.TeamRoleMember},
		{Team: domain.Team{ID: "team2", Slug: "beta", Name: "Beta"}, Role: domain.TeamRoleMember},
	}}
	uc := newSearchUC(repo, teams)

	hits, err := uc.SearchAcrossTeams(context.Background(), "u1", "parcel", 0)
	require.NoError(t, err)
	require.Len(t, hits, 2)
	assert.Equal(t, "Alpha", hits[0].TeamName)
	assert.Equal(t, "alpha", hits[0].TeamSlug)
	assert.Equal(t, "Beta", hits[1].TeamName)

	// В репозиторий ушёл фильтр по всем командам пользователя (не по одной).
	assert.ElementsMatch(t, []string{"team1", "team2"}, repo.lastFilter.TeamIDs)
	assert.Empty(t, repo.lastFilter.TeamID, "однокомандный TeamID не используется в кросс-поиске")
	assert.Equal(t, "parcel", repo.lastFilter.Search)
	assert.Equal(t, defaultNodeSearchLimit, repo.lastFilter.Limit, "limit=0 → дефолт 20")
}

// TestNodeUC_SearchAcrossTeams_Guards (§62): пустые/деградационные ветки не
// доходят до репозитория.
func TestNodeUC_SearchAcrossTeams_Guards(t *testing.T) {
	t.Parallel()

	t.Run("короткий запрос → пусто, репозиторий не вызван", func(t *testing.T) {
		t.Parallel()
		repo := &capturingNodeRepo{memNodeRepo: newMemNodeRepo()}
		teams := &membershipTeamRepo{memberships: []*domain.UserTeam{
			{Team: domain.Team{ID: "team1", Slug: "alpha"}, Role: domain.TeamRoleMember},
		}}
		hits, err := newSearchUC(repo, teams).SearchAcrossTeams(context.Background(), "u1", " a ", 0)
		require.NoError(t, err)
		assert.Empty(t, hits)
		assert.Zero(t, repo.listCalls, "по одной руне не грузим все команды")
	})

	t.Run("нет членств → пусто", func(t *testing.T) {
		t.Parallel()
		repo := &capturingNodeRepo{memNodeRepo: newMemNodeRepo()}
		teams := &membershipTeamRepo{memberships: nil}
		hits, err := newSearchUC(repo, teams).SearchAcrossTeams(context.Background(), "u1", "parcel", 0)
		require.NoError(t, err)
		assert.Empty(t, hits)
		assert.Zero(t, repo.listCalls)
	})

	t.Run("без TeamRepo → ошибка (конфигурационный сбой)", func(t *testing.T) {
		t.Parallel()
		repo := &capturingNodeRepo{memNodeRepo: newMemNodeRepo()}
		_, err := newSearchUC(repo, nil).SearchAcrossTeams(context.Background(), "u1", "parcel", 0)
		require.Error(t, err)
	})

	t.Run("limit выше потолка → срезается до max", func(t *testing.T) {
		t.Parallel()
		repo := &capturingNodeRepo{memNodeRepo: newMemNodeRepo()}
		teams := &membershipTeamRepo{memberships: []*domain.UserTeam{
			{Team: domain.Team{ID: "team1", Slug: "alpha"}, Role: domain.TeamRoleMember},
		}}
		_, err := newSearchUC(repo, teams).SearchAcrossTeams(context.Background(), "u1", "parcel", 9999)
		require.NoError(t, err)
		assert.Equal(t, maxNodeSearchLimit, repo.lastFilter.Limit)
	})

	t.Run("trim не режет валидную строку", func(t *testing.T) {
		t.Parallel()
		repo := &capturingNodeRepo{memNodeRepo: newMemNodeRepo()}
		teams := &membershipTeamRepo{memberships: []*domain.UserTeam{
			{Team: domain.Team{ID: "team1", Slug: "alpha"}, Role: domain.TeamRoleMember},
		}}
		_, err := newSearchUC(repo, teams).SearchAcrossTeams(context.Background(), "u1", "  "+strings.Repeat("x", 3)+"  ", 0)
		require.NoError(t, err)
		assert.Equal(t, "xxx", repo.lastFilter.Search)
	})
}
