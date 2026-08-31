//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	kafka "github.com/segmentio/kafka-go"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/clickhouse"
	"nexus/internal/platform/crypto"
	kafkapf "nexus/internal/platform/kafka"
	"nexus/internal/platform/logging"
	"nexus/internal/sender/adapter/out/chlog"
	senderuc "nexus/internal/sender/usecase"
	webch "nexus/internal/web/adapter/out/clickhouse"
	"nexus/internal/web/adapter/out/kafkaadmin"
	pgrepo "nexus/internal/web/adapter/out/postgres"
	webuc "nexus/internal/web/usecase"
	"nexus/internal/web/usecase/port"
)

// TestReplay_BodyFromQueue_E2E (§96): повтор берёт ПОЛНОЕ тело из оригинального
// конверта в Kafka, а не усечённую по max_body_size копию из ClickHouse.
//
// Это регрессия боевого инцидента 31.08.2026 (support/it_upr-vika): «Повторить
// все сейчас» отправлял журнальную копию — 50 000 рун плюс маркер
// `…(truncated)`, — приёмник получал оборванный JSON, отвечал 500, запись
// повторяли снова, и так бесконечно.
//
// Интеграция здесь настоящая в самом важном месте: конверт лежит в РЕАЛЬНОЙ
// Kafka, запись — в РЕАЛЬНОМ ClickHouse, и поиск идёт тем же kafkaadmin-клиентом,
// что работает в проде. Подменён только Receiver (capturingDispatcher) — шов
// Web→Receiver покрыт TestReplay_E2E_ClickHouse.
func TestReplay_BodyFromQueue_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	pool, pgCleanup := startPostgres(t, ctx)
	defer pgCleanup()
	conn, chCfg, chCleanup := startClickHouse(t, ctx)
	defer chCleanup()
	brokers, kafkaCleanup := startKafka(t, ctx)
	defer kafkaCleanup()

	logger := logging.NewNoop()
	cipher, _ := crypto.NewCipher(testEncryptionKey)

	const (
		chTable  = "nexus_default.replay_queue_e2e"
		nodePath = "demo/replay-queue"
		maxBody  = 100 // рун: заведомо меньше тела ниже
	)

	// 1. Узел с включённым лимитом тела — то есть в журнале будет КОПИЯ, а не тело.
	nodeRepo := pgrepo.NewNodeRepoPg(pool, cipher, logger)
	auditUC := webuc.NewAuditUsecase(pgrepo.NewAuditRepoPg(pool, logger), logger)
	uow := pgrepo.NewUnitOfWorkPg(pool, cipher, logger)
	defaultTeam := resolveDefaultTeamID(t, ctx, pool)
	teamRepo := pgrepo.NewTeamRepoPg(pool, logger)
	nodeUC := webuc.NewNodeUsecase(nodeRepo, nopCache{}, auditUC, uow, teamRepo, nil, nil, time.Minute, 0, defaultTeam, nil, logger)

	n := &domain.Node{
		Path:                    nodePath,
		RootMethod:              domain.RootMethodRequestAsync,
		URLMode:                 domain.URLModeStatic,
		TargetURL:               "https://example.com/upstream",
		AuthType:                domain.AuthTypeNone,
		IncomingAuthType:        domain.IncomingAuthTypeNone,
		IncomingMethod:          domain.HTTPMethodPOST,
		Status:                  domain.NodeStatusEnabled,
		LoggingEnabled:          true,
		LogRequestBody:          true,
		MaxBodySizeEnabled:      true,
		MaxBodySize:             maxBody,
		ClickHouseTable:         chTable,
		ClickHouseRetentionDays: 30,
		TimeoutMs:               5000,
		ForwardHeaders:          []string{"Content-Type"},
	}
	require.NoError(t, nodeUC.Create(ctx, webuc.SystemActor(), n))

	// 2. Конверт с ПОЛНЫМ телом — в DLQ, как его положил бы Sender после
	// исчерпания retry (§36.2). Тело заведомо длиннее max_body_size.
	fullBody := `{"payload":"` + strings.Repeat("z", 5000) + `"}`
	const origID = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbb1"
	receivedAt := time.Now().UTC().Add(-10 * time.Minute)

	cfg := newKafkaTestConfig(brokers)
	cfg.Kafka.ConsumerGroup = "nexus-replay-queue-it"
	require.NoError(t, kafkapf.EnsureTopics(ctx, cfg, logger, cfg.Kafka.AsyncTopic, cfg.Kafka.DLQTopic))

	producer := kafkapf.NewProducer(cfg)
	defer producer.Close()
	env := senderuc.Envelope{
		ID:         origID,
		NodePath:   nodePath,
		Method:     "POST",
		TargetURL:  "https://example.com/upstream",
		Headers:    map[string]string{"Content-Type": "application/json", "X-Skip-Me": "1"},
		Body:       []byte(fullBody),
		ReceivedAt: receivedAt,
	}
	publishEnvelope(t, ctx, producer, cfg.Kafka.DLQTopic, env)

	// 3. Журнальная копия — усечённая, ровно как её пишет Sender (§22.2).
	createNodeLogTable(t, ctx, conn, chTable)
	provider := clickhouse.StaticProvider(conn)
	writer := chlog.New(provider, chCfg, logger)
	defer writer.Stop(ctx)

	stored := string([]rune(fullBody)[:maxBody]) + domain.LogBodyTruncationMarker
	writer.Write(ctx, chTable, &domain.LogRecord{
		ID:              origID,
		Type:            domain.RootMethodRequestAsync,
		HTTPMethod:      "POST",
		URL:             "https://example.com/upstream",
		Request:         stored,
		RequestSize:     int64(len(fullBody)), // §42.10: истинный размер ДО усечения
		Status:          500,
		DateCreate:      receivedAt,
		DateRequest:     receivedAt,
		DateResponse:    receivedAt.Add(time.Second),
		Done:            false,
		NodeID:          n.ID,
		Attempts:        3,
		AttemptsDetails: "[]",
	})
	require.NoError(t, writer.Flush(ctx))
	waitLogVisible(t, ctx, conn, chTable, origID)

	// 4. Повтор: журнал читаем настоящий, конверты ищем настоящим клиентом.
	logReader := webch.NewLogReader(provider, logger)
	dispatcher := &capturingDispatcher{response: &port.DispatchResponse{
		StatusCode: 200, Body: []byte(`{"result":true,"id":"new-1"}`),
	}}
	admin := kafkaadmin.New(brokers, 5*time.Second, 30, logger)
	sources := []port.QueueSource{
		{Topic: cfg.Kafka.DLQTopic, Group: cfg.Kafka.DLQGroup(), Deep: true},
		{Topic: cfg.Kafka.AsyncTopic, Group: cfg.Kafka.ConsumerGroup},
	}
	replayUC := webuc.NewReplayUsecaseWithCancel(
		logReader, nodeRepo, dispatcher, nil, auditUC, 10,
		nil, teamRepo, time.Hour, logger,
		webuc.WithQueueOriginals(admin, sources),
	)

	res, err := replayUC.Replay(ctx, webuc.SystemActor(), origID, n.ID, "",
		webuc.ReplayOptions{UseNodeAuth: true})
	require.NoError(t, err)
	require.Equal(t, 200, res.StatusCode)

	dispatcher.mu.Lock()
	got := dispatcher.req
	dispatcher.mu.Unlock()
	require.NotNil(t, got, "диспетчер обязан быть вызван")
	require.Equal(t, fullBody, string(got.Body),
		"в приёмник обязано уйти полное тело конверта, а не журнальная копия")
	require.NotContains(t, string(got.Body), domain.LogBodyTruncationMarker)
	require.Equal(t, "application/json", got.Headers["Content-Type"],
		"§96.5: Content-Type восстанавливается из конверта — журнал заголовков не хранит")
	require.NotContains(t, got.Headers, "X-Skip-Me",
		"узел этот заголовок не пробрасывает, значит и повтор не должен")

	// 5. Тот же поиск после того, как репроцессор разобрал DLQ: конверт уходит
	// ПОЗАДИ committed offset его группы, и найти его может только глубокий
	// проход по времени (§96.3). Без него запись после истечения dlq_ttl стала
	// бы невосстановимой, хотя физически конверт жив до retention топика.
	commitDLQAsReprocessor(t, ctx, brokers, cfg.Kafka.DLQTopic, cfg.Kafka.DLQGroup())

	found, err := admin.FindOriginals(ctx, port.OriginalLookup{
		Sources:  sources,
		NodePath: nodePath,
		IDs:      []string{origID},
		Since:    receivedAt.Add(-time.Minute),
	})
	require.NoError(t, err)
	require.Contains(t, found, origID, "глубокий проход обязан найти закоммиченный конверт")
	require.Equal(t, fullBody, string(found[origID].Body))

	// 6. Без нижней границы по времени глубокий проход не запускается (скан
	// топика всех узлов «с начала» стоил бы дорого) — конверт не найден, и это
	// штатный промах, а не ошибка.
	none, err := admin.FindOriginals(ctx, port.OriginalLookup{
		Sources:  sources,
		NodePath: nodePath,
		IDs:      []string{origID},
	})
	require.NoError(t, err)
	require.Empty(t, none)
}

// publishEnvelope кладёт конверт в топик тем же ключом, что и Sender (key =
// node_path): по нему поиск отбирает сообщения узла ДО разбора тела.
func publishEnvelope(t *testing.T, ctx context.Context, p *kafkapf.Producer, topic string, env senderuc.Envelope) {
	t.Helper()
	raw, err := json.Marshal(env)
	require.NoError(t, err)
	require.NoError(t, p.Produce(ctx, topic, env.NodePath, raw, map[string]string{
		"id": env.ID, "node_path": env.NodePath,
	}))
}

// commitDLQAsReprocessor эмулирует разбор DLQ авто-репроцессором: читает всё и
// коммитит offset его группой.
func commitDLQAsReprocessor(t *testing.T, ctx context.Context, brokers, topic, group string) {
	t.Helper()
	r := kafka.NewReader(kafka.ReaderConfig{
		Brokers: []string{brokers}, Topic: topic, GroupID: group,
		MinBytes: 1, MaxBytes: 10 << 20, MaxWait: 500 * time.Millisecond,
	})
	defer r.Close()
	fetchCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	msg, err := r.FetchMessage(fetchCtx)
	require.NoError(t, err)
	require.NoError(t, r.CommitMessages(ctx, msg))
}

// waitLogVisible ждёт, пока запись станет видимой в ClickHouse.
func waitLogVisible(t *testing.T, ctx context.Context, conn chdriver.Conn, table, id string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var n uint64
		row := conn.QueryRow(ctx, "SELECT count() FROM "+table+" WHERE ID = ?", id)
		require.NoError(t, row.Scan(&n))
		if n >= 1 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("запись %s не появилась в %s", id, table)
}
