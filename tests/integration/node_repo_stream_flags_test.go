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

// TestNodeRepoStreamFlags_E2E — §107: оба флага узла переживают создание и
// правку через Web (миграция 0043 + INSERT/UPDATE/scan), а смена типа узла на
// requestAsync сбрасывает отказ от SSE-подписки, не трогая флаг таймаутов.
func TestNodeRepoStreamFlags_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	cipher, err := crypto.NewCipher(testEncryptionKey)
	require.NoError(t, err)
	logger := logging.NewNoop()
	nodeRepo := pgrepo.NewNodeRepoPg(pool, cipher, logger)
	auditUC := usecase.NewAuditUsecase(pgrepo.NewAuditRepoPg(pool, logger), logger)
	uow := pgrepo.NewUnitOfWorkPg(pool, cipher, logger)
	defaultTeam := resolveDefaultTeamID(t, ctx, pool)
	nodeUC := usecase.NewNodeUsecase(nodeRepo, nopCache{}, auditUC, uow,
		pgrepo.NewTeamRepoPg(pool, logger), nil, nil, time.Minute, 0, defaultTeam, nil, logger)

	// Узел без флагов — прежнее поведение (DEFAULT false в миграции).
	n := &domain.Node{
		Path:       "mcp-1c/dev",
		RootMethod: domain.RootMethodRequest,
		URLMode:    domain.URLModeStatic,
		TargetURL:  "http://mcp.example:8172/mcp",
	}
	require.NoError(t, nodeUC.Create(ctx, usecase.SystemActor(), n))
	got, err := nodeRepo.Get(ctx, n.ID)
	require.NoError(t, err)
	assert.False(t, got.SSEDecline)
	assert.False(t, got.BreakerIgnoreTimeouts)

	// Включение обоих флагов правкой.
	on := *got
	on.SSEDecline, on.BreakerIgnoreTimeouts = true, true
	require.NoError(t, nodeUC.Update(ctx, usecase.SystemActor(), &on, defaultTeam))
	got, err = nodeRepo.Get(ctx, n.ID)
	require.NoError(t, err)
	assert.True(t, got.SSEDecline, "sse_decline не сохранился")
	assert.True(t, got.BreakerIgnoreTimeouts, "breaker_ignore_timeouts не сохранился")

	// Перевод в requestAsync: подписка у async-узла на входе не висит — флаг
	// SSE сбрасывается нормализацией, флаг таймаутов остаётся (брейкер общий).
	async := *got
	async.RootMethod = domain.RootMethodRequestAsync
	require.NoError(t, nodeUC.Update(ctx, usecase.SystemActor(), &async, defaultTeam))
	got, err = nodeRepo.Get(ctx, n.ID)
	require.NoError(t, err)
	assert.False(t, got.SSEDecline, "у requestAsync флаг SSE обязан сброситься")
	assert.True(t, got.BreakerIgnoreTimeouts, "флаг таймаутов применим ко всем типам")
}
