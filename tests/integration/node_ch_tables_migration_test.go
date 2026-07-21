//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
	pgrepo "nexus/internal/web/adapter/out/postgres"
	"nexus/internal/web/usecase/port"
)

// TestNodeRepo_ListClickHouseTables_AllTeams_E2E — стартовая миграция CH-схемы
// Web'а (§37/§39/§42-доп) обязана видеть таблицы ВСЕХ команд, а не только
// default.
//
// Регрессия боевого инцидента (Sentry 158619): Web собирал список таблиц через
// List(TeamID: defaultTeamID), поэтому БД не-default команд (`nexus_vika.*` —
// там весь боевой трафик) не альтерились вовсе, и чтение логов падало с CH
// code 47 «Unknown expression identifier request_size», пока таблицу не
// доальтерит рестарт Sender'а (тот всегда ходил по всем командам).
//
// На старом коде тест падает: таблица команды acme в списке отсутствовала.
func TestNodeRepo_ListClickHouseTables_AllTeams_E2E(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	cipher, err := crypto.NewCipher(testEncryptionKey)
	require.NoError(t, err)
	logger := logging.NewNoop()

	teamRepo := pgrepo.NewTeamRepoPg(pool, logger)
	nodeRepo := pgrepo.NewNodeRepoPg(pool, cipher, logger)
	defaultTeamID := resolveDefaultTeamID(t, ctx, pool)

	// Команда вне default — как `vika` на бою.
	acme := &domain.Team{Slug: "acme", Name: "Acme", CHDatabase: domain.CHDatabaseForSlug("acme")}
	require.NoError(t, teamRepo.Create(ctx, acme))

	mkNode := func(teamID, path, table string, external bool) *domain.Node {
		n := &domain.Node{
			ExternalTable:   external,
			Path:            path,
			RootMethod:      domain.RootMethodRequest,
			URLMode:         domain.URLModeStatic,
			TargetURL:       "https://example.com/" + path,
			IncomingMethod:  "POST",
			OutgoingMethod:  "POST",
			Status:          domain.NodeStatusEnabled,
			TeamID:          teamID,
			ClickHouseTable: table,
			TimeoutMs:       1000,
			CreatedAt:       time.Now(),
			UpdatedAt:       time.Now(),
		}
		n.SetDefaults()
		n.ClickHouseTable = table // SetDefaults не должен перетереть явное имя
		require.NoError(t, nodeRepo.Create(ctx, n))
		return n
	}

	mkNode(defaultTeamID, "svc/in-default", "nexus_default.in_default", false)
	mkNode(acme.ID, "svc/in-acme", "nexus_acme.in_acme", false)
	mkNode(acme.ID, "svc/in-acme-2", "nexus_acme.in_acme", false) // та же таблица → дедуп DISTINCT
	// §64: внешняя таблица не мигрируется — её схему ведёт оператор.
	mkNode(defaultTeamID, "svc/external", "external_db.audit_log", true)
	// §64: таблицу делят внешний и обычный узел — она остаётся в списке, потому
	// что вторым узлом всё равно управляет Nexus.
	mkNode(defaultTeamID, "svc/mixed-ext", "nexus_default.mixed", true)
	mkNode(defaultTeamID, "svc/mixed-own", "nexus_default.mixed", false)

	tables, err := nodeRepo.ListClickHouseTables(ctx)
	require.NoError(t, err)

	require.Contains(t, tables, "nexus_default.in_default")
	require.Contains(t, tables, "nexus_acme.in_acme",
		"таблица не-default команды обязана попасть в стартовую миграцию — иначе SELECT новых колонок упадёт с CH code 47")
	require.NotContains(t, tables, "external_db.audit_log",
		"§64: внешняя таблица не должна получать ALTER'ы Nexus'а")
	require.Contains(t, tables, "nexus_default.mixed",
		"§64: общая таблица остаётся в списке, если на неё ссылается хотя бы один не-внешний узел")
	require.Len(t, tables, 3, "DISTINCT: одна таблица на два узла не дублируется")

	// Контроль: старый путь (фильтр по команде) действительно видит только default —
	// именно поэтому список таблиц для миграции нельзя брать через List(TeamID).
	def, err := nodeRepo.List(ctx, port.ListNodesFilter{TeamID: defaultTeamID})
	require.NoError(t, err)
	for _, n := range def {
		require.NotEqual(t, "nexus_acme.in_acme", n.ClickHouseTable,
			"листинг по команде не видит таблиц чужой команды — поэтому он не годится как источник для миграции")
	}
	require.Len(t, def, 4, "все узлы default-команды, включая внешние")
}
