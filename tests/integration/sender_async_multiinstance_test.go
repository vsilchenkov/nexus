//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	kafkapf "nexus/internal/platform/kafka"
	"nexus/internal/platform/logging"
)

// TestSender_Async_MultiInstanceSmoke: конфигурация из прода (несколько
// партиций и несколько consumer-инстансов) доставляет сообщения всех узлов.
//
// Все остальные async-тесты гоняют partitions=1/instances=1, поэтому путь с
// распределением партиций между инстансами не проверялся вообще. Тест намеренно
// НЕ ассертит, какой инстанс какую партицию получил: kafka.Hash может положить
// оба ключа в одну партицию, и это нормально.
func TestSender_Async_MultiInstanceSmoke(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	pool, pgCleanup := startPostgres(t, ctx)
	defer pgCleanup()
	brokers, kafkaCleanup := startKafka(t, ctx)
	defer kafkaCleanup()

	rec := &hitRecorder{}
	mock := newHitServer(rec)
	defer mock.Close()

	cfg := newKafkaTestConfig(brokers)
	cfg.Kafka.ConsumerGroup = "nexus-sender-multi-it"
	cfg.Kafka.Topic.Partitions = 2
	cfg.Kafka.Consumer.Instances = 2
	require.NoError(t, kafkapf.EnsureTopics(ctx, cfg, logging.NewNoop(), cfg.Kafka.AsyncTopic, cfg.Kafka.DLQTopic))

	stack := newAsyncStack(t, pool, cfg)
	defer stack.close()

	nodeUC := newAsyncNodeUsecase(t, ctx, pool, stack.cipher)
	createAsyncNode(t, ctx, nodeUC, "demo/multi-a", mock.URL+"/multi-a", "test.demo_multi_a", domain.NodeStatusEnabled)
	createAsyncNode(t, ctx, nodeUC, "demo/multi-b", mock.URL+"/multi-b", "test.demo_multi_b", domain.NodeStatusEnabled)

	cg := stack.startConsumerGroup(ctx, stack.newProcessor())
	defer cg.Stop()

	stack.produceEnvelope(t, ctx, "multi-a-1", "demo/multi-a", mock.URL+"/multi-a", `{"n":1}`)
	stack.produceEnvelope(t, ctx, "multi-b-1", "demo/multi-b", mock.URL+"/multi-b", `{"n":2}`)

	require.Eventually(t, func() bool {
		return rec.count("/multi-a") == 1 && rec.count("/multi-b") == 1
	}, 90*time.Second, 200*time.Millisecond,
		"оба узла должны получить свои сообщения при нескольких инстансах consumer'а")

	require.Len(t, cg.Snapshot(), 2, "по одному снимку lag на инстанс — Prometheus читает именно их")
}
