//go:build integration

package integration

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	kafkapf "nexus/internal/platform/kafka"
	"nexus/internal/platform/logging"
	senderuc "nexus/internal/sender/usecase"
)

// dropCommitProcessor моделирует падение Sender'а МЕЖДУ успешной доставкой и
// commit'ом offset'а. Первый вызов делегируется реальному обработчику (запрос
// к внешнему узлу уходит по-настоящему), но наружу возвращается HandleRetry —
// значит offset не коммитится. Последующие вызовы (retry-in-place крутит то же
// сообщение) уже НЕ делегируются: иначе тест считал бы лишние доставки, которых
// при настоящем падении процесса не было бы.
type dropCommitProcessor struct {
	inner asyncHandler

	mu        sync.Mutex
	delivered []senderuc.HandleResult
	firstDone chan struct{}
	once      sync.Once
}

func newDropCommitProcessor(inner asyncHandler) *dropCommitProcessor {
	return &dropCommitProcessor{inner: inner, firstDone: make(chan struct{})}
}

func (p *dropCommitProcessor) Handle(ctx context.Context, value []byte, headers map[string]string) senderuc.HandleResult {
	p.mu.Lock()
	already := len(p.delivered) > 0
	p.mu.Unlock()

	if !already {
		res := p.inner.Handle(ctx, value, headers)
		p.mu.Lock()
		p.delivered = append(p.delivered, res)
		p.mu.Unlock()
		p.once.Do(func() { close(p.firstDone) })
	}
	// «Процесс умер до commit'а»: offset остаётся незакоммиченным.
	return senderuc.HandleRetry
}

func (p *dropCommitProcessor) firstResult(t *testing.T) senderuc.HandleResult {
	t.Helper()
	select {
	case <-p.firstDone:
	case <-time.After(90 * time.Second):
		t.Fatal("сообщение так и не было обработано consumer'ом")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.delivered[0]
}

// TestSender_Async_AtLeastOnce_RedeliveryAfterCrashBeforeCommit фиксирует
// контракт шины: доставка гарантируется at-least-once, а не exactly-once.
//
// Если Sender упал (или был остановлен) после успешного HTTP-вызова, но до
// commit'а offset'а, сообщение будет доставлено ПОВТОРНО после рестарта —
// внешний приёмник получит дубль. Дедупликации в Nexus нет; идемпотентность на
// стороне приёмника (в envelope для этого есть стабильный id).
func TestSender_Async_AtLeastOnce_RedeliveryAfterCrashBeforeCommit(t *testing.T) {
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
	cfg.Kafka.ConsumerGroup = "nexus-sender-alo-it"
	require.NoError(t, kafkapf.EnsureTopics(ctx, cfg, logging.NewNoop(), cfg.Kafka.AsyncTopic, cfg.Kafka.DLQTopic))

	stack := newAsyncStack(t, pool, cfg)
	defer stack.close()

	nodeUC := newAsyncNodeUsecase(t, ctx, pool, stack.cipher)
	createAsyncNode(t, ctx, nodeUC, "demo/alo", mock.URL+"/alo", "test.demo_alo", domain.NodeStatusEnabled)

	// Consumer #1: доставляет, но «падает» до commit'а.
	crashing := newDropCommitProcessor(stack.newProcessor())
	stop1 := stack.startConsumerWith(ctx, crashing)

	stack.produceEnvelope(t, ctx, "alo-1", "demo/alo", mock.URL+"/alo", `{"hello":"alo"}`)

	require.Equal(t, senderuc.HandleAck, crashing.firstResult(t),
		"доставка должна была пройти успешно — падение имитируем ПОСЛЕ неё")
	require.Equal(t, 1, rec.count("/alo"), "первая доставка ровно одна")

	stop1() // offset так и не закоммичен

	// Consumer #2: та же группа, обычный обработчик — читает с последнего
	// закоммиченного offset'а, то есть заново.
	stop2 := stack.startConsumerWith(ctx, stack.newProcessor())
	defer stop2()

	require.Eventually(t, func() bool { return rec.count("/alo") == 2 },
		90*time.Second, 200*time.Millisecond,
		"после рестарта незакоммиченное сообщение доставляется повторно (at-least-once)")

	// И ровно один дубль: повторная доставка коммитится штатно.
	time.Sleep(5 * time.Second)
	require.Equal(t, 2, rec.count("/alo"), "ровно один дубль — сообщение не крутится по кругу")
}
