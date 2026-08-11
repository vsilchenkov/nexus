package usecase

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/web/usecase/port"
)

// TestNodeUC_ListAcrossTeams_Scope (§86.2): скоуп сквозного списка — членства
// пользователя, а не команда сессии.
func TestNodeUC_ListAcrossTeams_Scope(t *testing.T) {
	t.Parallel()
	repo := &capturingNodeRepo{
		memNodeRepo: newMemNodeRepo(),
		listResult: []*domain.Node{
			{ID: "n1", TeamID: "team1", Path: "svc/parcel"},
			{ID: "n2", TeamID: "team2", Path: "svc/parcel"}, // тот же путь в другой команде — норма
		},
	}
	teams := &membershipTeamRepo{memberships: []*domain.UserTeam{
		{Team: domain.Team{ID: "team1", Slug: "alpha", Name: "Alpha"}, Role: domain.TeamRoleMember},
		{Team: domain.Team{ID: "team2", Slug: "beta", Name: "Beta"}, Role: domain.TeamRoleMember},
	}}
	uc := newSearchUC(repo, teams)

	// f.TeamID заполнен командой сессии — сквозной режим обязан его отбросить,
	// иначе в репозиторий уехали бы два конфликтующих скоупа сразу.
	nodes, err := uc.ListAcrossTeams(context.Background(), "u1", port.ListNodesFilter{TeamID: "session-team"})
	require.NoError(t, err)
	require.Len(t, nodes, 2)

	assert.ElementsMatch(t, []string{"team1", "team2"}, repo.lastFilter.TeamIDs)
	assert.Empty(t, repo.lastFilter.TeamID, "команда сессии не должна попадать в сквозной фильтр")
}

// TestNodeUC_ListAcrossTeams_KeepsFilters (§86.3): поиск, метод и лимит работают
// так же, как в обычном списке — сквозной режим меняет только скоуп.
func TestNodeUC_ListAcrossTeams_KeepsFilters(t *testing.T) {
	t.Parallel()
	repo := &capturingNodeRepo{memNodeRepo: newMemNodeRepo()}
	teams := &membershipTeamRepo{memberships: []*domain.UserTeam{
		{Team: domain.Team{ID: "team1", Slug: "alpha"}, Role: domain.TeamRoleMember},
	}}

	_, err := newSearchUC(repo, teams).ListAcrossTeams(context.Background(), "u1", port.ListNodesFilter{
		Search:     "parcel",
		RootMethod: "request",
		Limit:      50,
		Offset:     10,
	})
	require.NoError(t, err)

	assert.Equal(t, "parcel", repo.lastFilter.Search)
	assert.Equal(t, "request", repo.lastFilter.RootMethod)
	assert.Equal(t, 50, repo.lastFilter.Limit)
	assert.Equal(t, 10, repo.lastFilter.Offset)
}

// TestNodeUC_ListAcrossTeams_Guards (§86.2, §86.9): деградационные ветки.
func TestNodeUC_ListAcrossTeams_Guards(t *testing.T) {
	t.Parallel()

	t.Run("нет членств → пустой список, репозиторий не вызван", func(t *testing.T) {
		t.Parallel()
		repo := &capturingNodeRepo{memNodeRepo: newMemNodeRepo()}
		nodes, err := newSearchUC(repo, &membershipTeamRepo{}).ListAcrossTeams(
			context.Background(), "u1", port.ListNodesFilter{})
		require.NoError(t, err)
		assert.Empty(t, nodes)
		// Ключевая проверка: пустой TeamIDs в репозитории означал бы «фильтра
		// нет» → выдачу узлов ВСЕХ команд инстанса. Запрос не должен уйти вовсе.
		assert.Zero(t, repo.listCalls, "без членств запрос в репозиторий не уходит")
	})

	t.Run("без TeamRepo → ошибка, а не тихий сквозной доступ", func(t *testing.T) {
		t.Parallel()
		repo := &capturingNodeRepo{memNodeRepo: newMemNodeRepo()}
		_, err := newSearchUC(repo, nil).ListAcrossTeams(context.Background(), "u1", port.ListNodesFilter{})
		require.Error(t, err)
		assert.Zero(t, repo.listCalls)
	})
}
