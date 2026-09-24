//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
	pgrepo "nexus/internal/web/adapter/out/postgres"
	"nexus/internal/web/usecase"
	"nexus/internal/web/usecase/port"
)

// revertEnv — стенд §108 на реальном PostgreSQL: узел и аудит пишутся одной
// UoW-транзакцией, как в Web.
type revertEnv struct {
	nodes  *pgrepo.NodeRepoPg
	audit  *pgrepo.AuditRepoPg
	nodeUC *usecase.NodeUsecase
	uc     *usecase.AuditRevertUsecase
	team   string
}

func newRevertEnv(t *testing.T, ctx context.Context) revertEnv {
	t.Helper()
	pool, cleanup := startPostgres(t, ctx)
	t.Cleanup(cleanup)
	cipher, err := crypto.NewCipher(testEncryptionKey)
	require.NoError(t, err)
	logger := logging.NewNoop()

	env := revertEnv{
		nodes: pgrepo.NewNodeRepoPg(pool, cipher, logger),
		audit: pgrepo.NewAuditRepoPg(pool, logger),
		team:  resolveDefaultTeamID(t, ctx, pool),
	}
	env.nodeUC = usecase.NewNodeUsecase(env.nodes, nopCache{},
		usecase.NewAuditUsecase(env.audit, logger), pgrepo.NewUnitOfWorkPg(pool, cipher, logger),
		pgrepo.NewTeamRepoPg(pool, logger), nil, nil, time.Minute, 0, env.team, nil, logger)
	env.uc = usecase.NewAuditRevertUsecase(env.audit, env.nodes, env.nodeUC, logger)
	return env
}

func (e revertEnv) actor(login string) usecase.Actor {
	return usecase.Actor{UserLogin: login, TeamID: e.team}
}

// lastEntry — самая свежая строка журнала узла с данным действием.
func (e revertEnv) lastEntry(t *testing.T, ctx context.Context, nodeID, action string) *domain.AuditEntry {
	t.Helper()
	got, err := e.audit.List(ctx, port.AuditFilter{TargetID: nodeID, Actions: []string{action}, Limit: 1})
	require.NoError(t, err)
	require.NotEmpty(t, got, "нет строки %s", action)
	return got[0]
}

// wireUpdatedAt — updated_at узла после прохода через JSON предпросмотра:
// клиент возвращает строку как пришла (§108.5).
func wireUpdatedAt(t *testing.T, p *usecase.RevertPreview) time.Time {
	t.Helper()
	require.NotNil(t, p.Node)
	ts, err := time.Parse(time.RFC3339Nano, p.Node.UpdatedAt.Format(time.RFC3339Nano))
	require.NoError(t, err)
	return ts
}

// TestAuditRevert_E2E — §108: предпросмотр, отмена, неактуальность строки,
// конфликт версии, повтор изменения через node.revert и лёгкий путь статуса.
func TestAuditRevert_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	env := newRevertEnv(t, ctx)

	n := &domain.Node{
		Path: "revert/test", RootMethod: domain.RootMethodRequest, URLMode: domain.URLModeStatic,
		TargetURL: "https://example.com/a", TimeoutMs: 1000, TeamID: env.team,
	}
	require.NoError(t, env.nodeUC.Create(ctx, env.actor("alice"), n))
	cur, err := env.nodes.Get(ctx, n.ID)
	require.NoError(t, err)
	upd := *cur
	upd.TargetURL, upd.TimeoutMs = "https://example.com/b", 2000
	require.NoError(t, env.nodeUC.Update(ctx, env.actor("bob"), &upd, env.team))
	updEntry := env.lastEntry(t, ctx, n.ID, domain.ActionNodeUpdate)
	time.Sleep(5 * time.Millisecond) // развести created_at строк журнала
	require.NoError(t, env.nodeUC.SetStatus(ctx, env.actor("carol"), n.ID, env.team, domain.NodeStatusPaused))
	statusEntry := env.lastEntry(t, ctx, n.ID, domain.ActionNodeUpdate)
	require.NotEqual(t, updEntry.ID, statusEntry.ID)

	t.Run("filter by id", func(t *testing.T) {
		got, err := env.audit.List(ctx, port.AuditFilter{ID: updEntry.ID, Limit: 10})
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, updEntry.ID, got[0].ID)
		got, err = env.audit.List(ctx, port.AuditFilter{ID: uuid.NewString(), Limit: 10})
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	p, err := env.uc.Preview(ctx, updEntry.ID)
	require.NoError(t, err)
	assert.Equal(t, usecase.RevertStateActual, p.State)
	require.NoError(t, p.DryRunErr)
	time.Sleep(5 * time.Millisecond) // развести транзакционные now()
	require.NoError(t, env.uc.Apply(ctx, env.actor("admin"), updEntry.ID,
		[]string{"target_url", "timeout_ms"}, wireUpdatedAt(t, p)))

	got, err := env.nodes.Get(ctx, n.ID)
	require.NoError(t, err)
	assert.Equal(t, "https://example.com/a", got.TargetURL)
	assert.Equal(t, int32(1000), got.TimeoutMs)
	assert.Equal(t, domain.NodeStatusPaused, got.Status, "статус в отменяемой строке не менялся")
	assert.Equal(t, "admin", got.UpdatedBy)

	revertEntry := env.lastEntry(t, ctx, n.ID, domain.ActionNodeRevert)
	assert.Equal(t, updEntry.ID, revertEntry.Details["reverted_from"])
	assert.NotEmpty(t, revertEntry.Details["reverted_from_at"])
	assert.Equal(t, env.team, revertEntry.TeamID, "строка отмены — в команде узла")
	assert.Contains(t, revertEntry.Details, "target_url")

	t.Run("source entry becomes stale", func(t *testing.T) {
		marks := env.uc.Annotate(ctx, []*domain.AuditEntry{updEntry, statusEntry, revertEntry})
		assert.Equal(t, usecase.RevertStateStale, marks[updEntry.ID].State)
		assert.Equal(t, "https://example.com/a", marks[updEntry.ID].Overwritten["target_url"])
		assert.Equal(t, usecase.RevertStateActual, marks[statusEntry.ID].State)
		assert.Equal(t, usecase.RevertStateActual, marks[revertEntry.ID].State)
	})

	t.Run("stale version conflicts", func(t *testing.T) {
		err := env.uc.Apply(ctx, env.actor("admin"), updEntry.ID, []string{"target_url"}, wireUpdatedAt(t, p))
		require.ErrorIs(t, err, domain.ErrAuditRevertConflict)
	})

	t.Run("redo via node.revert", func(t *testing.T) {
		rp, err := env.uc.Preview(ctx, revertEntry.ID)
		require.NoError(t, err)
		time.Sleep(5 * time.Millisecond)
		require.NoError(t, env.uc.Apply(ctx, env.actor("admin"), revertEntry.ID,
			[]string{"target_url"}, wireUpdatedAt(t, rp)))
		got, err := env.nodes.Get(ctx, n.ID)
		require.NoError(t, err)
		assert.Equal(t, "https://example.com/b", got.TargetURL)
	})

	t.Run("status light path", func(t *testing.T) {
		sp, err := env.uc.Preview(ctx, statusEntry.ID)
		require.NoError(t, err)
		time.Sleep(5 * time.Millisecond)
		require.NoError(t, env.uc.Apply(ctx, env.actor("admin"), statusEntry.ID,
			[]string{"status"}, wireUpdatedAt(t, sp)))
		got, err := env.nodes.Get(ctx, n.ID)
		require.NoError(t, err)
		assert.Equal(t, domain.NodeStatusEnabled, got.Status)
	})
}

// TestAuditRevert_PathTakenAndGone — §108.7: возврат пути, занятого другим
// узлом, — ErrNodeAlreadyExists (409), узел не меняется; после удаления узла
// строка gone, отмена — ErrNodeNotFound; GetByIDs отдаёт только живые узлы.
func TestAuditRevert_PathTakenAndGone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	env := newRevertEnv(t, ctx)

	n := &domain.Node{
		Path: "revert/old", RootMethod: domain.RootMethodRequest, URLMode: domain.URLModeStatic,
		TargetURL: "https://example.com/a", TeamID: env.team,
	}
	require.NoError(t, env.nodeUC.Create(ctx, env.actor("alice"), n))
	cur, err := env.nodes.Get(ctx, n.ID)
	require.NoError(t, err)
	upd := *cur
	upd.Path = "revert/new"
	require.NoError(t, env.nodeUC.Update(ctx, env.actor("bob"), &upd, env.team))
	entry := env.lastEntry(t, ctx, n.ID, domain.ActionNodeUpdate)

	other := &domain.Node{
		Path: "revert/old", RootMethod: domain.RootMethodRequest, URLMode: domain.URLModeStatic,
		TargetURL: "https://example.com/other", TeamID: env.team,
	}
	require.NoError(t, env.nodeUC.Create(ctx, env.actor("alice"), other))

	p, err := env.uc.Preview(ctx, entry.ID)
	require.NoError(t, err)
	assert.NoError(t, p.DryRunErr, "пробный прогон не видит уникальность пути — её проверяет только запись")
	err = env.uc.Apply(ctx, env.actor("admin"), entry.ID, []string{"path"}, wireUpdatedAt(t, p))
	require.ErrorIs(t, err, domain.ErrNodeAlreadyExists)
	got, err := env.nodes.Get(ctx, n.ID)
	require.NoError(t, err)
	assert.Equal(t, "revert/new", got.Path, "узел не изменился")
	revs, err := env.audit.List(ctx, port.AuditFilter{TargetID: n.ID, Actions: []string{domain.ActionNodeRevert}})
	require.NoError(t, err)
	assert.Empty(t, revs, "строка отмены откатилась вместе с узлом")

	byIDs, err := env.nodes.GetByIDs(ctx, []string{n.ID, other.ID, uuid.NewString()})
	require.NoError(t, err)
	assert.Len(t, byIDs, 2)

	require.NoError(t, env.nodeUC.Delete(ctx, env.actor("admin"), n.ID, env.team))
	gp, err := env.uc.Preview(ctx, entry.ID)
	require.NoError(t, err)
	assert.Equal(t, usecase.RevertStateGone, gp.State)
	assert.Nil(t, gp.Node)
	err = env.uc.Apply(ctx, env.actor("admin"), entry.ID, []string{"path"}, time.Now())
	require.ErrorIs(t, err, domain.ErrNodeNotFound)
	assert.Equal(t, usecase.RevertStateGone, env.uc.Annotate(ctx, []*domain.AuditEntry{entry})[entry.ID].State)

	byIDs, err = env.nodes.GetByIDs(ctx, []string{n.ID, other.ID})
	require.NoError(t, err)
	assert.Len(t, byIDs, 1)
	assert.Contains(t, byIDs, other.ID)
}
