//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/circuitbreaker"
	"nexus/internal/platform/clickhouse"
	"nexus/internal/platform/config"
	"nexus/internal/platform/logging"
	"nexus/internal/sender/adapter/out/chlog"
	"nexus/internal/sender/adapter/out/httpclient"
	"nexus/internal/sender/usecase"
)

// Сквозная проверка §55.4 на РЕАЛЬНЫХ ClickHouse и Redis: dry-run обязан
// дойти до target боевым путём и при этом не оставить следов.
//
// Unit-тесты проверяют гейты на стабах — здесь проверяется то, что стабом не
// поймать: что настоящий chlog.Writer ничего не вставил в таблицу узла и что
// настоящий circuitbreaker в Redis не сдвинулся. Ровно эти два следа и
// устроили бы инцидент (боевые логи + 503 живому трафику).

func dryRunSendCfg() *config.SenderHTTPClientConfig {
	return &config.SenderHTTPClientConfig{
		MaxIdleConns:          10,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeoutSec:    30,
		TLSHandshakeTimeoutMs: 5000,
		DialTimeoutMs:         5000,
	}
}

func dryRunInput(id, table, target string, dryRun bool) usecase.SendInput {
	return usecase.SendInput{
		ID:              id,
		NodePath:        "team/node",
		RootMethod:      domain.RootMethodRequest,
		TargetURL:       target,
		Method:          http.MethodPost,
		Body:            []byte(`{"test":true}`),
		TimeoutMs:       5000,
		ClickHouseTable: table,
		// Узел логирует — dry-run обязан НЕ писать вопреки этому (§55.4).
		LoggingEnabled: true,
		LogRequestBody: true,
		DryRun:         dryRun,
	}
}

// TestDryRun_LeavesNoTraces — боевой Send пишет лог и двигает breaker,
// dry-run по тому же коду — нет.
func TestDryRun_LeavesNoTraces(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	conn, chCfg, chCleanup := startClickHouse(t, ctx)
	defer chCleanup()
	redisClient, redisCleanup := startRedis(t, ctx)
	defer redisCleanup()

	const table = "nexus_default.test_dry_run_traces"
	createNodeLogTable(t, ctx, conn, table)

	var hits int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer target.Close()

	logger := logging.NewNoop()
	writer := chlog.New(clickhouse.StaticProvider(conn), chCfg, logger)
	defer writer.Stop(ctx)
	cb := circuitbreaker.New(redisClient, 3, time.Second)
	uc := usecase.NewSendUsecase(httpclient.New(dryRunSendCfg(), logger, 0), writer, cb, logger, 0)

	// --- Боевой вызов: след ЕСТЬ (иначе тест ниже ничего не доказывает).
	out := uc.Send(ctx, dryRunInput("00000000-0000-0000-0000-000000000001", table, target.URL, false))
	require.EqualValues(t, http.StatusOK, out.StatusCode)
	require.NoError(t, writer.Flush(ctx))

	deadline := time.Now().Add(20 * time.Second)
	var n uint64
	for time.Now().Before(deadline) {
		row := conn.QueryRow(ctx, fmt.Sprintf("SELECT count() FROM %s", table))
		require.NoError(t, row.Scan(&n))
		if n >= 1 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.EqualValues(t, 1, n, "боевой Send обязан записать лог — иначе проверка dry-run бессмысленна")

	// --- Dry-run: до target дошёл, следа НЕТ.
	out = uc.Send(ctx, dryRunInput("00000000-0000-0000-0000-000000000002", table, target.URL, true))
	require.EqualValues(t, http.StatusOK, out.StatusCode, "dry-run обязан реально вызвать target")
	assert.Equal(t, 2, hits, "target получил оба запроса — dry-run идёт боевым путём")
	require.NoError(t, writer.Flush(ctx))

	// Ждём, чтобы дать возможной записи просочиться: сразу после Flush её могло
	// бы не быть просто из-за батчинга, и тест прошёл бы по ложной причине.
	time.Sleep(2 * time.Second)
	row := conn.QueryRow(ctx, fmt.Sprintf("SELECT count() FROM %s", table))
	require.NoError(t, row.Scan(&n))
	assert.EqualValues(t, 1, n, "dry-run не должен писать в таблицу узла даже при logging_enabled=true")
}

// TestDryRun_DoesNotOpenBreaker — воспроизводит боевой сценарий vika_task:
// серия тестов по ЗАВИСШЕМУ адресу не имеет права открыть circuit breaker
// живого узла (§9.5/§50.4). Иначе диагностика инцидента усугубила бы инцидент:
// breaker начал бы отбивать реальный трафик 503-ми.
func TestDryRun_DoesNotOpenBreaker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	conn, chCfg, chCleanup := startClickHouse(t, ctx)
	defer chCleanup()
	redisClient, redisCleanup := startRedis(t, ctx)
	defer redisCleanup()

	const table = "nexus_default.test_dry_run_breaker"
	createNodeLogTable(t, ctx, conn, table)

	// Мёртвый адрес: соединение не устанавливается — как в боевом инциденте.
	const deadTarget = "http://127.0.0.1:1/hook"

	logger := logging.NewNoop()
	writer := chlog.New(clickhouse.StaticProvider(conn), chCfg, logger)
	defer writer.Stop(ctx)

	const threshold = 3
	cb := circuitbreaker.New(redisClient, threshold, 30*time.Second)
	uc := usecase.NewSendUsecase(httpclient.New(dryRunSendCfg(), logger, 0), writer, cb, logger, 0)

	// Серия dry-run по мёртвому адресу — больше порога breaker'а.
	for i := range threshold + 2 {
		in := dryRunInput(fmt.Sprintf("00000000-0000-0000-0000-00000000000%d", i), table, deadTarget, true)
		in.TimeoutMs = 1000
		out := uc.Send(ctx, in)
		require.EqualValues(t, 0, out.StatusCode, "мёртвый адрес — ответа нет")
	}

	st, err := cb.State(ctx, "team/node")
	require.NoError(t, err)
	assert.Equal(t, circuitbreaker.StateClosed, st,
		"breaker узла обязан остаться closed: dry-run по мёртвому адресу не должен резать боевой трафик")

	allowed, err := cb.Allow(ctx, "team/node")
	require.NoError(t, err)
	assert.True(t, allowed, "боевой трафик через узел продолжает проходить")

	// Контроль: тот же код БЕЗ dry_run breaker открывает — значит проверка выше
	// поймала бы забытый гейт, а не «breaker вообще не работает».
	for i := range threshold {
		in := dryRunInput(fmt.Sprintf("10000000-0000-0000-0000-00000000000%d", i), table, deadTarget, false)
		in.TimeoutMs = 1000
		in.LoggingEnabled = false // лог здесь не проверяем
		uc.Send(ctx, in)
	}
	st, err = cb.State(ctx, "team/node")
	require.NoError(t, err)
	assert.Equal(t, circuitbreaker.StateOpen, st,
		"боевые отказы breaker открывают — контроль того, что тест выше не ложно-зелёный")
}
