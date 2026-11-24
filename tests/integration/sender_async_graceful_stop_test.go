//go:build integration

package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	kafkapf "nexus/internal/platform/kafka"
	"nexus/internal/platform/logging"
)

// slowTarget — получатель, отвечающий с задержкой. Попадание фиксируется В
// НАЧАЛЕ обработки: так считает и 1С — оборванный клиентом вызов она обычно
// дорабатывает до конца, и запрос для неё уже состоялся.
type slowTarget struct {
	mu      sync.Mutex
	hits    int
	started chan struct{}
	once    sync.Once
}

func newSlowTarget(delay time.Duration) (*slowTarget, *httptest.Server) {
	st := &slowTarget{started: make(chan struct{})}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st.mu.Lock()
		st.hits++
		st.mu.Unlock()
		st.once.Do(func() { close(st.started) })
		time.Sleep(delay)
		w.WriteHeader(http.StatusOK)
	}))
	return st, srv
}

func (s *slowTarget) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits
}

// TestSender_Async_GracefulStopDoesNotRedeliver (§118.8): остановка Sender'а
// посреди медленной доставки больше не рождает повтор. До §118 отмена контекста
// приложения рвала HTTP-запрос, offset не коммитился, и после рестарта
// получатель получал то же сообщение второй раз — на каждом деплое.
func TestSender_Async_GracefulStopDoesNotRedeliver(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	pool, pgCleanup := startPostgres(t, ctx)
	defer pgCleanup()
	brokers, kafkaCleanup := startKafka(t, ctx)
	defer kafkaCleanup()

	target, srv := newSlowTarget(2 * time.Second)
	defer srv.Close()

	cfg := newKafkaTestConfig(brokers)
	cfg.Kafka.ConsumerGroup = "nexus-sender-graceful-it"
	require.NoError(t, kafkapf.EnsureTopics(ctx, cfg, logging.NewNoop(), cfg.Kafka.AsyncTopic, cfg.Kafka.DLQTopic))

	stack := newAsyncStack(t, pool, cfg)
	defer stack.close()
	nodeUC := newAsyncNodeUsecase(t, ctx, pool, stack.cipher)
	createAsyncNode(t, ctx, nodeUC, "demo/graceful", srv.URL+"/slow", "test.demo_graceful", domain.NodeStatusEnabled)

	// Экземпляр #1: «приложение», которое сейчас остановят.
	appCtx, cancelApp := context.WithCancel(ctx)
	cg := stack.startConsumerGroup(appCtx, stack.newProcessor())
	stack.produceEnvelope(t, ctx, "graceful-1", "demo/graceful", srv.URL+"/slow", `{"hello":"graceful"}`)

	select {
	case <-target.started:
	case <-time.After(90 * time.Second):
		t.Fatal("доставка так и не началась")
	}
	// Остановка посреди доставки: runner отменяет контекст приложения, затем
	// App.Stop дренирует доставки под потолком.
	cancelApp()
	budget, cancelBudget := context.WithTimeout(ctx, 30*time.Second)
	cg.Shutdown(budget)
	cancelBudget()
	require.Equal(t, 1, target.count())

	// Экземпляр #2: та же группа — «после рестарта». Доставка доиграна и
	// закоммичена, перечитывать нечего.
	stop2 := stack.startConsumerWith(ctx, stack.newProcessor())
	defer stop2()
	time.Sleep(10 * time.Second)
	require.Equal(t, 1, target.count(), "после мягкой остановки сообщение не доставляется повторно")
}
