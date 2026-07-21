package usecase

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
)

// TestNodeUC_Author (§63): Create проставляет created_by, Update — updated_by,
// не затирая created_by.
func TestNodeUC_Author(t *testing.T) {
	t.Parallel()

	repo := newMemNodeRepo()
	teams := &membershipTeamRepo{memberships: []*domain.UserTeam{
		{Team: domain.Team{ID: "default-team", Slug: "default"}, Role: domain.TeamRoleMember},
	}}
	uc := newResolveUC(repo, teams)

	n := &domain.Node{
		Path: "svc/x", RootMethod: domain.RootMethodRequest,
		URLMode: domain.URLModeStatic, TargetURL: "https://example.com/hook",
		TeamID: "default-team",
	}
	require.NoError(t, uc.Create(context.Background(), Actor{UserLogin: "alice"}, n))
	created, err := repo.Get(context.Background(), n.ID)
	require.NoError(t, err)
	assert.Equal(t, "alice", created.CreatedBy)
	assert.Empty(t, created.UpdatedBy)

	upd := *created
	upd.TargetURL = "https://example.com/changed"
	require.NoError(t, uc.Update(context.Background(), Actor{UserLogin: "bob"}, &upd, "default-team"))
	after, err := repo.Get(context.Background(), n.ID)
	require.NoError(t, err)
	assert.Equal(t, "bob", after.UpdatedBy, "updated_by = редактор")
	assert.Equal(t, "alice", after.CreatedBy, "created_by не затирается")
}
