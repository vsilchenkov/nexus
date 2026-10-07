//go:build integration

package integration

import (
	"context"
	"sync"
	"testing"
	"time"

	segkafka "github.com/segmentio/kafka-go"
	"github.com/stretchr/testify/require"

	kafkapf "nexus/internal/platform/kafka"
	"nexus/internal/platform/logging"
	kafkaadapter "nexus/internal/sender/adapter/in/kafka"
)

// commitErrTally — счётчик отвергнутых commit'ов (nexus_kafka_commit_errors_total).
type commitErrTally struct {
	mu sync.Mutex
	n  int
}

func (c *commitErrTally) InitKafkaCommitErrors(string) {}

func (c *commitErrTally) IncKafkaCommitError(string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
}

func (c *commitErrTally) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// maxSeen — сколько раз доставлено самое повторённое сообщение (1 — дублей нет).
func (r *idRecorder) maxSeen() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	most := 0
	for _, n := range r.seen {
		most = max(most, n)
	}
	return most
}

// setTopicConfig меняет настройку топика на лету (IncrementalAlterConfigs).
func setTopicConfig(t *testing.T, ctx context.Context, broker, topic, key, value string) {
	t.Helper()
	kc := &segkafka.Client{Addr: segkafka.TCP(broker), Timeout: 10 * time.Second}
	resp, err := kc.IncrementalAlterConfigs(ctx, &segkafka.IncrementalAlterConfigsRequest{
		Resources: []segkafka.IncrementalAlterConfigsRequestResource{{
			ResourceType: segkafka.ResourceTypeTopic,
			ResourceName: topic,
			Configs: []segkafka.IncrementalAlterConfigsRequestConfig{{
				Name: key, Value: value, ConfigOperation: segkafka.ConfigOperationSet,
			}},
		}},
	})
	require.NoError(t, err)
	for _, r := range resp.Resources {
		require.NoError(t, r.Error, "alter %s %s=%s", topic, key, value)
	}
}

// groupPosition — позиция группы по единственной партиции топика; ok=false —
// брокер не ответил (свежий брокер несколько секунд отвечает NOT_COORDINATOR).
func groupPosition(ctx context.Context, o *kafkapf.Offsets, group, topic string) (kafkapf.PartitionPosition, bool) {
	pos, err := o.GroupPositions(ctx, group, topic)
	if err != nil || len(pos) != 1 {
		return kafkapf.PartitionPosition{}, false
	}
	return pos[0], true
}

// committedAll — группа подтвердила всё, что есть в партиции.
func committedAll(ctx context.Context, o *kafkapf.Offsets, group, topic string) bool {
	p, ok := groupPosition(ctx, o, group, topic)
	return ok && p.Committed == p.Last
}

// TestSender_StallWatchdog_CommitRejectedNoRedelivery (§114.2/§114.3): брокер
// по-настоящему отвергает commit — у __consumer_offsets min.insync.replicas=2
// при одной реплике, и OffsetCommit отвечает NOT_ENOUGH_REPLICAS. Consumer
// доставляет всё и ждёт в fetch, committed стоит, lag > 0 — до §114 сторож
// перезапускал такой reader, и приёмник получал повторы (а новый участник не
// вошёл бы в группу: ребаланс пишет в тот же __consumer_offsets). Теперь:
// перезапусков нет, дублей нет, ошибки commit посчитаны; после восстановления
// позиции догоняют доставленное сами.
func TestSender_StallWatchdog_CommitRejectedNoRedelivery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	broker, kafkaCleanup := startKafka(t, ctx)
	defer kafkaCleanup()

	cfg := newKafkaTestConfig(broker)
	cfg.Kafka.ConsumerGroup = "nexus-sender-commit-it"
	require.NoError(t, kafkapf.EnsureTopics(ctx, cfg, logging.NewNoop(), cfg.Kafka.AsyncTopic))
	topic, group := cfg.Kafka.AsyncTopic, cfg.Kafka.ConsumerGroup

	offsets := kafkapf.NewOffsetsForBrokers(broker, 10*time.Second)
	restarts, commits, rec := &restartTally{}, &commitErrTally{}, &idRecorder{}
	guard := kafkaadapter.StallGuard{Inspector: offsets, After: 2 * time.Second, CheckEvery: 500 * time.Millisecond,
		ClientPrefix: "it", Metrics: restarts, Commits: commits}
	cg := kafkaadapter.NewConsumerGroup(cfg, topic, rec, logging.NewNoop(), kafkaadapter.WithStallGuard(guard))
	cg.Start(ctx)
	defer cg.Stop()

	pollUntil(t, 90*time.Second, func() bool {
		return assignmentHas(ctx, offsets, group, topic, group+"-it-", "-g1", 1)
	}, "reader вошёл в группу")
	writeBatch(t, ctx, broker, topic, 0, "before", 20)
	pollUntil(t, 60*time.Second, func() bool {
		return rec.unique() == 20 && committedAll(ctx, offsets, group, topic)
	}, "первая пачка доставлена и подтверждена")

	setTopicConfig(t, ctx, broker, "__consumer_offsets", "min.insync.replicas", "2")
	writeBatch(t, ctx, broker, topic, 0, "after", 20)
	pollUntil(t, 120*time.Second, func() bool { return rec.unique() == 40 && commits.count() > 0 },
		"вторая пачка доставлена, commit отвергается")
	stuck, ok := groupPosition(ctx, offsets, group, topic)
	require.True(t, ok)
	require.Less(t, stuck.Committed, stuck.Last, "позиция не сохранилась — сценарий настоящий")

	// Окно сторожа: committed стоит ≥ After, reader ждёт в fetch ≥ After.
	for end := time.Now().Add(4 * guard.After); time.Now().Before(end); {
		require.Zero(t, restarts.count(), "дочитавший reader со стоящим commit не перезапускается")
		time.Sleep(guard.CheckEvery)
	}
	require.Equal(t, 1, rec.maxSeen(), "повторной доставки нет")

	setTopicConfig(t, ctx, broker, "__consumer_offsets", "min.insync.replicas", "1")
	writeBatch(t, ctx, broker, topic, 0, "recover", 1)
	pollUntil(t, 60*time.Second, func() bool {
		return rec.unique() == 41 && committedAll(ctx, offsets, group, topic)
	}, "после восстановления позиция догнала доставленное")
	require.Zero(t, restarts.count())
	require.Equal(t, 1, rec.maxSeen())
}
