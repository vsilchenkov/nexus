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
	"nexus/internal/web/usecase"
)

// TestNodeIDsByPaths_E2E — §98.2: резолв «путь узла → id» для вкладки Kafka.
//
// Проверяется на ЖИВОМ PostgreSQL, а не стабом, по двум причинам сразу.
// Во-первых, запрос написан руками и содержит `min(id::text)`: у типа uuid
// агрегата min нет вовсе, и без приведения он падал бы только в рантайме.
// Во-вторых, отсев неоднозначных путей делает СУБД (`HAVING count(*) = 1`), а не
// Go — в unit-тесте с картой в памяти это условие вообще не выполняется.
//
// Совместимость: конструкций PostgreSQL 13+ здесь нет (на бою 12).
func TestNodeIDsByPaths_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	cipher, err := crypto.NewCipher(testEncryptionKey)
	require.NoError(t, err)
	logger := logging.NewNoop()

	teamRepo := pgrepo.NewTeamRepoPg(pool, logger)
	nodeRepo := pgrepo.NewNodeRepoPg(pool, cipher, logger)
	auditUC := usecase.NewAuditUsecase(pgrepo.NewAuditRepoPg(pool, logger), logger)
	uow := pgrepo.NewUnitOfWorkPg(pool, cipher, logger)
	defaultTeam := resolveDefaultTeamID(t, ctx, pool)
	nodeUC := usecase.NewNodeUsecase(nodeRepo, nopCache{}, auditUC, uow, teamRepo,
		nil, nil, time.Minute, 0, defaultTeam, nil, logger)

	mkTeam := func(slug, name string) *domain.Team {
		tm := &domain.Team{Slug: slug, Name: name, CHDatabase: domain.CHDatabaseForSlug(slug)}
		require.NoError(t, teamRepo.Create(ctx, tm))
		return tm
	}
	teamA := mkTeam("kafka_a", "Kafka A")
	teamB := mkTeam("kafka_b", "Kafka B")

	mkNode := func(team *domain.Team, path string) *domain.Node {
		n := &domain.Node{
			Path:            path,
			RootMethod:      domain.RootMethodRequestAsync,
			URLMode:         domain.URLModeStatic,
			TargetURL:       "https://example.test/hook",
			TeamID:          team.ID,
			ClickHouseTable: "logs",
		}
		require.NoError(t, nodeUC.Create(ctx, usecase.SystemActor(), n))
		return n
	}

	unique := mkNode(teamA, "billing")
	// Один и тот же путь в двух командах: UNIQUE (team_id, path) это допускает,
	// и именно такую строку экран обязан оставить текстом.
	mkNode(teamA, "twin")
	mkNode(teamB, "twin")

	got, err := nodeRepo.IDsByPaths(ctx, []string{"billing", "twin", "never-existed"})
	require.NoError(t, err)

	assert.Equal(t, unique.ID, got["billing"], "однозначный путь резолвится в свой id")
	assert.NotContains(t, got, "twin", "путь в двух командах неоднозначен — ссылки быть не должно")
	assert.NotContains(t, got, "never-existed")

	// Пустой вход не должен доходить до СУБД и обязан давать пустую карту, а не nil.
	empty, err := nodeRepo.IDsByPaths(ctx, nil)
	require.NoError(t, err)
	assert.NotNil(t, empty)
	assert.Empty(t, empty)
}
