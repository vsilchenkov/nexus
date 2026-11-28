//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/queuecancel"
	"nexus/internal/web/adapter/out/kafkaadmin"
	pgrepo "nexus/internal/web/adapter/out/postgres"
	webuc "nexus/internal/web/usecase"
)

// TestAsyncQueue_PausedBacklog_E2E (§35 + §3.6): бэклог узла на паузе лежит в
// delay-топике, а не в основном, — вкладка «Очередь» обязана его показывать и
// уметь чистить.
//
// Без этого теста регрессия была бы тихой: KPI «Ожидают отправки» показал бы 0,
// а «Очистить все ожидающие» отменила бы ноль сообщений, то есть отвалился бы
// главный сценарий §34.4 «очистить очередь мёртвого узла».
//
// §113.9: то же для sync-узла на паузе — его запросы Receiver ставит в ту же
// очередь с ответом 202. До §113 вкладка отвечала «у sync-узла очереди нет»:
// инцидент 06.10.2026, 960 невидимых и неочищаемых запросов.
func TestAsyncQueue_PausedBacklog_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	stack, rec, mock, pool := setupPausedStack(t, ctx, "nexus-sender-qpaused-it")
	redisClient, redisCleanup := startRedis(t, ctx)
	defer redisCleanup()

	// Sender должен видеть отмены оператора — иначе шаг 5 (отменённое не
	// доставляется) проверял бы не то.
	stack.cancel = queuecancel.New(redisClient)

	logger := logging.NewNoop()
	nodeUC := newAsyncNodeUsecase(t, ctx, pool, stack.cipher)
	createAsyncNode(t, ctx, nodeUC, "demo/qpaused", mock.URL+"/qpaused", "test.demo_qpaused", domain.NodeStatusPaused)
	createAsyncNode(t, ctx, nodeUC, "demo/qwarmup", mock.URL+"/qwarmup", "test.demo_qwarmup", domain.NodeStatusEnabled)
	createAsyncNode(t, ctx, nodeUC, "demo/qpaused-sync", mock.URL+"/qpaused-sync", "test.demo_qpaused_sync", domain.NodeStatusPaused)
	_, err := pool.Exec(ctx, "UPDATE nodes SET root_method = $1 WHERE path = $2",
		string(domain.RootMethodRequest), "demo/qpaused-sync")
	require.NoError(t, err)

	nodeRepo := pgrepo.NewNodeRepoPg(pool, stack.cipher, logger)
	created, err := nodeRepo.GetByPath(ctx, "demo/qpaused")
	require.NoError(t, err)
	syncNode, err := nodeRepo.GetByPath(ctx, "demo/qpaused-sync")
	require.NoError(t, err)
	require.Equal(t, domain.RootMethodRequest, syncNode.RootMethod)

	aqUC := webuc.NewAsyncQueueUsecase(
		kafkaadmin.New(stack.cfg.Kafka.Brokers, 5*time.Second, 0, logger),
		queuecancel.New(redisClient), nil, nodeRepo, nodeRepo,
		webuc.NewAuditUsecase(pgrepo.NewAuditRepoPg(pool, logger), logger),
		stack.cfg.Kafka.ConsumerGroup, stack.cfg.Kafka.AsyncTopic,
		stack.cfg.Kafka.PausedGroup(), stack.cfg.Kafka.PausedTopic,
		time.Hour, 1000, logger)

	// Consumer переносит сообщения paused-узла в delay-топик; sweeper пока не
	// запускаем — бэклог должен просто лежать и быть видимым в UI.
	stop := stack.startConsumer(ctx, stack.pausedRequeueOpt())
	defer stop()
	stack.warmupConsumer(t, ctx, rec, "demo/qwarmup", mock.URL+"/qwarmup", "/qwarmup")

	const backlog = 3
	for i := 1; i <= backlog; i++ {
		stack.produceEnvelope(t, ctx, fmt.Sprintf("qpaused-%d", i), "demo/qpaused",
			mock.URL+"/qpaused", fmt.Sprintf(`{"n":%d}`, i))
	}
	const syncBacklog = 2
	for i := 1; i <= syncBacklog; i++ {
		stack.produceEnvelope(t, ctx, fmt.Sprintf("qpaused-sync-%d", i), "demo/qpaused-sync",
			mock.URL+"/qpaused-sync", fmt.Sprintf(`{"s":%d}`, i))
	}

	// 1. Очередь видит бэклог, хотя в основном топике его уже нет.
	var list webuc.QueueListResult
	require.Eventually(t, func() bool {
		list, err = aqUC.List(ctx, created.ID, created.TeamID)
		return err == nil && len(list.Items) == backlog
	}, 90*time.Second, time.Second,
		"вкладка «Очередь» должна показывать бэклог из delay-топика")
	require.True(t, list.KafkaAvailable)
	require.Equal(t, stack.cfg.Kafka.PausedTopic, list.Items[0].Topic,
		"источник сообщения — delay-топик")

	// 2. Тело читается по координате вместе с топиком.
	body, err := aqUC.Body(ctx, created.ID, created.TeamID,
		list.Items[0].Topic, list.Items[0].Partition, list.Items[0].Offset)
	require.NoError(t, err)
	require.Equal(t, list.Items[0].ID, body.ID)

	// 3. Чужой топик читать нельзя (эндпоинт не универсальный ридер Kafka).
	_, err = aqUC.Body(ctx, created.ID, created.TeamID, "nexus.logs.retry", 0, 0)
	require.ErrorIs(t, err, webuc.ErrAsyncQueueUnknownTopic)

	// 3a. §113.9: очередь sync-узла на паузе видна и очищается.
	var syncList webuc.QueueListResult
	require.Eventually(t, func() bool {
		syncList, err = aqUC.List(ctx, syncNode.ID, syncNode.TeamID)
		return err == nil && len(syncList.Items) == syncBacklog
	}, 90*time.Second, time.Second,
		"вкладка «Очередь» sync-узла на паузе должна показывать его запросы")
	syncPurged, err := aqUC.PurgeAll(ctx, webuc.SystemActor(), syncNode.ID, syncNode.TeamID)
	require.NoError(t, err)
	require.Equal(t, syncBacklog, syncPurged.Cancelled, "purge sync-узла на паузе находит его запросы")

	// 4. «Очистить все ожидающие» отменяет весь бэклог (tombstones в Redis).
	purged, err := aqUC.PurgeAll(ctx, webuc.SystemActor(), created.ID, created.TeamID)
	require.NoError(t, err)
	require.Equal(t, backlog, purged.Cancelled,
		"purge обязан находить сообщения в delay-топике")

	// 5. Отменённые не доставляются даже после снятия паузы: sweeper их дропает.
	setNodeStatus(t, ctx, pool, "demo/qpaused", domain.NodeStatusEnabled)
	stopSweep := stack.startPausedSweeper(t, ctx, time.Second)
	defer stopSweep()

	time.Sleep(10 * time.Second)
	require.Zero(t, rec.count("/qpaused"), "отменённые сообщения во внешний узел не уходят")
}
