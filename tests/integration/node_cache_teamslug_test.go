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
	rnodecache "nexus/internal/receiver/adapter/out/nodecache"
	pgrepo "nexus/internal/web/adapter/out/postgres"
	webredis "nexus/internal/web/adapter/out/redis"
	"nexus/internal/web/usecase"
)

// TestNodeCache_TeamSlugKey_WebToReceiver_E2E — §50: Web пишет ключ кеша узла по
// РЕАЛЬНОМУ slug команды, и Receiver читает ИМЕННО его.
//
// Регрессия на боевой баг: Web всегда писал/инвалидировал node:default:<path>,
// а Receiver читает node:<team_slug>:<path>. Для узла вне команды default правки
// (target_url, статус, удаление) не доходили до Receiver до истечения Redis-TTL
// (до 5 мин) — узел продолжал ходить на старый адрес.
func TestNodeCache_TeamSlugKey_WebToReceiver_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool, pgCleanup := startPostgres(t, ctx)
	defer pgCleanup()
	rdb, redisCleanup := startRedis(t, ctx)
	defer redisCleanup()

	cipher, err := crypto.NewCipher(testEncryptionKey)
	require.NoError(t, err)
	logger := logging.NewNoop()

	teamRepo := pgrepo.NewTeamRepoPg(pool, logger)
	nodeRepo := pgrepo.NewNodeRepoPg(pool, cipher, logger)
	auditUC := usecase.NewAuditUsecase(pgrepo.NewAuditRepoPg(pool, logger), logger)
	uow := pgrepo.NewUnitOfWorkPg(pool, cipher, logger)
	nodeCache := webredis.NewNodeCacheRedis(rdb, cipher, logger)
	defaultTeam := resolveDefaultTeamID(t, ctx, pool)

	nodeUC := usecase.NewNodeUsecase(
		nodeRepo, nodeCache, auditUC, uow, teamRepo,
		nil, nil, time.Minute, 0, defaultTeam, nil, logger,
	)

	// Команда вне default.
	acme := &domain.Team{Slug: "acme", Name: "Acme", CHDatabase: domain.CHDatabaseForSlug("acme")}
	require.NoError(t, teamRepo.Create(ctx, acme))

	// Receiver-ридер на том же Redis+PG (читает ключ, который пишет Web).
	reader := rnodecache.New(rdb, pool, cipher, time.Minute, logger)

	// 1. Create через Web → Receiver видит узел по slug "acme", а не по default.
	node := &domain.Node{
		Path:            "billing/hook",
		RootMethod:      domain.RootMethodRequest,
		URLMode:         domain.URLModeStatic,
		TargetURL:       "http://old.example/hook",
		TeamID:          acme.ID,
		ClickHouseTable: "logs",
		AuthType:        domain.AuthTypeToken,
		AuthCredentials: "sekret",
	}
	require.NoError(t, nodeUC.Create(ctx, usecase.SystemActor(), node))
	require.NotEmpty(t, node.ID)

	got, err := reader.Get(ctx, "acme", "billing/hook")
	require.NoError(t, err)
	assert.Equal(t, "http://old.example/hook", got.TargetURL)
	assert.Equal(t, "sekret", got.AuthCredentials, "Receiver расшифровал креды из кеша")

	// Ключ именно node:acme:*, а node:default:* НЕ создан.
	require.Equal(t, int64(1), rdb.Exists(ctx, "node:acme:billing/hook").Val(),
		"Web должен писать ключ команды узла")
	require.Equal(t, int64(0), rdb.Exists(ctx, "node:default:billing/hook").Val(),
		"Web не должен писать в ключ чужой команды default")

	// 2. Update target через Web → Receiver СРАЗУ видит новый target (не stale).
	updated := *node
	updated.TargetURL = "http://new.example/hook"
	require.NoError(t, nodeUC.Update(ctx, usecase.SystemActor(), &updated, acme.ID))

	got, err = reader.Get(ctx, "acme", "billing/hook")
	require.NoError(t, err)
	assert.Equal(t, "http://new.example/hook", got.TargetURL,
		"правка target должна дойти до Receiver немедленно через ключ acme")

	// 3. Delete через Web → ключ команды инвалидирован, узла нет.
	require.NoError(t, nodeUC.Delete(ctx, usecase.SystemActor(), node.ID, acme.ID))
	require.Equal(t, int64(0), rdb.Exists(ctx, "node:acme:billing/hook").Val(),
		"Delete должен инвалидировать ключ команды")
	_, err = reader.Get(ctx, "acme", "billing/hook")
	require.ErrorIs(t, err, domain.ErrNodeNotFound)
}
