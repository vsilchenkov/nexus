//go:build integration

package integration

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	segkafka "github.com/segmentio/kafka-go"
	"github.com/stretchr/testify/require"

	kafkapf "nexus/internal/platform/kafka"
	"nexus/internal/platform/logging"
	kafkaadapter "nexus/internal/sender/adapter/in/kafka"
	senderuc "nexus/internal/sender/usecase"
)

// stallingInspector — настоящий брокер, кроме одного: пока «взведён», партиция
// stalledPart выглядит зависшей (lag 1, committed не двигается). Настоящий
// зависший kafka-go reader в тесте не воспроизвести, а реакция сторожа на него
// — перезапуск живого reader'а посреди потока — проверяется по-настоящему.
type stallingInspector struct {
	real *kafkapf.Offsets

	mu          sync.Mutex
	armed       bool
	stalledPart int
}

func (s *stallingInspector) GroupPositions(ctx context.Context, group, topic string) ([]kafkapf.PartitionPosition, error) {
	pos, err := s.real.GroupPositions(ctx, group, topic)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.armed {
		for i := range pos {
			if pos[i].Partition == s.stalledPart {
				pos[i].Committed, pos[i].First, pos[i].Last = 1_000_000, 0, 1_000_001
			}
		}
	}
	return pos, err
}

func (s *stallingInspector) GroupAssignment(ctx context.Context, group, topic string) (map[string][]int, error) {
	return s.real.GroupAssignment(ctx, group, topic)
}

// arm делает партицию part «стоящей»; part < 0 — снять.
func (s *stallingInspector) arm(part int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.armed, s.stalledPart = part >= 0, part
}

// idRecorder — обработчик, запоминающий тела сообщений (повторы допустимы).
type idRecorder struct {
	mu   sync.Mutex
	seen map[string]int
}

func (r *idRecorder) Handle(_ context.Context, value []byte, _ map[string]string) senderuc.HandleResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen == nil {
		r.seen = map[string]int{}
	}
	r.seen[string(value)]++
	return senderuc.HandleAck
}

func (r *idRecorder) unique() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.seen)
}

type restartTally struct {
	mu sync.Mutex
	n  int
}

func (r *restartTally) IncKafkaConsumerRestart(string, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.n++
}

func (r *restartTally) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

// writeBatch пишет n сообщений "<prefix>-<i>" в лидера партиции.
func writeBatch(t *testing.T, ctx context.Context, broker, topic string, partition int, prefix string, n int) {
	t.Helper()
	conn, err := segkafka.DialLeader(ctx, "tcp", broker, topic, partition)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	msgs := make([]segkafka.Message, n)
	for i := range msgs {
		msgs[i] = segkafka.Message{Value: fmt.Appendf(nil, "%s-%d", prefix, i)}
	}
	_, err = conn.WriteMessages(msgs...)
	require.NoError(t, err)
}

// assignmentHas — у какого-нибудь участника группы client.id с суффиксом suffix
// и назначенными партициями; заодно — покрыты ли все партиции.
func assignmentHas(ctx context.Context, o *kafkapf.Offsets, group, topic, prefix, suffix string, parts int) bool {
	a, err := o.GroupAssignment(ctx, group, topic)
	if err != nil {
		return false
	}
	covered, found := map[int]bool{}, false
	for id, ps := range a {
		if !strings.HasPrefix(id, prefix) {
			return false // чужой участник — значит client.id не наш
		}
		if strings.HasSuffix(id, suffix) {
			found = true
		}
		for _, p := range ps {
			covered[p] = true
		}
	}
	return found && len(covered) == parts
}

// TestSender_StallWatchdog_RecreatesReaderMidStream (§113.6): client.id
// reader'ов виден брокеру (на нём держится сопоставление партиций), сторож
// пересоздаёт reader экземпляра со «стоящей» партицией, и сообщения, записанные
// до и сразу после перезапуска, доставляются все (at-least-once).
func TestSender_StallWatchdog_RecreatesReaderMidStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	broker, kafkaCleanup := startKafka(t, ctx)
	defer kafkaCleanup()

	cfg := newKafkaTestConfig(broker)
	cfg.Kafka.ConsumerGroup = "nexus-sender-stall-it"
	cfg.Kafka.Topic.Partitions = 2
	cfg.Kafka.Consumer.Instances = 2
	require.NoError(t, kafkapf.EnsureTopics(ctx, cfg, logging.NewNoop(), cfg.Kafka.AsyncTopic))

	offsets := kafkapf.NewOffsetsForBrokers(broker, 10*time.Second)
	insp := &stallingInspector{real: offsets}
	restarts := &restartTally{}
	rec := &idRecorder{}
	guard := kafkaadapter.StallGuard{Inspector: insp, After: 2 * time.Second, CheckEvery: 500 * time.Millisecond,
		ClientPrefix: "it", Metrics: restarts}
	cg := kafkaadapter.NewConsumerGroup(cfg, cfg.Kafka.AsyncTopic, rec, logging.NewNoop(), kafkaadapter.WithStallGuard(guard))
	cg.Start(ctx)
	defer cg.Stop()

	const prefix = "nexus-sender-stall-it-it-"
	pollUntil(t, 90*time.Second, func() bool {
		return assignmentHas(ctx, offsets, cfg.Kafka.ConsumerGroup, cfg.Kafka.AsyncTopic, prefix, "-g1", 2)
	}, "брокер должен видеть client.id наших reader'ов и назначить им обе партиции")

	writeBatch(t, ctx, broker, cfg.Kafka.AsyncTopic, 0, "before", 20)
	writeBatch(t, ctx, broker, cfg.Kafka.AsyncTopic, 1, "before1", 20)
	pollUntil(t, 60*time.Second, func() bool { return rec.unique() == 40 }, "первая пачка доставлена")
	require.Zero(t, restarts.count(), "здоровая группа не перезапускается")

	insp.arm(0)
	pollUntil(t, 30*time.Second, func() bool { return restarts.count() >= 1 }, "сторож перезапускает экземпляр со стоящей партицией")
	insp.arm(-1)

	// Сразу после перезапуска — пока новый reader входит в группу.
	writeBatch(t, ctx, broker, cfg.Kafka.AsyncTopic, 0, "after", 20)
	writeBatch(t, ctx, broker, cfg.Kafka.AsyncTopic, 1, "after1", 20)
	pollUntil(t, 90*time.Second, func() bool { return rec.unique() == 80 }, "сообщения после перезапуска доставлены")
	pollUntil(t, 60*time.Second, func() bool {
		return assignmentHas(ctx, offsets, cfg.Kafka.ConsumerGroup, cfg.Kafka.AsyncTopic, prefix, "-g2", 2)
	}, "пересозданный reader — новое поколение client.id в группе")
	require.Equal(t, 1, restarts.count(), "пауза между перезапусками")
}
