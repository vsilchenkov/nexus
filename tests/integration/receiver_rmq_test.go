//go:build integration

// Сценарный e2e-тест узла RabbitMQAsync (§27.12): реальный RabbitMQ в
// testcontainers → Puller-воркер забирает сообщения → публикует в Kafka
// (здесь — fake-producer, ловит payload'ы) → ack в RabbitMQ. Проверяем:
// отсутствие потерь, корректный envelope (блок rmq, IP=rabbitmq://…), дренаж
// очереди. Kafka-контейнер не нужен — путь забора проверяется до Kafka, сам
// Kafka-конвейер покрыт receiver_async_test.go.
//
// Запуск: make test-integration (нужен Docker daemon).
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/testcontainers/testcontainers-go"
	tcrabbit "github.com/testcontainers/testcontainers-go/modules/rabbitmq"
	"github.com/testcontainers/testcontainers-go/wait"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/metrics"
	rmqadapter "nexus/internal/receiver/adapter/out/rabbitmq"
	"nexus/internal/receiver/usecase"
)

// captureProducer реализует usecase.AsyncProducer, накапливая payload'ы.
type captureProducer struct {
	mu      sync.Mutex
	values  [][]byte
	failNow bool
}

func (p *captureProducer) Produce(_ context.Context, _, _ string, value []byte, _ map[string]string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failNow {
		return fmt.Errorf("kafka unavailable")
	}
	cp := make([]byte, len(value))
	copy(cp, value)
	p.values = append(p.values, cp)
	return nil
}

func (p *captureProducer) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.values)
}

func startRabbitMQ(t *testing.T, ctx context.Context) (host string, port int, terminate func()) {
	t.Helper()
	c, err := tcrabbit.Run(ctx, "rabbitmq:3.13-management-alpine",
		testcontainers.WithWaitStrategy(
			wait.ForLog("Server startup complete").WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Fatalf("start rabbitmq: %v", err)
	}
	amqpURL, err := c.AmqpURL(ctx)
	if err != nil {
		t.Fatalf("amqp url: %v", err)
	}
	uri, err := amqp.ParseURI(amqpURL)
	if err != nil {
		t.Fatalf("parse amqp uri: %v", err)
	}
	return uri.Host, uri.Port, func() { _ = c.Terminate(ctx) }
}

func rmqTestNode(host string, port int, queue string) *domain.Node {
	n := &domain.Node{
		Path:        "billing-events",
		RootMethod:  domain.RootMethodRabbitMQAsync,
		TargetURL:   "https://api.partner.com/billing/webhook",
		RMQHost:     host,
		RMQPort:     int32(port),
		RMQUser:     "guest",
		RMQPassword: "guest",
		RMQQueue:    queue,
	}
	n.SetDefaults()
	n.PullIntervalSec = 1
	return n
}

// TestRMQPuller_E2E_NoLoss: публикуем N сообщений → воркер забирает все,
// публикует в (fake) Kafka, очередь дренируется, envelope содержит блок rmq.
func TestRMQPuller_E2E_NoLoss(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	host, port, terminate := startRabbitMQ(t, ctx)
	defer terminate()

	const queue = "billing.events.outbound"
	const n = 25
	publishMessages(t, ctx, host, port, queue, n)

	prod := &captureProducer{}
	worker := usecase.NewPullerWorker(
		rmqTestNode(host, port, queue),
		rmqadapter.NewConnector(), prod, "nexus.async", 10*1024*1024,
		metrics.New("receiver"), nil, logging.NewNoop())

	runCtx, runCancel := context.WithTimeout(ctx, 15*time.Second)
	defer runCancel()
	done := make(chan struct{})
	go func() { defer close(done); worker.Run(runCtx) }()

	// Ждём, пока все N сообщений будут опубликованы (без потерь).
	waitFor(t, 12*time.Second, func() bool { return prod.count() >= n })
	runCancel()
	<-done

	if got := prod.count(); got != n {
		t.Fatalf("want %d messages published, got %d", n, got)
	}
	// Envelope: блок rmq + IP=rabbitmq://… + method POST.
	var env usecase.Envelope
	if err := json.Unmarshal(prod.values[0], &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.RMQ == nil || env.Method != "POST" {
		t.Fatalf("envelope missing rmq block or wrong method: %+v", env)
	}
	if env.ClientIP == "" || env.ClientIP[:11] != "rabbitmq://" {
		t.Fatalf("client_ip should be rabbitmq://…, got %q", env.ClientIP)
	}

	// Очередь должна быть дренирована (все ack'нуты).
	assertQueueEmpty(t, ctx, host, port, queue)
}

// TestRMQPuller_E2E_KafkaDown_Requeue: при отказе Kafka сообщения возвращаются
// в очередь (nack requeue), потерь нет.
func TestRMQPuller_E2E_KafkaDown_Requeue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	host, port, terminate := startRabbitMQ(t, ctx)
	defer terminate()

	const queue = "kafka.down.queue"
	publishMessages(t, ctx, host, port, queue, 5)

	prod := &captureProducer{failNow: true} // Kafka «лежит»
	worker := usecase.NewPullerWorker(
		rmqTestNode(host, port, queue),
		rmqadapter.NewConnector(), prod, "nexus.async", 10*1024*1024,
		metrics.New("receiver"), nil, logging.NewNoop())

	runCtx, runCancel := context.WithTimeout(ctx, 5*time.Second)
	defer runCancel()
	done := make(chan struct{})
	go func() { defer close(done); worker.Run(runCtx) }()
	<-done

	if prod.count() != 0 {
		t.Fatalf("nothing should be published while kafka down, got %d", prod.count())
	}
	// Сообщения остались в очереди (requeue) — потерь нет.
	assertQueueDepth(t, ctx, host, port, queue, 5)
}

// captureSink реализует usecase.HealthSink, запоминая последний снимок и факт
// появления degraded.
type captureSink struct {
	mu           sync.Mutex
	last         domain.RMQHealth
	degradedSeen bool
}

func (s *captureSink) Publish(_ context.Context, h domain.RMQHealth) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last = h
	if h.Degraded {
		s.degradedSeen = true
	}
	return nil
}

func (s *captureSink) snapshot() (domain.RMQHealth, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last, s.degradedSeen
}

// TestRMQPuller_E2E_DegradedOnMissingQueue: RabbitMQ доступен, но очередь узла
// не существует → passive-declare даёт 404 на каждой попытке connect → после
// degradeAfter воркер помечает узел degraded (§27.4). Порог укорочен через
// SetDegradeAfter, чтобы не ждать 5 минут.
func TestRMQPuller_E2E_DegradedOnMissingQueue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	host, port, terminate := startRabbitMQ(t, ctx)
	defer terminate()

	// Очередь НЕ объявляем — её не существует.
	node := rmqTestNode(host, port, "queue.that.does.not.exist")
	sink := &captureSink{}
	worker := usecase.NewPullerWorker(
		node, rmqadapter.NewConnector(), &captureProducer{}, "nexus.async", 10*1024*1024,
		metrics.New("receiver"), sink, logging.NewNoop())
	worker.SetDegradeAfter(1 * time.Second) // не ждём дефолтные 5 минут

	runCtx, runCancel := context.WithTimeout(ctx, 30*time.Second)
	defer runCancel()
	done := make(chan struct{})
	go func() { defer close(done); worker.Run(runCtx) }()

	waitFor(t, 25*time.Second, func() bool { _, degraded := sink.snapshot(); return degraded })
	runCancel()
	<-done

	h, degraded := sink.snapshot()
	if !degraded {
		t.Fatal("worker did not report degraded for a missing queue")
	}
	if h.ConnState != domain.RMQConnDown {
		t.Fatalf("connection_state = %q, want down", h.ConnState)
	}
	if h.Reason == "" {
		t.Fatal("degraded health must carry a non-empty reason")
	}
	if h.Attempts < 1 {
		t.Fatalf("expected at least 1 failed attempt, got %d", h.Attempts)
	}
}

// --- amqp helpers ------------------------------------------------------------

func openChannel(t *testing.T, host string, port int) (*amqp.Connection, *amqp.Channel) {
	t.Helper()
	uri := amqp.URI{Scheme: "amqp", Host: host, Port: port, Username: "guest", Password: "guest", Vhost: "/"}
	conn, err := amqp.Dial(uri.String())
	if err != nil {
		t.Fatalf("amqp dial: %v", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("amqp channel: %v", err)
	}
	return conn, ch
}

func publishMessages(t *testing.T, ctx context.Context, host string, port int, queue string, n int) {
	t.Helper()
	conn, ch := openChannel(t, host, port)
	defer conn.Close()
	if _, err := ch.QueueDeclare(queue, true, false, false, false, nil); err != nil {
		t.Fatalf("queue declare: %v", err)
	}
	for i := 0; i < n; i++ {
		err := ch.PublishWithContext(ctx, "", queue, false, false, amqp.Publishing{
			ContentType: "application/json",
			MessageId:   fmt.Sprintf("msg-%d", i),
			Body:        []byte(fmt.Sprintf(`{"seq":%d}`, i)),
		})
		if err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}
}

func assertQueueEmpty(t *testing.T, ctx context.Context, host string, port int, queue string) {
	t.Helper()
	assertQueueDepth(t, ctx, host, port, queue, 0)
}

func assertQueueDepth(t *testing.T, _ context.Context, host string, port int, queue string, want int) {
	t.Helper()
	conn, ch := openChannel(t, host, port)
	defer conn.Close()
	q, err := ch.QueueDeclarePassive(queue, true, false, false, false, nil)
	if err != nil {
		t.Fatalf("queue passive declare: %v", err)
	}
	if q.Messages != want {
		t.Fatalf("queue depth = %d, want %d", q.Messages, want)
	}
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}
