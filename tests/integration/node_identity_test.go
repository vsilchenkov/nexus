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
	"nexus/internal/receiver/adapter/out/rabbitmq"
	"nexus/internal/sender/adapter/out/nodepg"
	pgrepo "nexus/internal/web/adapter/out/postgres"
	"nexus/internal/web/usecase"
)

// identityFixture — PostgreSQL с командами и узлами, созданными через Web-usecase
// (§101: одноимённые узлы разных команд).
type identityFixture struct {
	pool   *pgxpool.Pool
	cipher *crypto.Cipher
	logger logging.Logger
	mkTeam func(slug string) *domain.Team
	mkNode func(n *domain.Node) *domain.Node
}

func newIdentityFixture(t *testing.T, ctx context.Context) (*identityFixture, func()) {
	t.Helper()
	pool, cleanup := startPostgres(t, ctx)
	cipher, err := crypto.NewCipher(testEncryptionKey)
	require.NoError(t, err)
	logger := logging.NewNoop()

	teamRepo := pgrepo.NewTeamRepoPg(pool, logger)
	nodeRepo := pgrepo.NewNodeRepoPg(pool, cipher, logger)
	auditUC := usecase.NewAuditUsecase(pgrepo.NewAuditRepoPg(pool, logger), logger)
	uow := pgrepo.NewUnitOfWorkPg(pool, cipher, logger)
	nodeUC := usecase.NewNodeUsecase(nodeRepo, nopCache{}, auditUC, uow, teamRepo,
		nil, nil, time.Minute, 0, resolveDefaultTeamID(t, ctx, pool), nil, logger)

	return &identityFixture{
		pool: pool, cipher: cipher, logger: logger,
		mkTeam: func(slug string) *domain.Team {
			tm := &domain.Team{Slug: slug, Name: slug, CHDatabase: domain.CHDatabaseForSlug(slug)}
			require.NoError(t, teamRepo.Create(ctx, tm))
			return tm
		},
		mkNode: func(n *domain.Node) *domain.Node {
			require.NoError(t, nodeUC.Create(ctx, usecase.SystemActor(), n))
			return n
		},
	}, cleanup
}

// TestSender_NodeReaderSameNamePaths (§101.5) — ридер Sender'а на живом
// PostgreSQL. До §101 он искал узел `WHERE path = $1` без команды и без
// ORDER BY, то есть отдавал случайный из одноимённых узлов.
//
// Совместимость: конструкций PostgreSQL 13+ здесь нет (на бою 12).
func TestSender_NodeReaderSameNamePaths(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	fx, cleanup := newIdentityFixture(t, ctx)
	defer cleanup()

	mk := func(team *domain.Team, target, table string) *domain.Node {
		return fx.mkNode(&domain.Node{
			Path:            "telegram",
			RootMethod:      domain.RootMethodRequestAsync,
			URLMode:         domain.URLModeStatic,
			TargetURL:       target,
			TeamID:          team.ID,
			ClickHouseTable: table,
		})
	}
	messengers := mk(fx.mkTeam("messengers"), "https://messengers.example.test/bot", "telegram_messengers")
	webhook := mk(fx.mkTeam("webhook"), "https://webhook.example.test/hook", "telegram_webhook")

	reader := nodepg.New(fx.pool, fx.cipher, fx.logger)

	got, err := reader.GetByID(ctx, webhook.ID)
	require.NoError(t, err)
	assert.Equal(t, webhook.ID, got.ID)
	assert.Equal(t, "webhook", got.TeamSlug, "слог приходит из teams — по нему Sender ставит метку team")
	assert.Equal(t, webhook.TargetURL, got.TargetURL)
	// Create дописывает к таблице базу команды — у одноимённых узлов и таблицы разные.
	assert.Equal(t, domain.CHDatabaseForSlug("webhook")+".telegram_webhook", got.ClickHouseTable)

	_, err = reader.GetByID(ctx, "9a8b7c6d-5e4f-4a3b-8c2d-000000000009")
	assert.ErrorIs(t, err, domain.ErrNodeNotFound)

	list, err := reader.ListByPath(ctx, "telegram")
	require.NoError(t, err)
	require.Len(t, list, 2, "путь в двух командах — два узла, а не случайный из них")
	assert.Equal(t, messengers.ID, list[0].ID)
	assert.Equal(t, "messengers", list[0].TeamSlug)
	assert.Equal(t, webhook.ID, list[1].ID)

	// Испорченные креды узла третьей команды не мешают увидеть неоднозначность:
	// несколько узлов конверт не исполняют, и их креды не расшифровываются. Иначе
	// чужая порча давала бы ошибку чтения — и вечный повтор вместо DLQ.
	const brokenCreds = `UPDATE nodes SET auth_credentials = 'v1:not:base64:!' WHERE id = $1`
	brokenTeam := fx.mkTeam("broken")
	broken := mk(brokenTeam, "https://broken.example.test/x", "telegram_broken")
	_, err = fx.pool.Exec(ctx, brokenCreds, broken.ID)
	require.NoError(t, err)
	list, err = reader.ListByPath(ctx, "telegram")
	require.NoError(t, err, "креды нескольких узлов не расшифровываются")
	assert.Len(t, list, 3)
	_, err = reader.GetByID(ctx, broken.ID)
	require.Error(t, err, "узел по id по-прежнему расшифровывается")

	solo := fx.mkNode(&domain.Node{
		Path: "solo-broken", RootMethod: domain.RootMethodRequestAsync, URLMode: domain.URLModeStatic,
		TargetURL: "https://solo.example.test/x", TeamID: brokenTeam.ID, ClickHouseTable: "solo_broken",
	})
	_, err = fx.pool.Exec(ctx, brokenCreds, solo.ID)
	require.NoError(t, err)
	_, err = reader.ListByPath(ctx, "solo-broken")
	require.Error(t, err, "единственный узел пути расшифровывается — ему конверт и доставляется")

	none, err := reader.ListByPath(ctx, "never-existed")
	require.NoError(t, err)
	assert.Empty(t, none)
}

// TestRMQPuller_NodeListerTeamSlug (§101.7) — NodeLister отдаёт слог команды
// pull-узла: без него конверты одноимённых pull-узлов разных команд неразличимы.
// Заодно держит порядок колонок: слог добавлен в выборку, и сдвиг Scan испортил
// бы расшифровку пароля RabbitMQ.
func TestRMQPuller_NodeListerTeamSlug(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	fx, cleanup := newIdentityFixture(t, ctx)
	defer cleanup()

	mk := func(team *domain.Team) *domain.Node {
		return fx.mkNode(&domain.Node{
			Path:        "billing-events",
			RootMethod:  domain.RootMethodRabbitMQAsync,
			TargetURL:   "https://api.partner.test/billing/webhook",
			TeamID:      team.ID,
			RMQHost:     "rmq.internal",
			RMQQueue:    "billing.events." + team.Slug,
			RMQUser:     "nexus",
			RMQPassword: "s3cr3t-" + team.Slug,
		})
	}
	a := mk(fx.mkTeam("billing_a"))
	b := mk(fx.mkTeam("billing_b"))

	nodes, err := rabbitmq.NewNodeLister(fx.pool, fx.cipher, fx.logger).ListRabbitMQAsync(ctx)
	require.NoError(t, err)

	byID := make(map[string]*domain.Node, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}
	require.Contains(t, byID, a.ID)
	require.Contains(t, byID, b.ID)
	assert.Equal(t, "billing_a", byID[a.ID].TeamSlug)
	assert.Equal(t, "billing_b", byID[b.ID].TeamSlug)
	assert.Equal(t, "s3cr3t-billing_b", byID[b.ID].RMQPassword)
}
