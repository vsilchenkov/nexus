//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/config"
	"nexus/internal/platform/crypto"
	kafkapf "nexus/internal/platform/kafka"
	"nexus/internal/platform/logging"
	kafkaadapter "nexus/internal/sender/adapter/in/kafka"
	"nexus/internal/sender/adapter/out/httpclient"
	"nexus/internal/sender/adapter/out/nodepg"
	senderuc "nexus/internal/sender/usecase"
	pgrepo "nexus/internal/web/adapter/out/postgres"
	webuc "nexus/internal/web/usecase"
)

// asyncStack — собранный Sender-пайплайн для тестов async-потока. Позволяет
// поднимать consumer несколько раз подряд (та же consumer-group) — это нужно
// сценариям «рестарт сервиса».
type asyncStack struct {
	cfg      *config.Config
	pool     *pgxpool.Pool
	cipher   *crypto.Cipher
	producer *kafkapf.Producer
	logw     *capturingLogWriter
	logger   logging.Logger
}

func newAsyncStack(t *testing.T, pool *pgxpool.Pool, cfg *config.Config) *asyncStack {
	t.Helper()
	cipher, err := crypto.NewCipher(testEncryptionKey)
	require.NoError(t, err)
	logger := logging.NewNoop()
	return &asyncStack{
		cfg:      cfg,
		pool:     pool,
		cipher:   cipher,
		producer: kafkapf.NewProducer(cfg),
		logw:     &capturingLogWriter{},
		logger:   logger,
	}
}

// newProcessor собирает реальный AsyncProcessor (PG-reader + HTTP-клиент).
func (s *asyncStack) newProcessor(opts ...senderuc.AsyncOption) *senderuc.AsyncProcessor {
	nodeReader := nodepg.New(s.pool, s.cipher, s.logger)
	httpc := httpclient.New(&s.cfg.Sender.HTTPClient, s.logger, 64<<20)
	sendUC := senderuc.NewSendUsecase(httpc, s.logw, nil, s.logger, 64<<20)
	return senderuc.NewAsyncProcessor(nodeReader, sendUC, s.producer, nil, nil,
		s.cfg.Kafka.DLQTopic, nil, s.logger, opts...)
}

// startConsumer поднимает ConsumerGroup поверх реального AsyncProcessor.
// Возвращает функцию остановки — повторный вызов startConsumer = рестарт
// сервиса с той же consumer-group.
func (s *asyncStack) startConsumer(ctx context.Context, opts ...senderuc.AsyncOption) func() {
	return s.startConsumerWith(ctx, s.newProcessor(opts...))
}

// startConsumerWith — то же, но с произвольным обработчиком: тесты подменяют
// его декоратором, чтобы смоделировать сбой между доставкой и commit'ом.
func (s *asyncStack) startConsumerWith(ctx context.Context, proc asyncHandler) func() {
	return s.startConsumerGroup(ctx, proc).Stop
}

// startConsumerGroup возвращает саму группу — нужно тестам, которым интересен
// не только запуск/остановка, но и её состояние (например Snapshot с lag).
func (s *asyncStack) startConsumerGroup(ctx context.Context, proc asyncHandler) *kafkaadapter.ConsumerGroup {
	cg := kafkaadapter.NewConsumerGroup(s.cfg, s.cfg.Kafka.AsyncTopic, proc, s.logger)
	cg.Start(ctx)
	return cg
}

// asyncHandler — контракт обработчика сообщения, который принимает
// ConsumerGroup (интерфейс на его стороне unexported, поэтому дублируем здесь).
type asyncHandler interface {
	Handle(ctx context.Context, value []byte, headers map[string]string) senderuc.HandleResult
}

func (s *asyncStack) close() { _ = s.producer.Close() }

// produceEnvelope кладёт сообщение в nexus.async с ключом = path узла
// (как это делает Receiver — kafka.Hash партиционирует по нему).
func (s *asyncStack) produceEnvelope(t *testing.T, ctx context.Context, id, nodePath, targetURL, body string) {
	t.Helper()
	raw, err := json.Marshal(senderuc.Envelope{
		ID:         id,
		NodePath:   nodePath,
		Method:     http.MethodPost,
		TargetURL:  targetURL,
		Body:       []byte(body),
		ReceivedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	require.NoError(t, s.producer.Produce(ctx, s.cfg.Kafka.AsyncTopic, nodePath, raw, nil))
}

// newAsyncNodeUsecase — NodeUsecase для создания/переключения статуса узлов.
func newAsyncNodeUsecase(t *testing.T, ctx context.Context, pool *pgxpool.Pool, cipher *crypto.Cipher) *webuc.NodeUsecase {
	t.Helper()
	logger := logging.NewNoop()
	nodeRepo := pgrepo.NewNodeRepoPg(pool, cipher, logger)
	auditRepo := pgrepo.NewAuditRepoPg(pool, logger)
	uow := pgrepo.NewUnitOfWorkPg(pool, cipher, logger)
	auditUC := webuc.NewAuditUsecase(auditRepo, logger)
	teamRepo := pgrepo.NewTeamRepoPg(pool, logger)
	return webuc.NewNodeUsecase(nodeRepo, nopCache{}, auditUC, uow, teamRepo, nil, nil,
		time.Minute, 0, resolveDefaultTeamID(t, ctx, pool), nil, logger)
}

// setNodeStatus меняет статус узла напрямую в PG. Sender читает узел из PG на
// каждое сообщение (nodepg, без кеша) — изменение видно немедленно.
func setNodeStatus(t *testing.T, ctx context.Context, pool *pgxpool.Pool, path string, st domain.NodeStatus) {
	t.Helper()
	tag, err := pool.Exec(ctx, "UPDATE nodes SET status = $1 WHERE path = $2", string(st), path)
	require.NoError(t, err)
	require.EqualValues(t, 1, tag.RowsAffected(), "узел %s должен существовать", path)
}

// hitRecorder — mock внешнего узла, запоминающий порядок попаданий по пути.
type hitRecorder struct {
	mu   sync.Mutex
	path []string
}

func (h *hitRecorder) record(p string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.path = append(h.path, p)
}

func (h *hitRecorder) snapshot() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.path...)
}

func (h *hitRecorder) count(p string) int {
	n := 0
	for _, got := range h.snapshot() {
		if got == p {
			n++
		}
	}
	return n
}

// excluding — порядок попаданий без служебных (warmup) путей.
func (h *hitRecorder) excluding(skip string) []string {
	out := make([]string, 0, len(h.snapshot()))
	for _, got := range h.snapshot() {
		if got != skip {
			out = append(out, got)
		}
	}
	return out
}

// warmupConsumer доказывает, что consumer уже присоединился к группе и активно
// читает партицию: кладёт сообщение enabled-узла и ждёт его доставки.
//
// Без этого шага тесты недетерминированы: join+sync group занимает секунды, и
// «сообщение не доставлено» может означать не блокировку очереди, а то, что
// consumer ещё не начал читать — тест зеленел бы на сломанном коде.
func (s *asyncStack) warmupConsumer(t *testing.T, ctx context.Context, rec *hitRecorder, nodePath, targetURL, hitPath string) {
	t.Helper()
	s.produceEnvelope(t, ctx, "warmup-"+nodePath, nodePath, targetURL, `{"warmup":true}`)
	require.Eventually(t, func() bool { return rec.count(hitPath) >= 1 },
		90*time.Second, 200*time.Millisecond,
		"consumer должен присоединиться к группе и доставить warmup-сообщение")
}

func newHitServer(rec *hitRecorder) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
}

// createAsyncNode — узел requestAsync без ретраев и с логированием.
// chTable передаётся явно: имя таблицы CH ограничено набором символов, а path
// содержит слэш.
func createAsyncNode(t *testing.T, ctx context.Context, nodeUC *webuc.NodeUsecase, path, targetURL, chTable string, status domain.NodeStatus) *domain.Node {
	t.Helper()
	n := &domain.Node{
		Path:                    path,
		RootMethod:              domain.RootMethodRequestAsync,
		URLMode:                 domain.URLModeStatic,
		TargetURL:               targetURL,
		AuthType:                domain.AuthTypeNone,
		IncomingAuthType:        domain.IncomingAuthTypeNone,
		Status:                  status,
		LoggingEnabled:          true,
		ClickHouseTable:         chTable,
		ClickHouseRetentionDays: 30,
		TimeoutMs:               2000,
		RetryCount:              0,
		RetryBackoffMs:          50,
	}
	require.NoError(t, nodeUC.Create(ctx, webuc.SystemActor(), n))
	return n
}

// TestSender_Async_PausedDeliveredAfterUnpause (§3.6): сообщение paused-узла
// должно доставиться ПОСЛЕ снятия паузы — без рестарта Sender'а.
//
// Это репро бага: kafka-go FetchMessage без commit'а двигает внутренний курсор и
// сам по себе НЕ передоставляет сообщение на том же reader'е (см. комментарий в
// clog_retry_consumer.go). Старый код на HandleRetry просто шёл к следующему
// fetch — сообщение зависало до рестарта/ребаланса, и этот тест падал с hits==0.
// Фикс — retry-in-place: то же сообщение переобрабатывается до не-Retry.
func TestSender_Async_PausedDeliveredAfterUnpause(t *testing.T) {
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
	cfg.Kafka.ConsumerGroup = "nexus-sender-paused-it"
	require.NoError(t, kafkapf.EnsureTopics(ctx, cfg, logging.NewNoop(), cfg.Kafka.AsyncTopic, cfg.Kafka.DLQTopic))

	stack := newAsyncStack(t, pool, cfg)
	defer stack.close()

	nodeUC := newAsyncNodeUsecase(t, ctx, pool, stack.cipher)
	createAsyncNode(t, ctx, nodeUC, "demo/paused", mock.URL+"/paused", "test.demo_paused", domain.NodeStatusPaused)
	createAsyncNode(t, ctx, nodeUC, "demo/warmup", mock.URL+"/warmup", "test.demo_warmup", domain.NodeStatusEnabled)

	stop := stack.startConsumer(ctx, senderuc.WithPausedRetryAfter(200*time.Millisecond))
	defer stop()

	// Сначала убеждаемся, что consumer реально читает партицию, — иначе
	// «нет доставки» ниже ничего не доказывает.
	stack.warmupConsumer(t, ctx, rec, "demo/warmup", mock.URL+"/warmup", "/warmup")

	stack.produceEnvelope(t, ctx, "paused-1", "demo/paused", mock.URL+"/paused", `{"hello":"paused"}`)

	// Пока узел на паузе — доставки нет (несколько retry-циклов подряд).
	time.Sleep(3 * time.Second)
	require.Zero(t, rec.count("/paused"), "paused-узел не должен получать трафик")

	setNodeStatus(t, ctx, pool, "demo/paused", domain.NodeStatusEnabled)

	require.Eventually(t, func() bool { return rec.count("/paused") == 1 },
		60*time.Second, 200*time.Millisecond,
		"после снятия паузы сообщение должно доставиться БЕЗ рестарта consumer'а")

	// Ровно один раз: retry-in-place не должен продублировать доставку.
	time.Sleep(3 * time.Second)
	require.Equal(t, 1, rec.count("/paused"), "доставка ровно одна, дублей нет")
}

// TestSender_Async_PausedNotLostAcrossNeighborCommit — критический сценарий
// ПОТЕРИ сообщения. В одной партиции: msg1 узла A (paused) и msg2 узла B
// (enabled). Старый код пропускал msg1 (Retry без commit'а), доставлял msg2 и
// коммитил его offset — committed offset прокатывался МИМО msg1, и после
// ребаланса/рестарта msg1 терялся навсегда.
//
// Ожидаемое (исправленное) поведение: msg1 держит партицию до снятия паузы
// (head-of-line blocking — цена гарантии порядка), затем доставляются оба
// сообщения по порядку. Ничего не теряется.
func TestSender_Async_PausedNotLostAcrossNeighborCommit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	pool, pgCleanup := startPostgres(t, ctx)
	defer pgCleanup()
	brokers, kafkaCleanup := startKafka(t, ctx)
	defer kafkaCleanup()

	rec := &hitRecorder{}
	mock := newHitServer(rec)
	defer mock.Close()

	cfg := newKafkaTestConfig(brokers) // partitions=1 → оба сообщения в одной партиции
	cfg.Kafka.ConsumerGroup = "nexus-sender-noloss-it"
	require.NoError(t, kafkapf.EnsureTopics(ctx, cfg, logging.NewNoop(), cfg.Kafka.AsyncTopic, cfg.Kafka.DLQTopic))

	stack := newAsyncStack(t, pool, cfg)
	defer stack.close()

	nodeUC := newAsyncNodeUsecase(t, ctx, pool, stack.cipher)
	createAsyncNode(t, ctx, nodeUC, "demo/blocked", mock.URL+"/blocked", "test.demo_blocked", domain.NodeStatusPaused)
	createAsyncNode(t, ctx, nodeUC, "demo/healthy", mock.URL+"/healthy", "test.demo_healthy", domain.NodeStatusEnabled)
	createAsyncNode(t, ctx, nodeUC, "demo/warmup", mock.URL+"/warmup", "test.demo_warmup", domain.NodeStatusEnabled)

	stop := stack.startConsumer(ctx, senderuc.WithPausedRetryAfter(200*time.Millisecond))
	defer stop()

	stack.warmupConsumer(t, ctx, rec, "demo/warmup", mock.URL+"/warmup", "/warmup")

	// Порядок публикации важен: paused-сообщение идёт ПЕРВЫМ.
	stack.produceEnvelope(t, ctx, "blocked-1", "demo/blocked", mock.URL+"/blocked", `{"n":1}`)
	stack.produceEnvelope(t, ctx, "healthy-1", "demo/healthy", mock.URL+"/healthy", `{"n":2}`)

	// Пока голова партиции на паузе — НИЧЕГО не доставляется, включая
	// сообщение здорового узла. Именно здесь падал старый код: он доставлял
	// и коммитил /healthy, теряя /blocked.
	time.Sleep(5 * time.Second)
	require.Empty(t, rec.excluding("/warmup"),
		"paused-сообщение в голове партиции блокирует очередь; commit соседа = потеря")

	setNodeStatus(t, ctx, pool, "demo/blocked", domain.NodeStatusEnabled)

	require.Eventually(t, func() bool { return len(rec.excluding("/warmup")) == 2 },
		60*time.Second, 200*time.Millisecond,
		"после снятия паузы должны доставиться ОБА сообщения — ни одно не потеряно")

	require.Equal(t, []string{"/blocked", "/healthy"}, rec.excluding("/warmup"),
		"порядок партиции сохранён: разблокированная голова уходит первой")
}
