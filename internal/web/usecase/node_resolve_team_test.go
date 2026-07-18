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

// newResolveUC — NodeUsecase с teams-репо для тестов ResolveTeam (§58).
func newResolveUC(repo *memNodeRepo, teams port.TeamRepo) *NodeUsecase {
	return NewNodeUsecase(repo, nopNodeCache{}, NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()),
		nil, teams, nil, nil, time.Minute, 0, "default-team", nil, logging.NewNoop())
}

// TestNodeUC_ResolveTeam_Member (§58): пользователь состоит в команде узла →
// возвращается команда (id/slug/name) независимо от текущей команды сессии.
func TestNodeUC_ResolveTeam_Member(t *testing.T) {
	t.Parallel()
	repo := newMemNodeRepo()
	repo.items["n1"] = &domain.Node{ID: "n1", TeamID: "team1", Path: "svc/x", RootMethod: domain.RootMethodRequest}
	teams := &membershipTeamRepo{memberships: []*domain.UserTeam{
		{Team: domain.Team{ID: "team1", Slug: "alpha", Name: "Alpha"}, Role: domain.TeamRoleMember},
		{Team: domain.Team{ID: "team2", Slug: "beta", Name: "Beta"}, Role: domain.TeamRoleMember},
	}}
	uc := newResolveUC(repo, teams)

	team, err := uc.ResolveTeam(context.Background(), "u1", "n1")
	require.NoError(t, err)
	assert.Equal(t, "team1", team.ID)
	assert.Equal(t, "alpha", team.Slug)
	assert.Equal(t, "Alpha", team.Name)
}

// TestNodeUC_ResolveTeam_NotMember (§58, п.3): узел есть, но пользователь не
// член его команды → ErrNodeNotFound (no-leak, фронт покажет «Узел не доступен»).
func TestNodeUC_ResolveTeam_NotMember(t *testing.T) {
	t.Parallel()
	repo := newMemNodeRepo()
	repo.items["n1"] = &domain.Node{ID: "n1", TeamID: "team1", Path: "svc/x", RootMethod: domain.RootMethodRequest}
	teams := &membershipTeamRepo{memberships: []*domain.UserTeam{
		{Team: domain.Team{ID: "team2", Slug: "beta", Name: "Beta"}, Role: domain.TeamRoleMember},
	}}
	uc := newResolveUC(repo, teams)

	_, err := uc.ResolveTeam(context.Background(), "u1", "n1")
	assert.ErrorIs(t, err, domain.ErrNodeNotFound)
}

// TestNodeUC_ResolveTeam_NodeMissing (§58): несуществующий узел → ErrNodeNotFound
// (та же реакция, что «не член» — существование не различимо).
func TestNodeUC_ResolveTeam_NodeMissing(t *testing.T) {
	t.Parallel()
	teams := &membershipTeamRepo{memberships: []*domain.UserTeam{
		{Team: domain.Team{ID: "team1", Slug: "alpha", Name: "Alpha"}, Role: domain.TeamRoleMember},
	}}
	uc := newResolveUC(newMemNodeRepo(), teams)

	_, err := uc.ResolveTeam(context.Background(), "u1", "nope")
	assert.ErrorIs(t, err, domain.ErrNodeNotFound)
}

// TestNodeUC_ResolveTeam_NoTeamRepo: без TeamRepo (legacy single-team wiring) —
// ошибка, но НЕ ErrNodeNotFound (это конфигурационный сбой, не «недоступен»).
func TestNodeUC_ResolveTeam_NoTeamRepo(t *testing.T) {
	t.Parallel()
	repo := newMemNodeRepo()
	repo.items["n1"] = &domain.Node{ID: "n1", TeamID: "team1", Path: "svc/x", RootMethod: domain.RootMethodRequest}
	uc := newResolveUC(repo, nil)

	_, err := uc.ResolveTeam(context.Background(), "u1", "n1")
	require.Error(t, err)
	assert.NotErrorIs(t, err, domain.ErrNodeNotFound)
}
