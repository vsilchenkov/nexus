//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	kafkapf "nexus/internal/platform/kafka"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/queuecancel"
	kafkaadapter "nexus/internal/sender/adapter/in/kafka"
	"nexus/internal/sender/adapter/out/kafkaoffsets"
	"nexus/internal/sender/adapter/out/nodepg"
	senderuc "nexus/internal/sender/usecase"
)

// startGatedPausedSweeper — sweeper очереди паузы с планировщиком (§113.3), как
// в проде (sender/app.go). signal может быть nil (без Redis).
func (s *asyncStack) startGatedPausedSweeper(ctx context.Context, interval time.Duration, signal senderuc.PausedSweepSignaler) func() {
	proc := s.newProcessor(senderuc.WithPausedRequeue(s.cfg.Kafka.PausedTopic))
	offsets := kafkaoffsets.NewPausedTopic(kafkapf.NewOffsetsForBrokers(s.cfg.Kafka.Brokers, 10*time.Second),
		s.cfg.Kafka.PausedGroup(), s.cfg.Kafka.PausedTopic)
	planner := senderuc.NewPausedSweepPlanner(nodepg.New(s.pool, s.cipher, s.logger), signal, offsets,
		senderuc.PausedSweepPlannerConfig{Backstop: time.Hour}, nil, s.logger)
	sw := kafkaadapter.NewPausedSweeper(s.cfg, proc, interval, 100, nil, s.logger,
		kafkaadapter.WithSweepPlanner(planner))
	sctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		sw.Run(sctx)
	}()
	return func() {
		cancel()
		sw.Stop()
		<-done
	}
}

// pausedTopicProbe — конец лога delay-топика и lag группы sweeper'а из брокера.
type pausedTopicProbe struct {
	off          *kafkapf.Offsets
	group, topic string
}

func newPausedTopicProbe(s *asyncStack) *pausedTopicProbe {
	return &pausedTopicProbe{
		off:   kafkapf.NewOffsetsForBrokers(s.cfg.Kafka.Brokers, 10*time.Second),
		group: s.cfg.Kafka.PausedGroup(), topic: s.cfg.Kafka.PausedTopic,
	}
}

// positions — позиции группы sweeper'а. Свежий брокер первые секунды
// отвечает транзиентными ошибками (NOT_COORDINATOR, Unknown) — повторяем.
func (p *pausedTopicProbe) positions(t *testing.T, ctx context.Context) []kafkapf.PartitionPosition {
	t.Helper()
	var (
		pos []kafkapf.PartitionPosition
		err error
	)
	for range 30 {
		if pos, err = p.off.GroupPositions(ctx, p.group, p.topic); err == nil {
			return pos
		}
		time.Sleep(500 * time.Millisecond)
	}
	require.NoError(t, err, "позиции delay-топика")
	return nil
}

// hwm — сумма high watermark по партициям: растёт на каждой перезаписи.
func (p *pausedTopicProbe) hwm(t *testing.T, ctx context.Context) int64 {
	t.Helper()
	var sum int64
	for _, pp := range p.positions(t, ctx) {
		sum += pp.Last
	}
	return sum
}

func (p *pausedTopicProbe) lag(t *testing.T, ctx context.Context) int64 {
	t.Helper()
	var sum int64
	for _, pp := range p.positions(t, ctx) {
		sum += pp.Lag()
	}
	return sum
}

// pollUntil — опрос cond до timeout в ГОРУТИНЕ ТЕСТА: require внутри пробы в
// условии require.Eventually (своя горутина) не завершил бы тест, а повесил бы
// ожидание.
func pollUntil(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(msg)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// waitQuiet ждёт, пока конец лога не перестанет расти quiet подряд (раунд
// закончился), и возвращает его.
func (p *pausedTopicProbe) waitQuiet(t *testing.T, ctx context.Context, quiet, timeout time.Duration) int64 {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last, since := p.hwm(t, ctx), time.Now()
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		if cur := p.hwm(t, ctx); cur != last {
			last, since = cur, time.Now()
			continue
		}
		if time.Since(since) >= quiet {
			return last
		}
	}
	t.Fatalf("delay-топик не успокоился за %s (hwm=%d)", timeout, last)
	return 0
}

// requireStable — конец лога не меняется весь период: очередь не переписывается.
func (p *pausedTopicProbe) requireStable(t *testing.T, ctx context.Context, want int64, period time.Duration) {
	t.Helper()
	deadline := time.Now().Add(period)
	for time.Now().Before(deadline) {
		require.Equal(t, want, p.hwm(t, ctx), "очередь паузы переписывается без события")
		time.Sleep(time.Second)
	}
}

// setupGateStack — PG + Kafka (partitions партиций) + узлы на паузе + основной
// consumer с переносом в delay-топик. Возвращает стек после warmup и
// публикации backlog сообщений на каждый узел на паузе.
func setupGateStack(t *testing.T, ctx context.Context, group string, partitions int, paused []string, backlog int) (*asyncStack, *hitRecorder, func(path string) string) {
	t.Helper()
	pool, pgCleanup := startPostgres(t, ctx)
	t.Cleanup(pgCleanup)
	brokers, kafkaCleanup := startKafka(t, ctx)
	t.Cleanup(kafkaCleanup)
	rec := &hitRecorder{}
	mock := newHitServer(rec)
	t.Cleanup(mock.Close)

	cfg := newKafkaTestConfig(brokers)
	cfg.Kafka.ConsumerGroup = group
	cfg.Kafka.PausedTopic = "nexus.async.paused"
	cfg.Kafka.Topic.Partitions = partitions
	require.NoError(t, kafkapf.EnsureTopics(ctx, cfg, logging.NewNoop(),
		cfg.Kafka.AsyncTopic, cfg.Kafka.DLQTopic, cfg.Kafka.PausedTopic))
	stack := newAsyncStack(t, pool, cfg)
	t.Cleanup(stack.close)

	url := func(path string) string { return mock.URL + "/" + path }
	nodeUC := newAsyncNodeUsecase(t, ctx, pool, stack.cipher)
	for i, p := range paused {
		createAsyncNode(t, ctx, nodeUC, p, url(p), fmt.Sprintf("test.gate_%d", i), domain.NodeStatusPaused)
	}
	createAsyncNode(t, ctx, nodeUC, "gate/warmup", url("gate/warmup"), "test.gate_warmup", domain.NodeStatusEnabled)

	stopConsumer := stack.startConsumer(ctx, stack.pausedRequeueOpt())
	t.Cleanup(stopConsumer)
	stack.warmupConsumer(t, ctx, rec, "gate/warmup", url("gate/warmup"), "/gate/warmup")
	for _, p := range paused {
		for i := range backlog {
			stack.produceEnvelope(t, ctx, fmt.Sprintf("%s-%d", p, i), p, url(p), fmt.Sprintf(`{"n":%d}`, i))
		}
	}
	probe := newPausedTopicProbe(stack)
	want := int64(len(paused) * backlog)
	pollUntil(t, 60*time.Second, func() bool { return probe.hwm(t, ctx) >= want },
		"основной consumer переносит сообщения узлов на паузе в delay-топик")
	return stack, rec, url
}

// TestSender_PausedGate_IdleQueueNotRewritten (§113.3) — регрессия инцидента
// 06.10.2026: пока узлы на паузе и ничего не меняется, sweeper не переписывает
// их очередь; после рестарта — один стартовый раунд и снова тишина; снятие
// паузы доставляет весь бэклог. Две партиции: раунд закрывается по обеим.
func TestSender_PausedGate_IdleQueueNotRewritten(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	paused := []string{"gate/a", "gate/b", "gate/c"}
	const backlog = 4
	stack, rec, _ := setupGateStack(t, ctx, "nexus-sender-gate-idle-it", 2, paused, backlog)
	probe := newPausedTopicProbe(stack)

	stop := stack.startGatedPausedSweeper(ctx, time.Second, nil)
	quiet := probe.waitQuiet(t, ctx, 4*time.Second, 60*time.Second)
	probe.requireStable(t, ctx, quiet, 10*time.Second)
	stop()

	stop = stack.startGatedPausedSweeper(ctx, time.Second, nil)
	defer stop()
	pollUntil(t, 30*time.Second, func() bool { return probe.hwm(t, ctx) > quiet },
		"после рестарта — стартовый раунд")
	quiet = probe.waitQuiet(t, ctx, 4*time.Second, 60*time.Second)
	probe.requireStable(t, ctx, quiet, 6*time.Second)
	for _, p := range paused {
		require.Zero(t, rec.count("/"+p), "на паузе доставки нет")
	}

	for _, p := range paused {
		setNodeStatus(t, ctx, stack.pool, p, domain.NodeStatusEnabled)
	}
	require.Eventually(t, func() bool {
		for _, p := range paused {
			if rec.count("/"+p) < backlog {
				return false
			}
		}
		return true
	}, 60*time.Second, 200*time.Millisecond, "после снятия паузы доставлен весь бэклог")
}

// TestSender_PausedGate_StatusRoundTripTriggersRound: статус сменили туда и
// обратно между двумя тиками — сравнение статусов этого не увидело бы, а
// отпечаток (xmin) видит, и раунд проходит.
func TestSender_PausedGate_StatusRoundTripTriggersRound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	stack, rec, _ := setupGateStack(t, ctx, "nexus-sender-gate-roundtrip-it", 1, []string{"gate/rt"}, 3)
	probe := newPausedTopicProbe(stack)

	stop := stack.startGatedPausedSweeper(ctx, 3*time.Second, nil)
	defer stop()
	quiet := probe.waitQuiet(t, ctx, 8*time.Second, 90*time.Second)

	setNodeStatus(t, ctx, stack.pool, "gate/rt", domain.NodeStatusEnabled)
	setNodeStatus(t, ctx, stack.pool, "gate/rt", domain.NodeStatusPaused)

	// Тик мог попасть между двумя UPDATE — тогда раунд доставил. Иначе раунд
	// переписал очередь. Важно одно: раунд прошёл.
	pollUntil(t, 30*time.Second, func() bool { return probe.hwm(t, ctx) > quiet || rec.count("/gate/rt") > 0 },
		"смена статуса туда-обратно запускает раунд")
}

// TestSender_PausedGate_CancelSignalDropsQueue: «Очистить» во вкладке «Очередь»
// ставит отметки отмены и поднимает сигнал (Web: Cancel, затем один Bump у узла
// на паузе) — раунд снимает сообщения с очереди без правки узла, и после снятия
// паузы они не доставляются.
func TestSender_PausedGate_CancelSignalDropsQueue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	redis, redisCleanup := startRedis(t, ctx)
	defer redisCleanup()
	cancelSet := queuecancel.New(redis)

	const backlog = 3
	stack, rec, _ := setupGateStack(t, ctx, "nexus-sender-gate-cancel-it", 1, []string{"gate/purge"}, backlog)
	stack.cancel = cancelSet
	probe := newPausedTopicProbe(stack)

	stop := stack.startGatedPausedSweeper(ctx, time.Second, cancelSet)
	defer stop()
	quiet := probe.waitQuiet(t, ctx, 4*time.Second, 60*time.Second)
	require.Equal(t, int64(backlog), probe.lag(t, ctx), "живые копии очереди — за committed группы")

	ids := make([]string, 0, backlog)
	for i := range backlog {
		ids = append(ids, fmt.Sprintf("gate/purge-%d", i))
	}
	_, err := cancelSet.Cancel(ctx, ids, time.Hour)
	require.NoError(t, err)
	_, err = cancelSet.Bump(ctx)
	require.NoError(t, err)

	pollUntil(t, 30*time.Second, func() bool { return probe.lag(t, ctx) == 0 },
		"сигнал отмены запускает раунд, отменённое снимается с очереди")
	require.Equal(t, quiet, probe.hwm(t, ctx), "отменённое не переписывается")

	setNodeStatus(t, ctx, stack.pool, "gate/purge", domain.NodeStatusEnabled)
	time.Sleep(5 * time.Second)
	require.Zero(t, rec.count("/gate/purge"), "отменённые сообщения не доставляются")
}
