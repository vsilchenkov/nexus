//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	kafka "github.com/segmentio/kafka-go"
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
	// cancel — набор отменённых оператором сообщений (§34.4). nil, если тесту
	// Redis не нужен; иначе processor обязан его учитывать, иначе отменённый
	// бэклог всё равно уедет во внешний узел.
	cancel senderuc.CancelSet
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
	return senderuc.NewAsyncProcessor(nodeReader, sendUC, s.producer, s.cancel, nil,
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

// startPausedSweeper поднимает sweeper delay-топика (§3.6) с коротким
// интервалом. Возвращает функцию остановки; повторный вызов = рестарт sweeper'а.
//
// Run завершается по отмене контекста (Stop лишь закрывает reader — так же
// устроен DLQ-репроцессор, и в проде ctx отменяет runner при shutdown), поэтому
// здесь заводим собственный производный ctx.
func (s *asyncStack) startPausedSweeper(ctx context.Context, interval time.Duration) func() {
	proc := s.newProcessor(senderuc.WithPausedRequeue(s.cfg.Kafka.PausedTopic))
	sw := kafkaadapter.NewPausedSweeper(s.cfg, proc, interval, 100, nil, s.logger)
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

// pausedRequeueOpt — опция основного consumer'а: переносить сообщения
// paused-узлов в delay-топик (как в проде, см. sender/app.go).
func (s *asyncStack) pausedRequeueOpt() senderuc.AsyncOption {
	return senderuc.WithPausedRequeue(s.cfg.Kafka.PausedTopic)
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

// setupPausedStack — общий сетап тестов delay-топика: PG + Kafka + mock-узел +
// stack с созданными топиками (включая PausedTopic).
func setupPausedStack(t *testing.T, ctx context.Context, group string) (*asyncStack, *hitRecorder, *httptest.Server, *pgxpool.Pool) {
	t.Helper()

	pool, pgCleanup := startPostgres(t, ctx)
	t.Cleanup(pgCleanup)
	brokers, kafkaCleanup := startKafka(t, ctx)
	t.Cleanup(kafkaCleanup)

	rec := &hitRecorder{}
	mock := newHitServer(rec)
	t.Cleanup(mock.Close)

	cfg := newKafkaTestConfig(brokers) // partitions=1 → все узлы в одной партиции
	cfg.Kafka.ConsumerGroup = group
	cfg.Kafka.PausedTopic = "nexus.async.paused"
	require.NoError(t, kafkapf.EnsureTopics(ctx, cfg, logging.NewNoop(),
		cfg.Kafka.AsyncTopic, cfg.Kafka.DLQTopic, cfg.Kafka.PausedTopic))

	stack := newAsyncStack(t, pool, cfg)
	t.Cleanup(stack.close)
	return stack, rec, mock, pool
}

// TestSender_Async_PausedRequeuedToDelayTopic (§3.6): сообщение paused-узла
// переезжает в delay-топик, и offset основного топика коммитится. Именно это
// освобождает партицию — проверяем через отдельный reader delay-топика.
func TestSender_Async_PausedRequeuedToDelayTopic(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	stack, rec, mock, pool := setupPausedStack(t, ctx, "nexus-sender-requeue-it")

	nodeUC := newAsyncNodeUsecase(t, ctx, pool, stack.cipher)
	createAsyncNode(t, ctx, nodeUC, "demo/paused", mock.URL+"/paused", "test.demo_paused", domain.NodeStatusPaused)
	createAsyncNode(t, ctx, nodeUC, "demo/warmup", mock.URL+"/warmup", "test.demo_warmup", domain.NodeStatusEnabled)

	// Только основной consumer, БЕЗ sweeper'а: проверяем сам факт переноса.
	stop := stack.startConsumer(ctx, stack.pausedRequeueOpt())
	defer stop()
	stack.warmupConsumer(t, ctx, rec, "demo/warmup", mock.URL+"/warmup", "/warmup")

	delayReader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:  []string{stack.cfg.Kafka.Brokers},
		Topic:    stack.cfg.Kafka.PausedTopic,
		GroupID:  "nexus-delay-watcher-it",
		MaxWait:  500 * time.Millisecond,
		MinBytes: 1,
		MaxBytes: 1 << 20,
	})
	defer delayReader.Close()

	stack.produceEnvelope(t, ctx, "paused-1", "demo/paused", mock.URL+"/paused", `{"hello":"paused"}`)

	fetchCtx, fetchCancel := context.WithTimeout(ctx, 90*time.Second)
	defer fetchCancel()
	msg, err := delayReader.FetchMessage(fetchCtx)
	require.NoError(t, err, "сообщение paused-узла обязано оказаться в delay-топике")

	hdrs := kafkaHeadersToMap(msg.Headers)
	require.Equal(t, "paused-1", hdrs["id"])
	require.Equal(t, "demo/paused", hdrs["node_path"])
	require.Equal(t, "nexus.async", hdrs["orig_topic"])
	require.NotEmpty(t, hdrs["paused_since"], "метка возраста бэклога проставлена")
	require.Equal(t, "demo/paused", string(msg.Key), "ключ = путь узла (порядок бэклога узла)")

	require.Zero(t, rec.count("/paused"), "на паузе внешний узел трафика не получает")
}

// TestSender_Async_PausedDoesNotBlockNeighbor — ГЛАВНАЯ цель фичи: сообщение
// узла на паузе не задерживает соседей по партиции.
//
// В одной партиции: msg1 узла A (paused) идёт ПЕРВЫМ, msg2 узла B (enabled) —
// вторым. Раньше (retry-in-place) msg1 держал партицию, и B молчал до снятия
// паузы; ещё раньше (до Phase 2) msg1 вообще терялся при коммите msg2.
// Теперь msg1 уезжает в delay-топик, а B доставляется немедленно.
func TestSender_Async_PausedDoesNotBlockNeighbor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	stack, rec, mock, pool := setupPausedStack(t, ctx, "nexus-sender-isolation-it")

	nodeUC := newAsyncNodeUsecase(t, ctx, pool, stack.cipher)
	createAsyncNode(t, ctx, nodeUC, "demo/blocked", mock.URL+"/blocked", "test.demo_blocked", domain.NodeStatusPaused)
	createAsyncNode(t, ctx, nodeUC, "demo/healthy", mock.URL+"/healthy", "test.demo_healthy", domain.NodeStatusEnabled)
	createAsyncNode(t, ctx, nodeUC, "demo/warmup", mock.URL+"/warmup", "test.demo_warmup", domain.NodeStatusEnabled)

	stop := stack.startConsumer(ctx, stack.pausedRequeueOpt())
	defer stop()
	stopSweep := stack.startPausedSweeper(ctx, time.Second)
	defer stopSweep()

	stack.warmupConsumer(t, ctx, rec, "demo/warmup", mock.URL+"/warmup", "/warmup")

	// Порядок публикации важен: paused-сообщение идёт ПЕРВЫМ.
	stack.produceEnvelope(t, ctx, "blocked-1", "demo/blocked", mock.URL+"/blocked", `{"n":1}`)
	stack.produceEnvelope(t, ctx, "healthy-1", "demo/healthy", mock.URL+"/healthy", `{"n":2}`)

	// Сосед доставляется, НЕ дожидаясь снятия паузы, — это и есть изоляция.
	require.Eventually(t, func() bool { return rec.count("/healthy") == 1 },
		60*time.Second, 200*time.Millisecond,
		"сообщение здорового узла не должно ждать paused-соседа по партиции")
	require.Zero(t, rec.count("/blocked"), "узел на паузе трафик не получает")

	setNodeStatus(t, ctx, pool, "demo/blocked", domain.NodeStatusEnabled)

	require.Eventually(t, func() bool { return rec.count("/blocked") == 1 },
		60*time.Second, 200*time.Millisecond,
		"после снятия паузы sweeper доставляет отложенное сообщение")

	time.Sleep(5 * time.Second)
	require.Equal(t, 1, rec.count("/blocked"), "ровно одна доставка, циркуляция не дублирует")
	require.Equal(t, 1, rec.count("/healthy"))
}

// TestSender_Async_PausedBacklogDeliveredAfterUnpause: накопленный за паузу
// бэклог уезжает после снятия паузы — целиком и без рестарта Sender'а.
func TestSender_Async_PausedBacklogDeliveredAfterUnpause(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	stack, rec, mock, pool := setupPausedStack(t, ctx, "nexus-sender-backlog-it")

	nodeUC := newAsyncNodeUsecase(t, ctx, pool, stack.cipher)
	createAsyncNode(t, ctx, nodeUC, "demo/paused", mock.URL+"/paused", "test.demo_paused", domain.NodeStatusPaused)
	createAsyncNode(t, ctx, nodeUC, "demo/warmup", mock.URL+"/warmup", "test.demo_warmup", domain.NodeStatusEnabled)

	stop := stack.startConsumer(ctx, stack.pausedRequeueOpt())
	defer stop()
	stopSweep := stack.startPausedSweeper(ctx, time.Second)
	defer stopSweep()

	stack.warmupConsumer(t, ctx, rec, "demo/warmup", mock.URL+"/warmup", "/warmup")

	const backlog = 5
	for i := 1; i <= backlog; i++ {
		stack.produceEnvelope(t, ctx, fmt.Sprintf("backlog-%d", i), "demo/paused",
			mock.URL+"/paused", fmt.Sprintf(`{"n":%d}`, i))
	}

	// Бэклог циркулирует в delay-топике, внешний узел молчит.
	time.Sleep(5 * time.Second)
	require.Zero(t, rec.count("/paused"), "пока пауза — доставки нет")

	setNodeStatus(t, ctx, pool, "demo/paused", domain.NodeStatusEnabled)

	require.Eventually(t, func() bool { return rec.count("/paused") == backlog },
		90*time.Second, 200*time.Millisecond,
		"после снятия паузы должен уехать ВЕСЬ бэклог")

	time.Sleep(5 * time.Second)
	require.Equal(t, backlog, rec.count("/paused"), "без дублей: каждое сообщение доставлено один раз")
}

// TestSender_Async_PausedBacklogSurvivesSweeperRestart: рестарт Sender'а посреди
// циркуляции не теряет бэклог. Дубли допустимы (at-least-once), потеря — нет.
func TestSender_Async_PausedBacklogSurvivesSweeperRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	stack, rec, mock, pool := setupPausedStack(t, ctx, "nexus-sender-sweeprestart-it")

	nodeUC := newAsyncNodeUsecase(t, ctx, pool, stack.cipher)
	createAsyncNode(t, ctx, nodeUC, "demo/paused", mock.URL+"/paused", "test.demo_paused", domain.NodeStatusPaused)
	createAsyncNode(t, ctx, nodeUC, "demo/warmup", mock.URL+"/warmup", "test.demo_warmup", domain.NodeStatusEnabled)

	stop := stack.startConsumer(ctx, stack.pausedRequeueOpt())
	defer stop()
	stack.warmupConsumer(t, ctx, rec, "demo/warmup", mock.URL+"/warmup", "/warmup")

	const backlog = 3
	for i := 1; i <= backlog; i++ {
		stack.produceEnvelope(t, ctx, fmt.Sprintf("restart-%d", i), "demo/paused",
			mock.URL+"/paused", fmt.Sprintf(`{"n":%d}`, i))
	}

	// Первый sweeper покрутил бэклог и остановился (рестарт сервиса).
	stopSweep := stack.startPausedSweeper(ctx, time.Second)
	time.Sleep(5 * time.Second)
	stopSweep()
	require.Zero(t, rec.count("/paused"), "на паузе доставки не было")

	setNodeStatus(t, ctx, pool, "demo/paused", domain.NodeStatusEnabled)

	// Новый sweeper той же группы обязан найти весь бэклог.
	stopSweep2 := stack.startPausedSweeper(ctx, time.Second)
	defer stopSweep2()

	require.Eventually(t, func() bool { return rec.count("/paused") >= backlog },
		90*time.Second, 200*time.Millisecond,
		"после рестарта sweeper'а бэклог не потерян")
}
