//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/domain/ackspec"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
	rnodecache "nexus/internal/receiver/adapter/out/nodecache"
	pgrepo "nexus/internal/web/adapter/out/postgres"
	webredis "nexus/internal/web/adapter/out/redis"
	"nexus/internal/web/usecase"
)

// TestNodeAck_WebToReceiver_E2E — §83: шаблон ответа, настроенный в Web,
// доезжает до Receiver'а и обновляется СРАЗУ, а не по истечении TTL.
//
// Что именно проверяется (и почему это не поймать юнитами):
//  1. Receiver читает узел своим урезанным SELECT — колонка в нём есть;
//  2. спека переживает Redis-кеш (узел кладётся туда JSON'ом целиком);
//  3. правка шаблона в Web видна Receiver'у на следующем же чтении — за это
//     отвечают write-through кеша и инвалидация §57, а не TTL (300 с на бою);
//  4. выключение шаблона так же мгновенно возвращает прежний ответ.
func TestNodeAck_WebToReceiver_E2E(t *testing.T) {
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

	// Длинный TTL намеренно: если бы обновление держалось на протухании кеша,
	// тест бы этого не заметил. Здесь TTL заведомо переживает весь прогон.
	const cacheTTL = time.Hour
	nodeUC := usecase.NewNodeUsecase(
		nodeRepo, nodeCache, auditUC, uow, teamRepo,
		nil, nil, cacheTTL, 0, defaultTeam, nil, logger,
	)
	reader := rnodecache.New(rdb, pool, cipher, cacheTTL, logger)

	// 1. Узел без шаблона: Receiver отвечает как раньше.
	node := &domain.Node{
		Path:       "acs_sigur",
		RootMethod: domain.RootMethodRequestAsync,
		URLMode:    domain.URLModeStatic,
		TargetURL:  "http://receiver.example/hook",
		TeamID:     defaultTeam,
	}
	require.NoError(t, nodeUC.Create(ctx, usecase.SystemActor(), node))

	got, err := reader.Get(ctx, domain.DefaultTeamSlug, "acs_sigur")
	require.NoError(t, err)
	assert.Nil(t, got.AsyncAck, "узел без спеки — прежний ответ шины")

	// 2. Включаем шаблон в Web → Receiver обязан увидеть его немедленно.
	withSpec := *node
	withSpec.AsyncAck = &ackspec.Spec{
		Version:     ackspec.Version,
		ContentType: ackspec.ContentTypeJSON,
		Body:        `{"confirmedLogId": "${ body.logs[*].logId | max }"}`,
		OnError:     ackspec.OnErrorDefault,
	}
	require.NoError(t, nodeUC.Update(ctx, usecase.SystemActor(), &withSpec, defaultTeam))

	got, err = reader.Get(ctx, domain.DefaultTeamSlug, "acs_sigur")
	require.NoError(t, err)
	require.NotNil(t, got.AsyncAck, "шаблон не доехал до Receiver'а")
	assert.Equal(t, withSpec.AsyncAck.Body, got.AsyncAck.Body)
	assert.Equal(t, ackspec.ContentTypeJSON, got.AsyncAck.ContentType)

	// Шаблон должен быть не просто прочитан, а РАБОТОСПОСОБЕН на той стороне:
	// компилируется и даёт ожидаемый ответ на боевом теле.
	tmpl, err := ackspec.Compile(got.AsyncAck.Body, got.AsyncAck.ContentType)
	require.NoError(t, err)
	out, err := tmpl.Render(&ackspec.Ctx{
		Body:        []byte(`{"logs":[{"logId":79154},{"logId":79000}]}`),
		ContentType: "application/json",
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{"confirmedLogId": 79154}`, string(out))

	// 3. Правка шаблона — тоже сразу, без ожидания TTL.
	edited := withSpec
	spec := *withSpec.AsyncAck
	spec.Body = `{"confirmedLogId": "${ body.logs[*].logId | max }", "accepted": "${ body.logs[*].logId | count }"}`
	edited.AsyncAck = &spec
	require.NoError(t, nodeUC.Update(ctx, usecase.SystemActor(), &edited, defaultTeam))

	got, err = reader.Get(ctx, domain.DefaultTeamSlug, "acs_sigur")
	require.NoError(t, err)
	require.NotNil(t, got.AsyncAck)
	assert.Contains(t, got.AsyncAck.Body, "accepted",
		"правка шаблона обязана быть видна сразу, а не после истечения кеша")

	// 4. Выключение — так же мгновенно.
	off := edited
	off.AsyncAck = nil
	require.NoError(t, nodeUC.Update(ctx, usecase.SystemActor(), &off, defaultTeam))

	got, err = reader.Get(ctx, domain.DefaultTeamSlug, "acs_sigur")
	require.NoError(t, err)
	assert.Nil(t, got.AsyncAck, "выключение шаблона обязано быть видно сразу")
}
