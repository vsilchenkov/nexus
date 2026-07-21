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

// TestNodeAuthor_E2E — §63: created_by/updated_by пишутся автором мутации.
//
// Сценарий:
//  1. Create актором alice → created_by=alice, updated_by пусто, created_at==updated_at.
//  2. Update актором bob → updated_by=bob, created_by=alice (не изменился),
//     updated_at > created_at (сигнал «узел меняли»).
//  3. SetStatus актором carol → updated_by=carol.
//  4. Copy актором dave → у клона created_by=dave, created_at==updated_at
//     (снимок allowlist в той же UoW-транзакции now() не разводит времена).
func TestNodeAuthor_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	cipher, err := crypto.NewCipher(testEncryptionKey)
	require.NoError(t, err)
	logger := logging.NewNoop()

	nodeRepo := pgrepo.NewNodeRepoPg(pool, cipher, logger)
	teamRepo := pgrepo.NewTeamRepoPg(pool, logger)
	uow := pgrepo.NewUnitOfWorkPg(pool, cipher, logger)
	auditUC := usecase.NewAuditUsecase(pgrepo.NewAuditRepoPg(pool, logger), logger)
	defaultTeam := resolveDefaultTeamID(t, ctx, pool)
	nodeUC := usecase.NewNodeUsecase(nodeRepo, nopCache{}, auditUC, uow, teamRepo,
		nil, nil, time.Minute, 0, defaultTeam, nil, logger)

	actor := func(login string) usecase.Actor {
		return usecase.Actor{UserID: "", UserLogin: login, TeamID: defaultTeam}
	}

	// 1. Create.
	n := &domain.Node{
		Path:       "author/test",
		RootMethod: domain.RootMethodRequest,
		URLMode:    domain.URLModeStatic,
		TargetURL:  "https://example.com/hook",
		TeamID:     defaultTeam,
	}
	require.NoError(t, nodeUC.Create(ctx, actor("alice"), n))
	created, err := nodeRepo.Get(ctx, n.ID)
	require.NoError(t, err)
	assert.Equal(t, "alice", created.CreatedBy, "created_by = создатель")
	assert.Empty(t, created.UpdatedBy, "updated_by пусто до правок")
	assert.True(t, created.CreatedAt.Equal(created.UpdatedAt), "created_at==updated_at сразу после создания")

	time.Sleep(5 * time.Millisecond) // развести транзакционные now()

	// 2. Update другим актором.
	upd := *created
	upd.TargetURL = "https://example.com/changed"
	require.NoError(t, nodeUC.Update(ctx, actor("bob"), &upd, defaultTeam))
	afterUpd, err := nodeRepo.Get(ctx, n.ID)
	require.NoError(t, err)
	assert.Equal(t, "bob", afterUpd.UpdatedBy, "updated_by = редактор")
	assert.Equal(t, "alice", afterUpd.CreatedBy, "created_by не меняется")
	assert.True(t, afterUpd.UpdatedAt.After(afterUpd.CreatedAt), "updated_at > created_at после правки")

	// 3. SetStatus (пауза) третьим актором.
	require.NoError(t, nodeUC.SetStatus(ctx, actor("carol"), n.ID, defaultTeam, domain.NodeStatusPaused))
	afterStatus, err := nodeRepo.Get(ctx, n.ID)
	require.NoError(t, err)
	assert.Equal(t, "carol", afterStatus.UpdatedBy, "updated_by = сменивший статус")
	assert.Equal(t, "alice", afterStatus.CreatedBy)

	// 4. Copy — автор клона = копировщик.
	clone, err := nodeUC.Copy(ctx, actor("dave"), n.ID, "author/test-copy", defaultTeam)
	require.NoError(t, err)
	gotClone, err := nodeRepo.Get(ctx, clone.ID)
	require.NoError(t, err)
	assert.Equal(t, "dave", gotClone.CreatedBy, "created_by клона = копировщик")
	assert.True(t, gotClone.CreatedAt.Equal(gotClone.UpdatedAt), "у копии created_at==updated_at (Обновлено скрыто)")
}
