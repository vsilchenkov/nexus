//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
	pgrepo "nexus/internal/web/adapter/out/postgres"
	"nexus/internal/web/usecase"
	"nexus/internal/web/usecase/port"
)

// pathResolveFixture — PostgreSQL с командами и узлами для резолва путей.
type pathResolveFixture struct {
	nodeRepo *pgrepo.NodeRepoPg
	mkTeam   func(slug, name string) *domain.Team
	mkNode   func(team *domain.Team, path string) *domain.Node
}

func newPathResolveFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) pathResolveFixture {
	t.Helper()
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

	return pathResolveFixture{
		nodeRepo: nodeRepo,
		mkTeam: func(slug, name string) *domain.Team {
			tm := &domain.Team{Slug: slug, Name: name, CHDatabase: domain.CHDatabaseForSlug(slug)}
			require.NoError(t, teamRepo.Create(ctx, tm))
			return tm
		},
		mkNode: func(team *domain.Team, path string) *domain.Node {
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
		},
	}
}

// TestNodeRepoIDsByPaths_E2E — §98.2: резолв «путь узла → id» без команды.
//
// Проверяется на ЖИВОМ PostgreSQL, а не стабом, по двум причинам сразу.
// Во-первых, запрос написан руками и содержит `min(id::text)`: у типа uuid
// агрегата min нет вовсе, и без приведения он падал бы только в рантайме.
// Во-вторых, отсев неоднозначных путей делает СУБД (`HAVING count(*) = 1`), а не
// Go — в unit-тесте с картой в памяти это условие вообще не выполняется.
//
// Совместимость: конструкций PostgreSQL 13+ здесь нет (на бою 12).
func TestNodeRepoIDsByPaths_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()
	fx := newPathResolveFixture(t, ctx, pool)

	teamA := fx.mkTeam("kafka_a", "Kafka A")
	teamB := fx.mkTeam("kafka_b", "Kafka B")

	unique := fx.mkNode(teamA, "billing")
	// Один и тот же путь в двух командах: UNIQUE (team_id, path) это допускает,
	// и именно такой путь резолвер обязан не отдавать.
	fx.mkNode(teamA, "twin")
	fx.mkNode(teamB, "twin")

	got, err := fx.nodeRepo.IDsByPaths(ctx, []string{"billing", "twin", "never-existed"})
	require.NoError(t, err)

	assert.Equal(t, unique.ID, got["billing"], "однозначный путь резолвится в свой id")
	assert.NotContains(t, got, "twin", "путь в двух командах неоднозначен")
	assert.NotContains(t, got, "never-existed")

	// Пустой вход не должен доходить до СУБД и обязан давать пустую карту, а не nil.
	empty, err := fx.nodeRepo.IDsByPaths(ctx, nil)
	require.NoError(t, err)
	assert.NotNil(t, empty)
	assert.Empty(t, empty)
}

// TestNodeRepoRefsByPaths_E2E — §101.9: узлы по путям во всех командах вместе
// со слогом команды. По этому списку экран Kafka строит ссылку по точной паре
// (команда, путь), а рабочий стол проверяет однозначность пути для рядов до
// §101. Запрос с JOIN teams проверяется на живой СУБД: имя колонки и тип
// приведения `id::text` стаб не проверит. Синтаксис совместим с PostgreSQL 12.
func TestNodeRepoRefsByPaths_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()
	fx := newPathResolveFixture(t, ctx, pool)

	teamA := fx.mkTeam("refs_a", "Refs A")
	teamB := fx.mkTeam("refs_b", "Refs B")
	solo := fx.mkNode(teamA, "solo")
	twinA := fx.mkNode(teamA, "twin")
	twinB := fx.mkNode(teamB, "twin")
	fx.mkNode(teamB, "unrelated")

	got, err := fx.nodeRepo.RefsByPaths(ctx, []string{"solo", "twin", "never-existed"})
	require.NoError(t, err)
	assert.ElementsMatch(t, []port.NodeKeyRef{
		{ID: solo.ID, Key: port.NodeKey{Team: "refs_a", Path: "solo"}},
		{ID: twinA.ID, Key: port.NodeKey{Team: "refs_a", Path: "twin"}},
		{ID: twinB.ID, Key: port.NodeKey{Team: "refs_b", Path: "twin"}},
	}, got, "одноимённые узлы — обе строки, каждая со своим слогом")

	empty, err := fx.nodeRepo.RefsByPaths(ctx, nil)
	require.NoError(t, err)
	assert.NotNil(t, empty)
	assert.Empty(t, empty)
}

// TestNodeRepoPausedNodes_E2E — §113.8: узлы на паузе во всех командах со
// слогом команды, по порядку (слог, путь). Остальные статусы в список не
// попадают. Синтаксис совместим с PostgreSQL 12.
func TestNodeRepoPausedNodes_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()
	fx := newPathResolveFixture(t, ctx, pool)

	empty, err := fx.nodeRepo.PausedNodes(ctx)
	require.NoError(t, err)
	assert.NotNil(t, empty)
	assert.Empty(t, empty)

	teamA := fx.mkTeam("paused_a", "Paused A")
	teamB := fx.mkTeam("paused_b", "Paused B")
	zeta := fx.mkNode(teamB, "zeta")
	alpha := fx.mkNode(teamB, "alpha")
	solo := fx.mkNode(teamA, "solo")
	fx.mkNode(teamA, "active")
	disabled := fx.mkNode(teamA, "disabled")
	setStatus := func(id string, st domain.NodeStatus) {
		_, err := pool.Exec(ctx, `UPDATE nodes SET status = $1 WHERE id = $2`, string(st), id)
		require.NoError(t, err)
	}
	for _, n := range []*domain.Node{zeta, alpha, solo} {
		setStatus(n.ID, domain.NodeStatusPaused)
	}
	setStatus(disabled.ID, domain.NodeStatusDisabled)

	got, err := fx.nodeRepo.PausedNodes(ctx)
	require.NoError(t, err)
	assert.Equal(t, []port.NodeKeyRef{
		{ID: solo.ID, Key: port.NodeKey{Team: "paused_a", Path: "solo"}},
		{ID: alpha.ID, Key: port.NodeKey{Team: "paused_b", Path: "alpha"}},
		{ID: zeta.ID, Key: port.NodeKey{Team: "paused_b", Path: "zeta"}},
	}, got)
}
