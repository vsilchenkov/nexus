//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
	pgrepo "nexus/internal/web/adapter/out/postgres"
	"nexus/internal/web/usecase"
	"nexus/internal/web/usecase/port"
)

// TestMultiTenancy_Isolation_E2E — Phase 10.G: end-to-end проверка
// изоляции команд (multi-tenancy v2, §16 ТЗ).
//
// Сценарий:
//  1. После миграций уже есть default-team. Создаём через TeamRepo
//     ещё две — acme и globex.
//  2. В каждой команде создаём узел с ОДИНАКОВЫМ path "/order"
//     (раньше это было бы UNIQUE-violation; после миграции 0008 — нет).
//  3. List по TeamID должен вернуть только узлы своей команды.
//  4. NodeUsecase.Get с чужим teamID → ErrNodeNotFound (cross-team
//     leak prevention).
//  5. NodeUsecase.Update с чужим teamID → ErrNodeNotFound.
//  6. NodeUsecase.Delete с чужим teamID → ErrNodeNotFound;
//     узлы выживают.
//  7. NodeUsecase.Create нормализует clickhouse_table до
//     "<team.ch_database>.<table>" (Phase 10.C.2).
//  8. Audit-записи о Create узла наследуют TeamID из Actor
//     (Phase 10.F.1).
func TestMultiTenancy_Isolation_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	cipher, err := crypto.NewCipher(testEncryptionKey)
	require.NoError(t, err)
	logger := logging.NewNoop()

	teamRepo := pgrepo.NewTeamRepoPg(pool, logger)
	nodeRepo := pgrepo.NewNodeRepoPg(pool, cipher, logger)
	auditRepo := pgrepo.NewAuditRepoPg(pool, logger)
	uow := pgrepo.NewUnitOfWorkPg(pool, cipher, logger)
	auditUC := usecase.NewAuditUsecase(auditRepo, logger)
	defaultTeam := resolveDefaultTeamID(t, ctx, pool)
	nodeUC := usecase.NewNodeUsecase(
		nodeRepo, nopCache{}, auditUC, uow, teamRepo,
		time.Minute, 0, defaultTeam, logger,
	)

	// 1. Создаём команды acme и globex напрямую через repo (provisioner
	// в этом тесте не нужен — CH-ветка проверяется отдельно).
	acme := &domain.Team{
		Slug: "acme", Name: "Acme Corp",
		CHDatabase: domain.CHDatabaseForSlug("acme"),
	}
	require.NoError(t, teamRepo.Create(ctx, acme))
	require.NotEmpty(t, acme.ID)

	globex := &domain.Team{
		Slug: "globex", Name: "Globex",
		CHDatabase: domain.CHDatabaseForSlug("globex"),
	}
	require.NoError(t, teamRepo.Create(ctx, globex))

	// 2. Одинаковый path "/order" в обеих командах.
	mkNode := func(teamID, path string) *domain.Node {
		return &domain.Node{
			Path:            path,
			RootMethod:      domain.RootMethodRequest,
			URLMode:         domain.URLModeStatic,
			TargetURL:       "https://example.com/" + path,
			TeamID:          teamID,
			ClickHouseTable: "logs", // короткое имя — NodeUsecase префиксует
		}
	}

	acmeNode := mkNode(acme.ID, "order")
	require.NoError(t, nodeUC.Create(ctx, usecase.Actor{
		UserID: "11111111-1111-1111-1111-111111111111", UserLogin: "u1",
		TeamID: acme.ID,
	}, acmeNode))
	require.NotEmpty(t, acmeNode.ID)

	globexNode := mkNode(globex.ID, "order")
	require.NoError(t, nodeUC.Create(ctx, usecase.Actor{
		UserID: "22222222-2222-2222-2222-222222222222", UserLogin: "u2",
		TeamID: globex.ID,
	}, globexNode))
	require.NotEmpty(t, globexNode.ID)
	require.NotEqual(t, acmeNode.ID, globexNode.ID,
		"одинаковый path в разных командах должен дать разные узлы")

	// 7. NodeUsecase префиксует clickhouse_table до nexus_<slug>.logs
	// (Phase 10.C.2).
	assert.Equal(t, "nexus_acme.logs", acmeNode.ClickHouseTable)
	assert.Equal(t, "nexus_globex.logs", globexNode.ClickHouseTable)

	// 3. List возвращает только узлы своей команды.
	acmeList, err := nodeRepo.List(ctx, port.ListNodesFilter{TeamID: acme.ID, Limit: 100})
	require.NoError(t, err)
	require.Len(t, acmeList, 1)
	assert.Equal(t, acmeNode.ID, acmeList[0].ID)

	globexList, err := nodeRepo.List(ctx, port.ListNodesFilter{TeamID: globex.ID, Limit: 100})
	require.NoError(t, err)
	require.Len(t, globexList, 1)
	assert.Equal(t, globexNode.ID, globexList[0].ID)

	// 4. Cross-team Get → ErrNodeNotFound (не утечка существования).
	_, err = nodeUC.Get(ctx, acmeNode.ID, globex.ID)
	assert.ErrorIs(t, err, domain.ErrNodeNotFound,
		"globex не должен видеть узел acme")

	_, err = nodeUC.Get(ctx, globexNode.ID, acme.ID)
	assert.ErrorIs(t, err, domain.ErrNodeNotFound)

	// Свой Get работает.
	gotAcme, err := nodeUC.Get(ctx, acmeNode.ID, acme.ID)
	require.NoError(t, err)
	assert.Equal(t, "order", gotAcme.Path)
	assert.Equal(t, acme.ID, gotAcme.TeamID)

	// 5. Cross-team Update → ErrNodeNotFound.
	updated := *acmeNode
	updated.TargetURL = "https://hacker.example/"
	err = nodeUC.Update(ctx, usecase.SystemActor(), &updated, globex.ID)
	assert.ErrorIs(t, err, domain.ErrNodeNotFound)

	// 6. Cross-team Delete → ErrNodeNotFound; оба узла живы.
	err = nodeUC.Delete(ctx, usecase.SystemActor(), acmeNode.ID, globex.ID)
	assert.ErrorIs(t, err, domain.ErrNodeNotFound)

	_, err = nodeRepo.Get(ctx, acmeNode.ID)
	require.NoError(t, err, "узел acme должен выжить после чужой Delete")
	_, err = nodeRepo.Get(ctx, globexNode.ID)
	require.NoError(t, err, "узел globex должен выжить после чужой Delete")

	// 8. Audit-записи о Create несут team_id своих узлов (Phase 10.F.1).
	acmeAudit, err := auditRepo.List(ctx, port.AuditFilter{
		TargetID: acmeNode.ID, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, acmeAudit, 1)
	assert.Equal(t, acme.ID, acmeAudit[0].TeamID,
		"audit-запись acme должна иметь team_id acme")

	globexAudit, err := auditRepo.List(ctx, port.AuditFilter{
		TargetID: globexNode.ID, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, globexAudit, 1)
	assert.Equal(t, globex.ID, globexAudit[0].TeamID)

	// 9. Audit-фильтр по TeamID отдаёт только записи этой команды.
	onlyAcme, err := auditRepo.List(ctx, port.AuditFilter{TeamID: acme.ID, Limit: 10})
	require.NoError(t, err)
	for _, e := range onlyAcme {
		assert.Equal(t, acme.ID, e.TeamID, "all audit entries scoped to acme")
	}

	// 10. Cleanup: удаление команды с активным узлом запрещено
	// FK ON DELETE RESTRICT.
	err = teamRepo.Delete(ctx, acme.ID)
	require.Error(t, err, "delete team with active nodes must fail (FK RESTRICT)")
	// Сообщение PG говорит про violation FK; точная строка зависит от
	// версии — проверяем по non-nil без жёсткой подстроки.
	require.NotNil(t, err)

	// После удаления узла команду можно дропнуть.
	require.NoError(t, nodeUC.Delete(ctx, usecase.SystemActor(), acmeNode.ID, acme.ID))
	require.NoError(t, teamRepo.Delete(ctx, acme.ID))

	_, err = teamRepo.GetByID(ctx, acme.ID)
	assert.True(t, errors.Is(err, domain.ErrTeamNotFound),
		"after Delete: team must not be found")
}
