//go:build integration

package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/clickhouse"
	kafkapf "nexus/internal/platform/kafka"
	"nexus/internal/platform/logging"
	kafkaadapter "nexus/internal/sender/adapter/in/kafka"
	"nexus/internal/sender/adapter/out/chlog"
	"nexus/internal/sender/adapter/out/chlogretry"
	"nexus/internal/sender/clogwire"
)

// TestSender_CHRetryViaKafka — сквозной путь §38: ClickHouse недоступен →
// проваленный батч логов уходит в Kafka-топик nexus.logs.retry → retry-consumer
// дренит его обратно в ClickHouse после «восстановления». Реальные Kafka и CH
// в testcontainers.
//
// Фаза 1 (CH down): chlog.Writer с nil-conn провайдером (insertBatch падает) и
// Retrier → Kafka. Записи продьюсятся в retry-топик; в CH их ещё нет.
// Фаза 2 (CH up): ChLogRetryConsumer с инсертером поверх живого CH дренит топик;
// все записи появляются в CH (нулевая потеря). Битое сообщение в топике
// дропается (Ack), не ломая дренаж.
func TestSender_CHRetryViaKafka(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	broker, kCleanup := startKafka(t, ctx)
	defer kCleanup()

	chConn, chCfg, chCleanup := startClickHouse(t, ctx)
	defer chCleanup()

	const table = "nexus_default.test_retry_kafka"
	createNodeLogTable(t, ctx, chConn, table)

	logger := logging.NewNoop()

	cfg := newKafkaTestConfig(broker)
	cfg.Kafka.RetryTopic = "nexus.logs.retry"
	require.NoError(t, kafkapf.EnsureTopics(ctx, cfg, logger, cfg.Kafka.RetryTopic))

	producer := kafkapf.NewProducer(cfg)
	defer producer.Close()
	retrier := chlogretry.New(producer, cfg.Kafka.RetryTopic, cfg.Kafka.Topic.MaxMessageBytes, logger)

	// --- Фаза 1: CH «лежит» — провайдер с nil-conn, insert падает → retry в Kafka.
	const n = 12
	downWriter := chlog.NewWithRetrier(clickhouse.StaticProvider(nil), chCfg, retrier, nil, logger)
	for i := 0; i < n; i++ {
		downWriter.Write(ctx, table, retryRec(fmt.Sprintf("rec-%02d", i), int32(200+i)))
	}
	// Stop дренирует канал и делает финальный flushAll: insertBatch(nil)→ошибка →
	// retrier.Retry синхронно продьюсит батч в Kafka. После Stop всё в топике.
	downWriter.Stop(ctx)

	// Дополнительно кладём БИТОЕ сообщение — consumer обязан его дропнуть (Ack),
	// не застряв и не потеряв валидные батчи.
	require.NoError(t, producer.Produce(ctx, cfg.Kafka.RetryTopic, table, []byte("{not a valid envelope"), nil))

	// CH ещё пуст: down-writer не смог вставить (nil-conn).
	require.EqualValues(t, 0, chCount(t, ctx, chConn, table), "CH must be empty before retry-consumer drains")

	// --- Фаза 2: CH «поднялся» — consumer дренит топик в живой CH.
	upWriter := chlog.NewManagerWithRetrier(clickhouse.StaticProvider(chConn), chCfg, nil, nil, logger)
	defer upWriter.Stop(ctx)
	handler := kafkaadapter.NewChLogRetryHandler(upWriter, nil, logger)
	consumer := kafkaadapter.NewChLogRetryConsumer(cfg, cfg.Kafka.RetryTopic, "nexus-it-clog-retry", handler, logger)
	consumer.Start(ctx)
	defer consumer.Stop()

	// Ждём, пока все n записей просочатся в CH (consumer poll + insert + commit).
	deadline := time.Now().Add(90 * time.Second)
	var got uint64
	for time.Now().Before(deadline) {
		got = chCount(t, ctx, chConn, table)
		if got >= n {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	require.EqualValues(t, n, got, "all %d records must reach CH via Kafka retry (zero loss)", n)

	// Контроль формата конверта: то, что лежит в топике, декодируется обратно.
	// (Через ту же сериализацию, что использует Retrier.)
	wireSanityCheck(t, table)
}

// retryRec — минимальная валидная запись лога для §38-теста.
func retryRec(id string, status int32) *domain.LogRecord {
	now := time.Now().UTC().Truncate(time.Second)
	return &domain.LogRecord{
		ID:               id,
		Type:             domain.RootMethodRequest,
		URL:              "https://example.com/retry",
		Method:           "POST",
		Request:          `{"k":"v"}`,
		Response:         `{"ok":true}`,
		Status:           status,
		Reason:           "OK",
		DateCreate:       now,
		DateRequest:      now,
		DateResponse:     now.Add(10 * time.Millisecond),
		Duration:         10,
		Done:             true,
		ChecksumRequest:  strings.Repeat("a", 32),
		ChecksumResponse: strings.Repeat("b", 32),
		Host:             "h1",
		IP:               "127.0.0.1",
		Attempts:         1,
		AttemptsDetails:  "[]",
	}
}

func chCount(t *testing.T, ctx context.Context, conn chdriver.Conn, table string) uint64 {
	t.Helper()
	var n uint64
	require.NoError(t, conn.QueryRow(ctx, fmt.Sprintf("SELECT count() FROM %s", table)).Scan(&n))
	return n
}

// wireSanityCheck — Marshal/Unmarshal конверта совпадают (формат на проводе).
func wireSanityCheck(t *testing.T, table string) {
	t.Helper()
	b, err := clogwire.Marshal(table, []*domain.LogRecord{retryRec("x", 200)})
	require.NoError(t, err)
	env, err := clogwire.Unmarshal(b)
	require.NoError(t, err)
	require.Equal(t, table, env.Table)
	require.Len(t, env.Logs, 1)
}
