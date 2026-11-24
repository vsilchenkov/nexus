//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	segkafka "github.com/segmentio/kafka-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kafkapf "nexus/internal/platform/kafka"
	"nexus/internal/platform/logging"
)

// TestKafkaOffsets_GroupPositionsAndRetention (§113.4): позиции группы и
// retention топика читаются из настоящего брокера — на них стоят lag в
// Prometheus, экран Kafka и решение планировщика прохода очереди паузы.
func TestKafkaOffsets_GroupPositionsAndRetention(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	brokers, kafkaCleanup := startKafka(t, ctx)
	defer kafkaCleanup()

	cfg := newKafkaTestConfig(brokers)
	cfg.Kafka.Topic.Partitions = 2
	const topic = "nexus.it.offsets"
	require.NoError(t, kafkapf.EnsureTopics(ctx, cfg, logging.NewNoop(), topic))

	writeToPartition(t, ctx, brokers, topic, 0, 5)
	writeToPartition(t, ctx, brokers, topic, 1, 3)

	// Группа без участников коммитит «как simple consumer» (generation -1):
	// партиция 0 прочитана до offset 2, партицию 1 группа не трогала.
	commitSimple(t, ctx, brokers, "it-offsets", topic, 0, 2)

	off := kafkapf.NewOffsetsForBrokers(brokers, 10*time.Second)

	// Свежий брокер какое-то время отвечает NOT_COORDINATOR: координатор групп
	// ещё поднимается. Это ошибка, а не «группа ничего не коммитила».
	var pos []kafkapf.PartitionPosition
	require.Eventually(t, func() bool {
		var err error
		pos, err = off.GroupPositions(ctx, "it-offsets", topic)
		return err == nil
	}, 60*time.Second, 500*time.Millisecond)
	assert.Equal(t, []kafkapf.PartitionPosition{
		{Partition: 0, First: 0, Last: 5, Committed: 2},
		{Partition: 1, First: 0, Last: 3, Committed: -1},
	}, pos)
	assert.Equal(t, int64(3), pos[0].Lag())
	assert.Equal(t, int64(3), pos[1].Lag(), "некоммиченная партиция читается с начала")

	unknown, err := off.GroupPositions(ctx, "it-offsets-nobody", topic)
	require.NoError(t, err)
	for _, p := range unknown {
		assert.Equal(t, int64(-1), p.Committed, "группа, которой нет, ничего не коммитила")
	}

	retention, err := off.TopicRetention(ctx, topic)
	require.NoError(t, err)
	assert.Equal(t, time.Duration(cfg.Kafka.Topic.RetentionMs)*time.Millisecond, retention)
}

// commitSimple коммитит offset группы без участников (generation -1), повторяя,
// пока координатор групп свежего брокера не готов принять коммит.
func commitSimple(t *testing.T, ctx context.Context, broker, group, topic string, partition int, offset int64) {
	t.Helper()
	kc := &segkafka.Client{Addr: segkafka.TCP(broker), Timeout: 10 * time.Second}
	require.Eventually(t, func() bool {
		resp, err := kc.OffsetCommit(ctx, &segkafka.OffsetCommitRequest{
			GroupID: group, GenerationID: -1,
			Topics: map[string][]segkafka.OffsetCommit{topic: {{Partition: partition, Offset: offset}}},
		})
		if err != nil {
			return false
		}
		for _, p := range resp.Topics[topic] {
			if p.Error != nil {
				return false
			}
		}
		return true
	}, 60*time.Second, 500*time.Millisecond, "коммит offset'а группы")
}

// writeToPartition пишет n сообщений прямо в лидера партиции — тесту нужно
// точно знать, сколько сообщений легло в каждую.
func writeToPartition(t *testing.T, ctx context.Context, broker, topic string, partition, n int) {
	t.Helper()
	conn, err := segkafka.DialLeader(ctx, "tcp", broker, topic, partition)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	msgs := make([]segkafka.Message, n)
	for i := range msgs {
		msgs[i] = segkafka.Message{Value: fmt.Appendf(nil, "p%d-%d", partition, i)}
	}
	_, err = conn.WriteMessages(msgs...)
	require.NoError(t, err)
}
