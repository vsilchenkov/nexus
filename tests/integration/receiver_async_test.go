//go:build integration

package integration

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tckafka "github.com/testcontainers/testcontainers-go/modules/kafka"

	"nexus/internal/domain"
	"nexus/internal/platform/config"
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

// TestSender_Async_E2E прогоняет полный путь async-обработки:
//
//	Producer → Kafka → ConsumerGroup → AsyncProcessor → SendUsecase → mock HTTP.
//
// Покрывает §3.6 / §4.2 / §10.2 ТЗ. Не запускает реальный Receiver — вместо
// него тест играет роль продюсера, который кладёт Envelope в `nexus.async`.
// Это правильное «вертикальное» покрытие Sender-стороны: всё, что после
// Receiver, должно работать end-to-end.
//
// Зависимости: Kafka + Postgres через testcontainers. ClickHouse заменён
// in-memory mock'ом log-writer'а, чтобы не тащить ещё один контейнер.
func TestSender_Async_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	pool, pgCleanup := startPostgres(t, ctx)
	defer pgCleanup()

	brokers, kafkaCleanup := startKafka(t, ctx)
	defer kafkaCleanup()

	logger := logging.NewNoop()
	cipher, _ := crypto.NewCipher(testEncryptionKey)

	// 1. Mock внешнего узла. Считаем количество вызовов — нужно ровно 1.
	var (
		mu       sync.Mutex
		hits     int32
		gotBody  []byte
		gotAuth  string
		gotPath  string
	)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		mu.Lock()
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer mock.Close()

	// 2. Узел в БД.
	nodeRepo := pgrepo.NewNodeRepoPg(pool, cipher, logger)
	auditRepo := pgrepo.NewAuditRepoPg(pool, logger)
	uow := pgrepo.NewUnitOfWorkPg(pool, cipher, logger)
	auditUC := webuc.NewAuditUsecase(auditRepo, logger)
	defaultTeam := resolveDefaultTeamID(t, ctx, pool)
	nodeUC := webuc.NewNodeUsecase(nodeRepo, nopCache{}, auditUC, uow, time.Minute, 0, defaultTeam, logger)

	n := &domain.Node{
		Path:                    "demo/async",
		RootMethod:              domain.RootMethodRequestAsync,
		URLMode:                 domain.URLModeStatic,
		TargetURL:               mock.URL + "/upstream",
		AuthType:                domain.AuthTypeToken,
		AuthCredentials:         "supersecret",
		IncomingAuthType:        domain.IncomingAuthTypeNone,
		Status:                  domain.NodeStatusEnabled,
		ClickHouseTable:         "test.demo_async",
		ClickHouseRetentionDays: 30,
		TimeoutMs:               5000,
		RetryCount:              0,
		RetryBackoffMs:          100,
	}
	if err := nodeUC.Create(ctx, webuc.SystemActor(), n); err != nil {
		t.Fatalf("create node: %v", err)
	}

	// 3. Минимальный config для kafka-обёрток.
	cfg := newKafkaTestConfig(brokers)

	// 4. Создаём топики (idempotent).
	if err := kafkapf.EnsureTopics(ctx, cfg, logger, cfg.Kafka.AsyncTopic, cfg.Kafka.DLQTopic); err != nil {
		t.Fatalf("ensure topics: %v", err)
	}

	// 5. Sender pipeline: NodeReader из PG + SendUsecase + AsyncProcessor.
	nodeReader := nodepg.New(pool, cipher, logger)
	httpc := httpclient.New(&cfg.Sender.HTTPClient, logger)
	logw := &capturingLogWriter{}
	sendUC := senderuc.NewSendUsecase(httpc, logw, nil, logger)

	producer := kafkapf.NewProducer(cfg)
	defer producer.Close()

	asyncProc := senderuc.NewAsyncProcessor(nodeReader, sendUC, producer, cfg.Kafka.DLQTopic, nil, logger)
	consumer := kafkaadapter.NewConsumerGroup(cfg, cfg.Kafka.AsyncTopic, asyncProc, logger)
	consumer.Start(ctx)
	defer consumer.Stop()

	// 6. Receiver pipeline: тот же реальный RouteAsyncUsecase, что в проде.
	//    Кладёт Envelope в Kafka через Producer; в Envelope попадает
	//    собранный AuthHeader и cleaned headers/query — это то, что
	//    sync-тест проверял sync-путь, а здесь — async.
	receiverReader := &fakeReader{repo: nodeRepo}
	routeAsyncUC := rcv.NewRouteAsyncUsecase(receiverReader, producer, cfg.Kafka.AsyncTopic, logger)

	res, err := routeAsyncUC.RouteAsync(ctx, rcv.RouteInput{
		NodePath: "demo/async",
		Method:   "POST",
		Header:   http.Header{"Content-Type": []string{"application/json"}},
		Query:    url.Values{},
		Body:     []byte(`{"hello":"async"}`),
		ClientIP: "127.0.0.1",
	})
	if err != nil {
		t.Fatalf("RouteAsync: %v", err)
	}
	if res.ID == "" {
		t.Fatal("empty envelope id")
	}

	// 7. Ждём обработки. Sender должен прочитать сообщение, выполнить HTTP-вызов,
	//    закоммитить offset. Таймаут — 60 sec (Kafka rebalance может занять время).
	deadline := time.Now().Add(60 * time.Second)
	for atomic.LoadInt32(&hits) == 0 && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("expected 1 upstream hit, got %d", got)
	}

	// 8. Verify request shape.
	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/upstream" {
		t.Fatalf("upstream path: %q", gotPath)
	}
	if string(gotBody) != `{"hello":"async"}` {
		t.Fatalf("upstream body: %q", string(gotBody))
	}
	if gotAuth != "Bearer supersecret" {
		t.Fatalf("upstream auth: %q", gotAuth)
	}

	// 9. Verify log writer получил запись с правильной таблицей.
	logw.mu.Lock()
	defer logw.mu.Unlock()
	if len(logw.records) != 1 {
		t.Fatalf("expected 1 log record, got %d", len(logw.records))
	}
	if logw.records[0].table != "test.demo_async" {
		t.Fatalf("log table: %q", logw.records[0].table)
	}
	if logw.records[0].rec.Status != http.StatusOK {
		t.Fatalf("log status: %d", logw.records[0].rec.Status)
	}
}

// startKafka поднимает Kafka (KRaft) через testcontainers и возвращает
// строку брокеров вида "host:port".
//
// Образ намеренно НЕ совпадает с deploy/docker-compose.yml (там
// apache/kafka:3.9.0). testcontainers-go/modules/kafka@v0.42 хардкодит
// в стартовый shell-скрипт пути из confluentinc/confluent-local
// (/etc/confluent/docker/{bash-config,configure,launch}); apache/kafka
// этих скриптов не содержит и контейнер падает с exit 127. Прод-параллель
// не страдает: тест проверяет наш wrapper Producer/ConsumerGroup поверх
// segmentio/kafka-go — а это library-level проверка, для которой
// конкретный дистрибутив Kafka неважен.
func startKafka(t *testing.T, ctx context.Context) (string, func()) {
	t.Helper()

	c, err := tckafka.Run(ctx, "confluentinc/confluent-local:7.5.0")
	if err != nil {
		t.Fatalf("start kafka: %v", err)
	}
	brokers, err := c.Brokers(ctx)
	if err != nil {
		_ = c.Terminate(ctx)
		t.Fatalf("kafka brokers: %v", err)
	}
	if len(brokers) == 0 {
		_ = c.Terminate(ctx)
		t.Fatalf("kafka returned 0 brokers")
	}
	cleanup := func() { _ = c.Terminate(ctx) }
	return brokers[0], cleanup
}

// newKafkaTestConfig — минимальный *config.Config с правдоподобными
// kafka/sender-секциями для тестов.
func newKafkaTestConfig(broker string) *config.Config {
	return &config.Config{
		Kafka: config.KafkaSection{
			Brokers:       broker,
			AsyncTopic:    "nexus.async",
			DLQTopic:      "nexus.async.dlq",
			ConsumerGroup: "nexus-sender-it",
			Topic: config.KafkaTopicSection{
				Partitions:        1,
				ReplicationFactor: 1,
				MinInsyncReplicas: 1,
				RetentionMs:       int64(time.Hour / time.Millisecond),
				RetentionBytes:    -1,
				SegmentMs:         int64(10 * time.Minute / time.Millisecond),
				CleanupPolicy:     "delete",
				CompressionType:   "producer",
				MaxMessageBytes:   1 << 20,
			},
			Producer: config.KafkaProducerSection{
				Acks:                "all",
				CompressionType:     "lz4",
				LingerMs:            10,
				BatchSize:           16 * 1024,
				EnableIdempotence:   true,
				MaxInFlightRequests: 5,
				RequestTimeoutMs:    10000,
				DeliveryTimeoutMs:   30000,
			},
			Consumer: config.KafkaConsumerSection{
				AutoOffsetReset:        "earliest",
				EnableAutoCommit:       false,
				FetchMinBytes:          1,
				FetchMaxBytes:          1 << 20,
				MaxPartitionFetchBytes: 1 << 20,
				SessionTimeoutMs:       10000,
				HeartbeatIntervalMs:    3000,
				MaxPollRecords:         100,
				IsolationLevel:         "read_uncommitted",
				Instances:              1,
			},
		},
		Sender: config.SenderSection{
			HTTPClient: config.SenderHTTPClientConfig{
				TimeoutMs:             5000,
				MaxIdleConns:          10,
				MaxIdleConnsPerHost:   5,
				IdleConnTimeoutSec:    30,
				DialTimeoutMs:         2000,
				TLSHandshakeTimeoutMs: 2000,
			},
		},
	}
}

// capturingLogWriter — in-memory port.LogWriter для assertion'ов.
type capturingLogWriter struct {
	mu      sync.Mutex
	records []captured
}

type captured struct {
	table string
	rec   *domain.LogRecord
}

func (c *capturingLogWriter) Write(_ context.Context, table string, rec *domain.LogRecord) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, captured{table: table, rec: rec})
}

func (c *capturingLogWriter) Flush(_ context.Context) error { return nil }
