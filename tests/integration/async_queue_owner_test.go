//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	kafkapf "nexus/internal/platform/kafka"
	"nexus/internal/platform/queuecancel"
	rcv "nexus/internal/receiver/usecase"
	"nexus/internal/web/adapter/out/kafkaadmin"
	pgrepo "nexus/internal/web/adapter/out/postgres"
	webuc "nexus/internal/web/usecase"
)

// TestAsyncQueue_SameNamePathsOwnership (§101.8) — «Очередь» на живых
// PostgreSQL, Kafka и Redis. Узлы `telegram` двух команд пишут в топик с
// одинаковым ключом; до §101 список и очистка брали сообщения обоих, а тело
// читалось по координатам без сверки владельца.
func TestAsyncQueue_SameNamePathsOwnership(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	fx, cleanup := newIdentityFixture(t, ctx)
	defer cleanup()
	brokers, kafkaCleanup := startKafka(t, ctx)
	defer kafkaCleanup()
	redisClient, redisCleanup := startRedis(t, ctx)
	defer redisCleanup()

	mkAsync := func(team *domain.Team, path string) *domain.Node {
		return fx.mkNode(&domain.Node{
			Path:             path,
			RootMethod:       domain.RootMethodRequestAsync,
			URLMode:          domain.URLModeStatic,
			TargetURL:        "https://example.test/hook",
			AuthType:         domain.AuthTypeNone,
			IncomingAuthType: domain.IncomingAuthTypeNone,
			Status:           domain.NodeStatusEnabled,
			TimeoutMs:        2000,
			TeamID:           team.ID,
		})
	}
	messengers := fx.mkTeam("messengers")
	nodeA := mkAsync(messengers, "telegram")
	nodeB := mkAsync(fx.mkTeam("webhook"), "telegram")
	solo := mkAsync(messengers, "solo")

	cfg := newKafkaTestConfig(brokers)
	cfg.Kafka.ConsumerGroup = "nexus-sender-qowner-it"
	require.NoError(t, kafkapf.EnsureTopics(ctx, cfg, fx.logger, cfg.Kafka.AsyncTopic))
	producer := kafkapf.NewProducer(cfg)
	defer producer.Close()

	// Конверты пишутся напрямую: старый конверт без node_id Receiver после §101
	// уже не производит. Ключ — путь, как у продюсера Receiver'а.
	publish := func(path, id, nodeID string) {
		raw, err := json.Marshal(rcv.Envelope{
			ID: id, NodePath: path, NodeID: nodeID, Method: "POST",
			TargetURL: "https://example.test/hook", Body: []byte(`{"id":"` + id + `"}`),
			ReceivedAt: time.Now().UTC(),
		})
		require.NoError(t, err)
		require.NoError(t, producer.Produce(ctx, cfg.Kafka.AsyncTopic, path, raw, nil))
	}
	publish("telegram", "msg-a", nodeA.ID)
	publish("telegram", "msg-b", nodeB.ID)
	publish("telegram", "msg-legacy", "")  // путь занят двумя командами — владелец неизвестен
	publish("solo", "msg-solo-legacy", "") // путь однозначен — сообщение узла solo

	cancelSet := queuecancel.New(redisClient)
	nodeRepo := pgrepo.NewNodeRepoPg(fx.pool, fx.cipher, fx.logger)
	auditUC := webuc.NewAuditUsecase(pgrepo.NewAuditRepoPg(fx.pool, fx.logger), fx.logger)
	// Delay-топик паузы сценарию не нужен: пустое имя отключает второй источник.
	uc := webuc.NewAsyncQueueUsecase(kafkaadmin.New(brokers, 5*time.Second, 0, fx.logger), cancelSet, nil,
		nodeRepo, nodeRepo, auditUC, cfg.Kafka.ConsumerGroup, cfg.Kafka.AsyncTopic, "", "",
		time.Hour, 1000, fx.logger)

	ids := func(r webuc.QueueListResult) []string {
		out := make([]string, 0, len(r.Items))
		for _, it := range r.Items {
			out = append(out, it.ID)
		}
		return out
	}

	var listA, listB, listSolo webuc.QueueListResult
	require.Eventually(t, func() bool {
		var errA, errB, errSolo error
		listA, errA = uc.List(ctx, nodeA.ID, nodeA.TeamID)
		listB, errB = uc.List(ctx, nodeB.ID, nodeB.TeamID)
		listSolo, errSolo = uc.List(ctx, solo.ID, solo.TeamID)
		return errA == nil && errB == nil && errSolo == nil &&
			len(listA.Items) > 0 && len(listB.Items) > 0 && len(listSolo.Items) > 0
	}, 30*time.Second, time.Second, "очереди всех трёх узлов должны стать видимыми")

	require.Equal(t, []string{"msg-a"}, ids(listA),
		"узел A не видит сообщение одноимённого узла B и старый конверт неоднозначного пути")
	require.Equal(t, []string{"msg-b"}, ids(listB))
	require.Equal(t, []string{"msg-solo-legacy"}, ids(listSolo),
		"старый конверт однозначного пути остаётся видимым своему узлу")

	// Тело сообщения B по его координатам, запрошенное через узел A, — «не найдено».
	msgB := listB.Items[0]
	_, err := uc.Body(ctx, nodeA.ID, nodeA.TeamID, msgB.Topic, msgB.Partition, msgB.Offset)
	require.ErrorIs(t, err, domain.ErrNotFound, "тело чужого сообщения не отдаётся")
	body, err := uc.Body(ctx, nodeB.ID, nodeB.TeamID, msgB.Topic, msgB.Partition, msgB.Offset)
	require.NoError(t, err)
	require.Equal(t, "msg-b", body.ID)

	// Очистка A отменяет только своё сообщение.
	purged, err := uc.PurgeAll(ctx, webuc.SystemActor(), nodeA.ID, nodeA.TeamID)
	require.NoError(t, err)
	require.Equal(t, 1, purged.Cancelled)
	for id, want := range map[string]bool{"msg-a": true, "msg-b": false, "msg-legacy": false} {
		got, cErr := cancelSet.IsCancelled(ctx, id)
		require.NoError(t, cErr)
		require.Equal(t, want, got, "tombstone %s", id)
	}
}
