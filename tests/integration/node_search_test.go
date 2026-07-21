//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
	pgrepo "nexus/internal/web/adapter/out/postgres"
	"nexus/internal/web/usecase"
)

// TestNodeSearch_AcrossTeams_E2E — §62: глобальный поиск узлов возвращает только
// узлы команд, в которых пользователь состоит, матчит и по path, и по target_url,
// обогащает командой-владельцем и уважает лимит.
func TestNodeSearch_AcrossTeams_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	cipher, err := crypto.NewCipher(testEncryptionKey)
	require.NoError(t, err)
	logger := logging.NewNoop()

	teamRepo := pgrepo.NewTeamRepoPg(pool, logger)
	nodeRepo := pgrepo.NewNodeRepoPg(pool, cipher, logger)
	userRepo := pgrepo.NewUserRepoPg(pool, logger)
	auditUC := usecase.NewAuditUsecase(pgrepo.NewAuditRepoPg(pool, logger), logger)
	uow := pgrepo.NewUnitOfWorkPg(pool, cipher, logger)
	defaultTeam := resolveDefaultTeamID(t, ctx, pool)
	nodeUC := usecase.NewNodeUsecase(nodeRepo, nopCache{}, auditUC, uow, teamRepo,
		nil, nil, time.Minute, 0, defaultTeam, nil, logger)

	// Команды A/B/C; пользователь член A и B, C — чужая.
	mkTeam := func(slug, name string) *domain.Team {
		tm := &domain.Team{Slug: slug, Name: name, CHDatabase: domain.CHDatabaseForSlug(slug)}
		require.NoError(t, teamRepo.Create(ctx, tm))
		return tm
	}
	teamA := mkTeam("alpha", "Alpha")
	teamB := mkTeam("beta", "Beta")
	teamC := mkTeam("gamma", "Gamma")

	u := &domain.User{Login: "search_user", Role: domain.UserRoleViewer, Active: true, Lang: domain.UserLangEN}
	require.NoError(t, userRepo.Create(ctx, u))
	require.NoError(t, teamRepo.AddMember(ctx, u.ID, teamA.ID, domain.TeamRoleMember))
	require.NoError(t, teamRepo.AddMember(ctx, u.ID, teamB.ID, domain.TeamRoleMember))

	mkNode := func(team *domain.Team, path, target string) {
		n := &domain.Node{
			Path:            path,
			RootMethod:      domain.RootMethodRequest,
			URLMode:         domain.URLModeStatic,
			TargetURL:       target,
			TeamID:          team.ID,
			ClickHouseTable: "logs",
		}
		require.NoError(t, nodeUC.Create(ctx, usecase.SystemActor(), n))
	}
	// A: матч по path; B: матч по target_url; C: матч по path, но чужая команда.
	mkNode(teamA, "svc/parcel-track", "https://a.example/x")
	mkNode(teamB, "svc/order", "https://parcels.example/y")
	mkNode(teamC, "svc/parcel-hidden", "https://c.example/z")
	// Ещё один узел A без совпадения — не должен попасть в выдачу "parcel".
	mkNode(teamA, "svc/health", "https://a.example/health")

	// Поиск "parcel" как пользователь-член A и B.
	hits, err := nodeUC.SearchAcrossTeams(ctx, u.ID, "parcel", 0)
	require.NoError(t, err)
	require.Len(t, hits, 2, "только совпадения в командах A и B; чужой узел C скрыт")

	byPath := map[string]usecase.NodeSearchHit{}
	for _, h := range hits {
		byPath[h.Node.Path] = h
	}
	// A — совпадение по path, команда обогащена.
	trackHit, ok := byPath["svc/parcel-track"]
	require.True(t, ok, "узел A найден по path")
	assert.Equal(t, teamA.ID, trackHit.Node.TeamID)
	assert.Equal(t, "alpha", trackHit.TeamSlug)
	assert.Equal(t, "Alpha", trackHit.TeamName)
	// B — совпадение по target_url.
	orderHit, ok := byPath["svc/order"]
	require.True(t, ok, "узел B найден по target_url")
	assert.Equal(t, "Beta", orderHit.TeamName)
	// C скрыт.
	_, leaked := byPath["svc/parcel-hidden"]
	assert.False(t, leaked, "узел чужой команды не должен утечь")

	// Лимит: 5 матчащих узлов в A, лимит 3 → 3 записи.
	for i := range 5 {
		mkNode(teamA, fmt.Sprintf("svc/limit-parcel-%02d", i), "https://a.example/l")
	}
	limited, err := nodeUC.SearchAcrossTeams(ctx, u.ID, "limit-parcel", 3)
	require.NoError(t, err)
	assert.Len(t, limited, 3, "лимит уважается")

	// Короткий запрос → пусто.
	empty, err := nodeUC.SearchAcrossTeams(ctx, u.ID, "p", 0)
	require.NoError(t, err)
	assert.Empty(t, empty)
}
