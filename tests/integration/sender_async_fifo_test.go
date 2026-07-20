//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	kafka "github.com/segmentio/kafka-go"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	kafkapf "nexus/internal/platform/kafka"
	"nexus/internal/platform/logging"
	webuc "nexus/internal/web/usecase"
)

// seqRecorder запоминает порядок seq из тел пришедших запросов.
type seqRecorder struct {
	mu   sync.Mutex
	seen []int
}

func (r *seqRecorder) add(n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, n)
}

func (r *seqRecorder) snapshot() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.seen...)
}

// newSeqServer — mock внешнего узла: пишет seq в recorder и отвечает статусом,
// который вернёт statusFor (позволяет «ломать» конкретное сообщение).
func newSeqServer(t *testing.T, rec *seqRecorder, statusFor func(seq int) int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var payload struct {
			Seq int `json:"seq"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		rec.add(payload.Seq)
		w.WriteHeader(statusFor(payload.Seq))
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
}

// createAsyncNodeWithRetry — как createAsyncNode, но с настраиваемыми ретраями.
func createAsyncNodeWithRetry(t *testing.T, ctx context.Context, nodeUC *webuc.NodeUsecase,
	path, targetURL, chTable string, retryCount, backoffMs int32,
) *domain.Node {
	t.Helper()
	n := &domain.Node{
		Path:                    path,
		RootMethod:              domain.RootMethodRequestAsync,
		URLMode:                 domain.URLModeStatic,
		TargetURL:               targetURL,
		AuthType:                domain.AuthTypeNone,
		IncomingAuthType:        domain.IncomingAuthTypeNone,
		Status:                  domain.NodeStatusEnabled,
		LoggingEnabled:          true,
		ClickHouseTable:         chTable,
		ClickHouseRetentionDays: 30,
		TimeoutMs:               2000,
		RetryCount:              retryCount,
		RetryBackoffMs:          backoffMs,
	}
	require.NoError(t, nodeUC.Create(ctx, webuc.SystemActor(), n))
	return n
}

// TestSender_Async_FIFO_SingleNodeOrder: сообщения одного узла доставляются
// строго в порядке публикации. Producer партиционирует по node_path
// (kafka.Hash), так что все сообщения узла лежат в одной партиции, а consumer
// обрабатывает её последовательно — порядок обязан сохраняться.
func TestSender_Async_FIFO_SingleNodeOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	pool, pgCleanup := startPostgres(t, ctx)
	defer pgCleanup()
	brokers, kafkaCleanup := startKafka(t, ctx)
	defer kafkaCleanup()

	rec := &seqRecorder{}
	mock := newSeqServer(t, rec, func(int) int { return http.StatusOK })
	defer mock.Close()

	cfg := newKafkaTestConfig(brokers)
	cfg.Kafka.ConsumerGroup = "nexus-sender-fifo-it"
	require.NoError(t, kafkapf.EnsureTopics(ctx, cfg, logging.NewNoop(), cfg.Kafka.AsyncTopic, cfg.Kafka.DLQTopic))

	stack := newAsyncStack(t, pool, cfg)
	defer stack.close()

	nodeUC := newAsyncNodeUsecase(t, ctx, pool, stack.cipher)
	createAsyncNode(t, ctx, nodeUC, "demo/fifo", mock.URL+"/fifo", "test.demo_fifo", domain.NodeStatusEnabled)

	stop := stack.startConsumer(ctx)
	defer stop()

	const total = 5
	for i := 1; i <= total; i++ {
		stack.produceEnvelope(t, ctx, fmt.Sprintf("fifo-%d", i), "demo/fifo",
			mock.URL+"/fifo", fmt.Sprintf(`{"seq":%d}`, i))
	}

	require.Eventually(t, func() bool { return len(rec.snapshot()) == total },
		90*time.Second, 200*time.Millisecond, "должны доставиться все сообщения")
	require.Equal(t, []int{1, 2, 3, 4, 5}, rec.snapshot(),
		"порядок публикации сохраняется при доставке")
}

// TestSender_Async_FIFO_HeadRetryBlocksButKeepsOrder: ошибка в первом сообщении
// задерживает остальные (head-of-line blocking), но не переупорядочивает их.
//
// Первое сообщение всегда получает 500 → retry_count=1 даёт 2 попытки → уходит в
// DLQ, и только после этого партиция продолжает разбираться. Ожидаемая
// последовательность попаданий: 1, 1, 2, 3, 4, 5.
func TestSender_Async_FIFO_HeadRetryBlocksButKeepsOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	pool, pgCleanup := startPostgres(t, ctx)
	defer pgCleanup()
	brokers, kafkaCleanup := startKafka(t, ctx)
	defer kafkaCleanup()

	rec := &seqRecorder{}
	mock := newSeqServer(t, rec, func(seq int) int {
		if seq == 1 {
			return http.StatusInternalServerError
		}
		return http.StatusOK
	})
	defer mock.Close()

	cfg := newKafkaTestConfig(brokers)
	cfg.Kafka.ConsumerGroup = "nexus-sender-fifo-retry-it"
	require.NoError(t, kafkapf.EnsureTopics(ctx, cfg, logging.NewNoop(), cfg.Kafka.AsyncTopic, cfg.Kafka.DLQTopic))

	stack := newAsyncStack(t, pool, cfg)
	defer stack.close()

	nodeUC := newAsyncNodeUsecase(t, ctx, pool, stack.cipher)
	createAsyncNodeWithRetry(t, ctx, nodeUC, "demo/fifo-retry", mock.URL+"/fifo-retry",
		"test.demo_fifo_retry", 1, 50)

	// Читатель DLQ — отдельная группа, чтобы не конкурировать с Sender'ом.
	dlqReader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:  []string{brokers},
		Topic:    cfg.Kafka.DLQTopic,
		GroupID:  "nexus-fifo-dlq-watcher-it",
		MaxWait:  500 * time.Millisecond,
		MinBytes: 1,
		MaxBytes: 1 << 20,
	})
	defer dlqReader.Close()

	stop := stack.startConsumer(ctx)
	defer stop()

	const total = 5
	for i := 1; i <= total; i++ {
		stack.produceEnvelope(t, ctx, fmt.Sprintf("fifo-retry-%d", i), "demo/fifo-retry",
			mock.URL+"/fifo-retry", fmt.Sprintf(`{"seq":%d}`, i))
	}

	// 6 попаданий: две попытки первого + по одной на остальные.
	require.Eventually(t, func() bool { return len(rec.snapshot()) == total+1 },
		120*time.Second, 200*time.Millisecond, "ожидаем 2 попытки первого сообщения и 4 остальных")
	require.Equal(t, []int{1, 1, 2, 3, 4, 5}, rec.snapshot(),
		"ретраи головы завершаются ДО обработки следующих сообщений; порядок не нарушен")

	// Первое сообщение должно уйти в DLQ после исчерпания ретраев.
	fetchCtx, fetchCancel := context.WithTimeout(ctx, 60*time.Second)
	defer fetchCancel()
	msg, err := dlqReader.FetchMessage(fetchCtx)
	require.NoError(t, err, "неудачное сообщение обязано оказаться в DLQ")

	hdrs := kafkaHeadersToMap(msg.Headers)
	require.Equal(t, "fifo-retry-1", hdrs["id"], "в DLQ ушло именно первое (сломанное) сообщение")
	require.Equal(t, "demo/fifo-retry", hdrs["node_path"])
	require.Contains(t, hdrs["reason"], "status=500")
	require.Contains(t, hdrs["reason"], "attempts=2")

	// Больше ничего не доставляется: очередь разобрана полностью, дублей нет.
	time.Sleep(5 * time.Second)
	require.Equal(t, []int{1, 1, 2, 3, 4, 5}, rec.snapshot(), "лишних доставок нет")
}
