//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/clickhouse"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
	webch "nexus/internal/web/adapter/out/clickhouse"
	pgrepo "nexus/internal/web/adapter/out/postgres"
	"nexus/internal/web/usecase"
)

// TestNodeUC_CreateWithTemplate_E2E (§19, Phase F1.5): создание узла с шаблоном
// через NodeUsecase на реальных PG+CH автоматически создаёт таблицу логов в БД
// команды. Покрывает normalizeCHTable → provisionTable → CreateTable + загрузку
// сид-шаблона «Standard logs».
func TestNodeUC_CreateWithTemplate_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	pool, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()
	conn, _, cleanupCH := startClickHouse(t, ctx)
	defer cleanupCH()

	// БД default-команды должна существовать в CH (в проде её создаёт TeamProvisioner).
	require.NoError(t, conn.Exec(ctx, "CREATE DATABASE IF NOT EXISTS nexus_default"))

	logger := logging.NewNoop()
	cipher, err := crypto.NewCipher(testEncryptionKey)
	require.NoError(t, err)

	provider := clickhouse.StaticProvider(conn)
	provisioner := webch.NewTeamProvisioner(provider, logger)

	nodeRepo := pgrepo.NewNodeRepoPg(pool, cipher, logger)
	chTemplateRepo := pgrepo.NewCHTemplateRepoPg(pool, logger)
	auditRepo := pgrepo.NewAuditRepoPg(pool, logger)
	uow := pgrepo.NewUnitOfWorkPg(pool, cipher, logger)
	auditUC := usecase.NewAuditUsecase(auditRepo, logger)
	teamRepo := pgrepo.NewTeamRepoPg(pool, logger)
	defaultTeam := resolveDefaultTeamID(t, ctx, pool)

	def, err := chTemplateRepo.GetDefault(ctx)
	require.NoError(t, err)

	nodeUC := usecase.NewNodeUsecase(
		nodeRepo, nopCache{}, auditUC, uow, teamRepo, provisioner, chTemplateRepo,
		time.Minute, 0, defaultTeam, logger,
	)

	n := &domain.Node{
		Path:                 "svc/tmpl",
		RootMethod:           domain.RootMethodRequest,
		URLMode:              domain.URLModeStatic,
		TargetURL:            "https://example.com/hook",
		ClickHouseTable:      "logs_e2e", // без БД — normalizeCHTable добавит nexus_default.
		ClickHouseTemplateID: def.ID,
	}
	require.NoError(t, nodeUC.Create(ctx, usecase.SystemActor(), n))
	require.Equal(t, "nexus_default.logs_e2e", n.ClickHouseTable)

	// Таблица должна существовать в ClickHouse.
	var cnt uint64
	require.NoError(t, conn.QueryRow(ctx,
		"SELECT count() FROM system.tables WHERE database = 'nexus_default' AND name = 'logs_e2e'").Scan(&cnt))
	require.EqualValues(t, 1, cnt, "log table must be auto-created from template")
}

// TestNodeUC_CreateNoTemplate_DefaultProvision_E2E (Фикс #7): узел БЕЗ
// явного template_id, но с включённым логированием, должен авто-создать
// таблицу логов из дефолтного шаблона каталога. Иначе чтение метрик/логов
// падает с CH code 60 «Unknown table» (баг тестового стенда —
// nexus_default.stand_async_noauth не существует). Проверяем, что после Create
// таблица есть и NodeKPI по ней не падает (пустая → нули).
func TestNodeUC_CreateNoTemplate_DefaultProvision_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	pool, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()
	conn, _, cleanupCH := startClickHouse(t, ctx)
	defer cleanupCH()

	require.NoError(t, conn.Exec(ctx, "CREATE DATABASE IF NOT EXISTS nexus_default"))

	logger := logging.NewNoop()
	cipher, err := crypto.NewCipher(testEncryptionKey)
	require.NoError(t, err)

	provider := clickhouse.StaticProvider(conn)
	provisioner := webch.NewTeamProvisioner(provider, logger)
	metricsReader := webch.NewMetricsReader(provider, logger)

	nodeRepo := pgrepo.NewNodeRepoPg(pool, cipher, logger)
	chTemplateRepo := pgrepo.NewCHTemplateRepoPg(pool, logger)
	auditRepo := pgrepo.NewAuditRepoPg(pool, logger)
	uow := pgrepo.NewUnitOfWorkPg(pool, cipher, logger)
	auditUC := usecase.NewAuditUsecase(auditRepo, logger)
	teamRepo := pgrepo.NewTeamRepoPg(pool, logger)
	defaultTeam := resolveDefaultTeamID(t, ctx, pool)

	nodeUC := usecase.NewNodeUsecase(
		nodeRepo, nopCache{}, auditUC, uow, teamRepo, provisioner, chTemplateRepo,
		time.Minute, 0, defaultTeam, logger,
	)

	// Узел как со стенда: clickhouse_table без шаблона, логирование включено.
	n := &domain.Node{
		Path:            "stand/async-noauth",
		RootMethod:      domain.RootMethodRequestAsync,
		URLMode:         domain.URLModeStatic,
		TargetURL:       "https://example.com/hook",
		ClickHouseTable: "stand_async_noauth", // normalizeCHTable → nexus_default.
		LoggingEnabled:  true,
	}
	require.NoError(t, nodeUC.Create(ctx, usecase.SystemActor(), n))
	require.Equal(t, "nexus_default.stand_async_noauth", n.ClickHouseTable)

	var cnt uint64
	require.NoError(t, conn.QueryRow(ctx,
		"SELECT count() FROM system.tables WHERE database = 'nexus_default' AND name = 'stand_async_noauth'").Scan(&cnt))
	require.EqualValues(t, 1, cnt, "log table must be auto-created from default template")

	// Чтение KPI по только что созданной таблице не должно падать (CH code 60).
	kpi, err := metricsReader.NodeKPI(ctx, n.ClickHouseTable, 0, 0)
	require.NoError(t, err, "NodeKPI over provisioned table must not fail")
	assert.Zero(t, kpi.Total, "fresh table is empty")
}
