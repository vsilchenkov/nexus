//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/crypto"
	kafkapf "nexus/internal/platform/kafka"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/queuecancel"
	rcv "nexus/internal/receiver/usecase"
	kafkaadapter "nexus/internal/sender/adapter/in/kafka"
	"nexus/internal/sender/adapter/out/httpclient"
	"nexus/internal/sender/adapter/out/nodepg"
	senderuc "nexus/internal/sender/usecase"
	"nexus/internal/web/adapter/out/kafkaadmin"
	pgrepo "nexus/internal/web/adapter/out/postgres"
	webuc "nexus/internal/web/usecase"
)

// TestAsyncQueueMgmt_E2E (§35): сквозной сценарий управления async-очередью на
// реальных Postgres+Kafka+Redis — накопление сообщений, peek (list/body),
// team-scope, purge по периоду и целиком (tombstones в Redis) и пропуск
// отменённых Sender'ом (НЕ доставляются на dead-target, очередь дренирует).
// Кодифицирует ручную стенд-проверку §35; косвенно покрывает peek-MaxWait
// (без короткого MaxWait каждый List/Body висел бы ~MaxWait у конца очереди).
func TestAsyncQueueMgmt_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	pool, pgCleanup := startPostgres(t, ctx)
	defer pgCleanup()
	brokers, kafkaCleanup := startKafka(t, ctx)
	defer kafkaCleanup()
	redisClient, redisCleanup := startRedis(t, ctx)
	defer redisCleanup()

	logger := logging.NewNoop()
	cipher, _ := crypto.NewCipher(testEncryptionKey)

	// mock upstream — считает доставки. Должно остаться 0: отменённые не шлём.
	var hits int32
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer mock.Close()

	nodeRepo := pgrepo.NewNodeRepoPg(pool, cipher, logger)
	auditRepo := pgrepo.NewAuditRepoPg(pool, logger)
	uow := pgrepo.NewUnitOfWorkPg(pool, cipher, logger)
	auditUC := webuc.NewAuditUsecase(auditRepo, logger)
	defaultTeam := resolveDefaultTeamID(t, ctx, pool)
	teamRepo := pgrepo.NewTeamRepoPg(pool, logger)
	nodeUC := webuc.NewNodeUsecase(nodeRepo, nopCache{}, auditUC, uow, teamRepo, nil, nil, time.Minute, 0, defaultTeam, nil, logger)

	const nodePath = "demo/qmgmt"
	require.NoError(t, nodeUC.Create(ctx, webuc.SystemActor(), &domain.Node{
		Path:             nodePath,
		RootMethod:       domain.RootMethodRequestAsync,
		URLMode:          domain.URLModeStatic,
		TargetURL:        mock.URL + "/up",
		AuthType:         domain.AuthTypeNone,
		IncomingAuthType: domain.IncomingAuthTypeNone,
		Status:           domain.NodeStatusEnabled,
		LoggingEnabled:   false,
		TimeoutMs:        2000,
		RetryCount:       0,
		RetryBackoffMs:   10,
	}))
	created, err := nodeRepo.GetByPath(ctx, nodePath)
	require.NoError(t, err)
	nodeID, teamID := created.ID, created.TeamID

	cfg := newKafkaTestConfig(brokers)
	cfg.Kafka.ConsumerGroup = "nexus-sender-qmgmt-it"
	require.NoError(t, kafkapf.EnsureTopics(ctx, cfg, logger, cfg.Kafka.AsyncTopic, cfg.Kafka.DLQTopic))

	producer := kafkapf.NewProducer(cfg)
	defer producer.Close()

	// Публикуем 3 сообщения (producer ставит key=node.Path). Consumer пока НЕ
	// запущен → сообщения копятся в очереди (как на paused-узле).
	routeAsync := rcv.NewRouteAsyncUsecase(&fakeReader{repo: nodeRepo}, producer, cfg.Kafka.AsyncTopic, 5, logger)
	ids := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		res, rErr := routeAsync.RouteAsync(ctx, rcv.RouteInput{
			NodePath: nodePath,
			Method:   "POST",
			Header:   http.Header{"Content-Type": []string{"application/json"}},
			Query:    url.Values{},
			Body:     []byte(fmt.Sprintf(`{"n":%d}`, i)),
			ClientIP: "127.0.0.1",
		})
		require.NoError(t, rErr)
		ids = append(ids, res.ID)
	}

	peeker := kafkaadmin.New(brokers, 5*time.Second, 0, logger)
	cancelSet := queuecancel.New(redisClient)
	aqUC := webuc.NewAsyncQueueUsecase(peeker, cancelSet, nil, nodeRepo, auditUC,
		cfg.Kafka.ConsumerGroup, cfg.Kafka.AsyncTopic,
		cfg.Kafka.PausedGroup(), cfg.Kafka.PausedTopic,
		time.Hour, 1000, logger)

	// 1. List показывает 3 ожидающих (Kafka eventual — poll).
	var list webuc.QueueListResult
	require.Eventually(t, func() bool {
		list, err = aqUC.List(ctx, nodeID, teamID)
		return err == nil && len(list.Items) == 3
	}, 30*time.Second, time.Second, "expected 3 pending messages in live queue")
	require.True(t, list.KafkaAvailable)

	// 2. Body одного сообщения — совпадает по id и содержит payload.
	body, err := aqUC.Body(ctx, nodeID, teamID, list.Items[0].Topic, list.Items[0].Partition, list.Items[0].Offset)
	require.NoError(t, err)
	require.Equal(t, list.Items[0].ID, body.ID)
	require.Contains(t, string(body.Body), `"n":`)

	// 3. team-scope: чужая команда → 404 (как в replay).
	_, err = aqUC.List(ctx, nodeID, "other-team")
	require.ErrorIs(t, err, domain.ErrNodeNotFound)

	// 4. PurgePeriod с окном в будущем — ничего не отменяет (ScanIDs по ReceivedAt).
	future := time.Now().Add(time.Hour)
	pp, err := aqUC.PurgePeriod(ctx, webuc.SystemActor(), nodeID, teamID, future, future.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, 0, pp.Cancelled, "future window must cancel nothing")

	// 5. PurgeAll — отменяет все 3 (tombstones в Redis).
	pa, err := aqUC.PurgeAll(ctx, webuc.SystemActor(), nodeID, teamID)
	require.NoError(t, err)
	require.Equal(t, 3, pa.Cancelled)
	for _, id := range ids {
		c, cErr := cancelSet.IsCancelled(ctx, id)
		require.NoError(t, cErr)
		require.True(t, c, "id %s must be tombstoned", id)
	}

	// 6. Sender consumer: отменённые ПРОПУСКАЮТСЯ (0 доставок на upstream),
	// очередь дренирует (committed догоняет high → List пуст).
	nodeReader := nodepg.New(pool, cipher, logger)
	httpc := httpclient.New(&cfg.Sender.HTTPClient, logger, 64<<20)
	sendUC := senderuc.NewSendUsecase(httpc, &capturingLogWriter{}, nil, logger, 64<<20)
	asyncProc := senderuc.NewAsyncProcessor(nodeReader, sendUC, producer, cancelSet, nil, cfg.Kafka.DLQTopic, nil, logger)
	consumer := kafkaadapter.NewConsumerGroup(cfg, cfg.Kafka.AsyncTopic, asyncProc, logger)
	consumer.Start(ctx)
	defer consumer.Stop()

	require.Eventually(t, func() bool {
		l, e := aqUC.List(ctx, nodeID, teamID)
		return e == nil && len(l.Items) == 0
	}, 60*time.Second, time.Second, "live queue must drain after cancelled messages consumed")
	require.EqualValues(t, 0, atomic.LoadInt32(&hits), "cancelled messages must not be delivered to upstream")
}
