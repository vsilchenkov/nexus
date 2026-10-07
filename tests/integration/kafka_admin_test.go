//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tckafka "github.com/testcontainers/testcontainers-go/modules/kafka"

	kafkapf "nexus/internal/platform/kafka"
	"nexus/internal/platform/logging"
	"nexus/internal/web/adapter/out/kafkaadmin"
	"nexus/internal/web/usecase/port"
)

// topicGroup — группа топика из ответа Topics; ok=false — топика или группы нет.
func topicGroup(topics []port.TopicInfo, topic, group string) (port.TopicConsumerGroup, bool) {
	for _, t := range topics {
		if t.Name != topic {
			continue
		}
		for _, g := range t.ConsumerGroups {
			if g.Group == group {
				return g, true
			}
		}
	}
	return port.TopicConsumerGroup{}, false
}

// TestKafkaAdmin_StoppedBrokerNotOnline (§114.4): адаптер экрана Kafka на
// настоящем брокере. Пока брокер жив, обход топиков видит группу с её lag, а
// ping (ApiVersions) отвечает OK. После остановки брокера kafka-go продолжает
// отдавать метаданные из кеша транспорта, поэтому до §114 ping через Metadata
// показывал «OK», а Topics, проглотив ошибку ListGroups, возвращал топики без
// групп как нормальный ответ. Теперь ping — «недоступен», Topics — ошибка.
func TestKafkaAdmin_StoppedBrokerNotOnline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	c, err := tckafka.Run(ctx, "confluentinc/confluent-local:7.5.0")
	require.NoError(t, err)
	defer func() { _ = c.Terminate(context.WithoutCancel(ctx)) }()
	brokers, err := c.Brokers(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, brokers)
	broker := brokers[0]

	cfg := newKafkaTestConfig(broker)
	const topic, group = "nexus.it.admin", "it-admin"
	require.NoError(t, kafkapf.EnsureTopics(ctx, cfg, logging.NewNoop(), topic))
	writeToPartition(t, ctx, broker, topic, 0, 4)
	commitSimple(t, ctx, broker, group, topic, 0, 1)

	admin := kafkaadmin.New(broker, 5*time.Second, 0, logging.NewNoop())
	var g port.TopicConsumerGroup
	require.Eventually(t, func() bool {
		topics, err := admin.Topics(ctx)
		if err != nil {
			return false
		}
		var ok bool
		g, ok = topicGroup(topics, topic, group)
		return ok
	}, 60*time.Second, 500*time.Millisecond, "группа видна в обходе топиков")
	assert.EqualValues(t, 3, g.LagTotal)

	pings, err := admin.Ping(ctx)
	require.NoError(t, err)
	require.Len(t, pings, 1)
	assert.True(t, pings[0].OK, "живой брокер отвечает на ApiVersions")

	stop := 10 * time.Second
	require.NoError(t, c.Stop(ctx, &stop))

	pings, err = admin.Ping(ctx)
	require.NoError(t, err)
	require.Len(t, pings, 1)
	assert.False(t, pings[0].OK, "остановленный брокер не «OK»")
	assert.Equal(t, "unreachable", pings[0].Warn)

	_, err = admin.Topics(ctx)
	assert.Error(t, err, "обход без ListGroups — ошибка, а не топики без групп")
}
