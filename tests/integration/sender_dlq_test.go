//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	kafka "github.com/segmentio/kafka-go"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/crypto"
	kafkapf "nexus/internal/platform/kafka"
	"nexus/internal/platform/logging"
	rcv "nexus/internal/receiver/usecase"
	kafkaadapter "nexus/internal/sender/adapter/in/kafka"
	"nexus/internal/sender/adapter/out/httpclient"
	"nexus/internal/sender/adapter/out/nodepg"
	senderuc "nexus/internal/sender/usecase"
	pgrepo "nexus/internal/web/adapter/out/postgres"
	webuc "nexus/internal/web/usecase"
)

// TestSender_Async_DLQ_E2E: внешний узел всегда отвечает 500, узел сконфигурирован
// с retry_count=2 → SendUsecase делает 3 попытки → все провалились → AsyncProcessor
// публикует исходный envelope в nexus.async.dlq и коммитит offset исходного
// сообщения (HandleDLQed). Тест отдельным kafka-reader'ом дожидается сообщения
// в DLQ-топике и проверяет headers (id/node_path/reason/last_attempt_at).
//
// Покрывает §3.6 / §5.3 ТЗ (DLQ after retry exhaustion).
func TestSender_Async_DLQ_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	pool, pgCleanup := startPostgres(t, ctx)
	defer pgCleanup()

	brokers, kafkaCleanup := startKafka(t, ctx)
	defer kafkaCleanup()

	logger := logging.NewNoop()
	cipher, _ := crypto.NewCipher(testEncryptionKey)

	// 1. Mock внешнего узла — всегда 500. Считаем число попыток.
	var (
		hits    int32
		gotPath atomic.Value
	)
	gotPath.Store("")
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		gotPath.Store(r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"err":"boom"}`))
	}))
	defer mock.Close()

	// 2. Узел с retry_count=2 (→ всего 3 попытки), backoff небольшой,
	// чтобы тест не растягивался.
	nodeRepo := pgrepo.NewNodeRepoPg(pool, cipher, logger)
	auditRepo := pgrepo.NewAuditRepoPg(pool, logger)
	uow := pgrepo.NewUnitOfWorkPg(pool, cipher, logger)
	auditUC := webuc.NewAuditUsecase(auditRepo, logger)
	defaultTeam := resolveDefaultTeamID(t, ctx, pool)
	teamRepo := pgrepo.NewTeamRepoPg(pool, logger)
	nodeUC := webuc.NewNodeUsecase(nodeRepo, nopCache{}, auditUC, uow, teamRepo, nil, nil, time.Minute, 0, defaultTeam, nil, logger)

	n := &domain.Node{
		Path:                    "demo/dlq",
		RootMethod:              domain.RootMethodRequestAsync,
		URLMode:                 domain.URLModeStatic,
		TargetURL:               mock.URL + "/upstream",
		AuthType:                domain.AuthTypeNone,
		IncomingAuthType:        domain.IncomingAuthTypeNone,
		Status:                  domain.NodeStatusEnabled,
		LoggingEnabled:          true, // §22: иначе лог в ClickHouse не пишется
		ClickHouseTable:         "test.demo_dlq",
		ClickHouseRetentionDays: 30,
		TimeoutMs:               2000,
		RetryCount:              2,
		RetryBackoffMs:          50,
	}
	require.NoError(t, nodeUC.Create(ctx, webuc.SystemActor(), n))

	cfg := newKafkaTestConfig(brokers)
	// Уникальная consumer-group, чтобы fresh-start с earliest и без
	// конфликта с предыдущим запуском в той же Kafka-сессии.
	cfg.Kafka.ConsumerGroup = "nexus-sender-dlq-it"

	require.NoError(t,
		kafkapf.EnsureTopics(ctx, cfg, logger, cfg.Kafka.AsyncTopic, cfg.Kafka.DLQTopic),
		"ensure topics")

	// 3. Sender pipeline.
	nodeReader := nodepg.New(pool, cipher, logger)
	httpc := httpclient.New(&cfg.Sender.HTTPClient, logger)
	logw := &capturingLogWriter{}
	sendUC := senderuc.NewSendUsecase(httpc, logw, nil, logger)

	producer := kafkapf.NewProducer(cfg)
	defer producer.Close()

	asyncProc := senderuc.NewAsyncProcessor(nodeReader, sendUC, producer, nil, cfg.Kafka.DLQTopic, nil, logger)
	consumer := kafkaadapter.NewConsumerGroup(cfg, cfg.Kafka.AsyncTopic, asyncProc, logger)
	consumer.Start(ctx)
	defer consumer.Stop()

	// 4. DLQ-reader — отдельная consumer-group, чтобы не конкурировать с
	// нашим Sender'ом (он publish'ит в DLQ, но сам его не читает).
	dlqReader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:  []string{brokers},
		Topic:    cfg.Kafka.DLQTopic,
		GroupID:  "nexus-dlq-watcher-it",
		MaxWait:  500 * time.Millisecond,
		MinBytes: 1,
		MaxBytes: 1 << 20,
	})
	defer dlqReader.Close()

	// 5. Receiver путь: RouteAsync публикует envelope в nexus.async.
	routeAsyncUC := rcv.NewRouteAsyncUsecase(&fakeReader{repo: nodeRepo}, producer, cfg.Kafka.AsyncTopic, 5, logger)
	res, err := routeAsyncUC.RouteAsync(ctx, rcv.RouteInput{
		NodePath: "demo/dlq",
		Method:   "POST",
		Header:   http.Header{"Content-Type": []string{"application/json"}},
		Query:    url.Values{},
		Body:     []byte(`{"hello":"dlq"}`),
		ClientIP: "127.0.0.1",
	})
	require.NoError(t, err)
	require.NotEmpty(t, res.ID)

	// 6. Ждём сообщение в DLQ. FetchMessage блокирует — отдельный ctx
	// с собственным timeout'ом, чтобы тест не висел весь deadline.
	fetchCtx, fetchCancel := context.WithTimeout(ctx, 120*time.Second)
	defer fetchCancel()
	msg, err := dlqReader.FetchMessage(fetchCtx)
	require.NoError(t, err, "fetch DLQ message")
	require.NoError(t, dlqReader.CommitMessages(ctx, msg))

	// 7. Mock должен был получить ровно retry_count+1 = 3 попытки.
	require.EqualValues(t, 3, atomic.LoadInt32(&hits), "expected 3 upstream attempts")
	require.Equal(t, "/upstream", gotPath.Load().(string))

	// 8. Headers DLQ-сообщения.
	hdrs := kafkaHeadersToMap(msg.Headers)
	require.Equal(t, res.ID, hdrs["id"], "DLQ header 'id' must match envelope id")
	require.Equal(t, "demo/dlq", hdrs["node_path"])
	require.Equal(t, "nexus.async", hdrs["orig_topic"])
	require.Contains(t, hdrs["reason"], "status=500", "DLQ reason must contain HTTP status")
	require.Contains(t, hdrs["reason"], "attempts=3", "DLQ reason must contain attempt count")
	require.NotEmpty(t, hdrs["last_attempt_at"])

	// 9. Value DLQ-сообщения = тот же envelope, что лежал в исходном топике.
	var env senderuc.Envelope
	require.NoError(t, json.Unmarshal(msg.Value, &env))
	require.Equal(t, res.ID, env.ID)
	require.Equal(t, "demo/dlq", env.NodePath)
	require.True(t, strings.HasSuffix(env.TargetURL, "/upstream"))
	require.Equal(t, `{"hello":"dlq"}`, string(env.Body))

	// 10. Логи: 1 запись со Status=500, done=false, Attempts=3.
	logw.mu.Lock()
	defer logw.mu.Unlock()
	require.Len(t, logw.records, 1)
	rec := logw.records[0]
	require.Equal(t, "test.demo_dlq", rec.table)
	require.EqualValues(t, 500, rec.rec.Status)
	require.False(t, rec.rec.Done)
	require.EqualValues(t, 3, rec.rec.Attempts)
}

// kafkaHeadersToMap — берём первое значение на ключ (как делает Sender).
func kafkaHeadersToMap(in []kafka.Header) map[string]string {
	out := make(map[string]string, len(in))
	for _, h := range in {
		if _, exists := out[h.Key]; !exists {
			out[h.Key] = string(h.Value)
		}
	}
	return out
}
