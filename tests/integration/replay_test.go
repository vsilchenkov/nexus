//go:build integration

package integration

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/clickhouse"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
	"nexus/internal/sender/adapter/out/chlog"
	webch "nexus/internal/web/adapter/out/clickhouse"
	pgrepo "nexus/internal/web/adapter/out/postgres"
	webuc "nexus/internal/web/usecase"
	"nexus/internal/web/usecase/port"
)

// TestReplay_E2E_ClickHouse: записываем оригинальный log в ClickHouse, затем
// дёргаем ReplayUsecase — он должен:
//   - найти Node в PG;
//   - прочитать оригинальный log из CH через LogReader;
//   - вызвать ReceiverDispatcher с маркером __replay_of=<orig_id> в query и
//     оригинальным телом запроса;
//   - вернуть NewLogID и StatusCode из ответа dispatcher'а;
//   - записать audit-запись domain.ActionNodeReplay.
//
// ReceiverDispatcher здесь — capturingDispatcher (fake), потому что иначе
// тест превращается в boot-up всех 3 сервисов с HTTP-серверами. Шов Web→Receiver
// уже покрыт другими тестами; здесь интеграция = реальный CH + реальный PG
// + проход через ReplayUsecase.
//
// Покрывает §7.4.1 ТЗ (Replay).
func TestReplay_E2E_ClickHouse(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	pool, pgCleanup := startPostgres(t, ctx)
	defer pgCleanup()

	conn, chCfg, chCleanup := startClickHouse(t, ctx)
	defer chCleanup()

	logger := logging.NewNoop()
	cipher, _ := crypto.NewCipher(testEncryptionKey)

	// 1. Узел в PG.
	const chTable = "nexus_default.replay_e2e"
	nodeRepo := pgrepo.NewNodeRepoPg(pool, cipher, logger)
	auditRepo := pgrepo.NewAuditRepoPg(pool, logger)
	uow := pgrepo.NewUnitOfWorkPg(pool, cipher, logger)
	auditUC := webuc.NewAuditUsecase(auditRepo, logger)
	defaultTeam := resolveDefaultTeamID(t, ctx, pool)
	teamRepo := pgrepo.NewTeamRepoPg(pool, logger)
	nodeUC := webuc.NewNodeUsecase(nodeRepo, nopCache{}, auditUC, uow, teamRepo, nil, nil, time.Minute, 0, defaultTeam, logger)

	n := &domain.Node{
		Path:                    "demo/replay",
		RootMethod:              domain.RootMethodRequest,
		URLMode:                 domain.URLModeStatic,
		TargetURL:               "https://example.com/upstream",
		AuthType:                domain.AuthTypeNone,
		IncomingAuthType:        domain.IncomingAuthTypeNone,
		Status:                  domain.NodeStatusEnabled,
		ClickHouseTable:         chTable,
		ClickHouseRetentionDays: 30,
		TimeoutMs:               5000,
	}
	require.NoError(t, nodeUC.Create(ctx, webuc.SystemActor(), n))
	require.NotEmpty(t, n.ID)

	// 2. CH-таблица + одна "оригинальная" запись.
	createNodeLogTable(t, ctx, conn, chTable)

	provider := clickhouse.StaticProvider(conn)
	writer := chlog.New(provider, chCfg, logger)
	defer writer.Stop(ctx)

	now := time.Now().UTC().Truncate(time.Second)
	const origID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa1"
	orig := &domain.LogRecord{
		ID:               origID,
		Type:             domain.RootMethodRequest,
		URL:              "https://example.com/upstream",
		Method:           "POST",
		Parameters:       "x=1&y=2",
		Request:          `{"hello":"world"}`,
		Response:         `{"err":"boom"}`,
		Status:           500,
		DateCreate:       now,
		DateRequest:      now,
		DateResponse:     now.Add(50 * time.Millisecond),
		Duration:         50,
		Done:             false,
		ChecksumRequest:  strings.Repeat("a", 32),
		ChecksumResponse: strings.Repeat("b", 32),
		Host:             "h1",
		IP:               "127.0.0.1",
		Attempts:         1,
		AttemptsDetails:  "[]",
	}
	writer.Write(ctx, chTable, orig)
	require.NoError(t, writer.Flush(ctx))

	// Дождаться видимости записи в CH.
	deadline := time.Now().Add(20 * time.Second)
	var rowCount uint64
	for time.Now().Before(deadline) {
		row := conn.QueryRow(ctx, fmt.Sprintf("SELECT count() FROM %s WHERE ID = ?", chTable), origID)
		require.NoError(t, row.Scan(&rowCount))
		if rowCount >= 1 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.EqualValues(t, 1, rowCount)

	// 3. Replay через ReplayUsecase.
	logReader := webch.NewLogReader(provider, logger)
	dispatcher := &capturingDispatcher{
		response: &port.DispatchResponse{
			StatusCode: 200,
			Body:       []byte(`{"replayed":true}`),
			Headers:    map[string]string{"Content-Type": "application/json"},
		},
	}

	replayUC := webuc.NewReplayUsecase(logReader, nodeRepo, dispatcher, nil, auditUC, 10, logger)

	// UserID должен быть валидным UUID — колонка user_audit.user_id типа UUID,
	// SQL Write делает NULLIF($1,'')::uuid. Невалидное значение валит cast,
	// audit.Log() глушит ошибку (это by-design — сбой аудита не ломает
	// бизнес-операцию), и assert на entries=1 ниже падает с пустым списком.
	actor := webuc.Actor{
		UserID:    "11111111-1111-1111-1111-111111111111",
		UserLogin: "alice",
		IPAddress: "127.0.0.1",
	}
	result, err := replayUC.Replay(ctx, actor, origID, n.ID, "", webuc.ReplayOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, result.NewLogID, "replay must return new id")
	require.Equal(t, 200, result.StatusCode)
	require.Equal(t, `{"replayed":true}`, result.BodyPreview)

	// 4. Dispatcher: __replay_of в query + оригинальное тело.
	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()
	require.NotNil(t, dispatcher.req, "dispatcher must be called once")
	require.Equal(t, "demo/replay", dispatcher.req.NodePath)
	require.False(t, dispatcher.req.Async, "node.RootMethod=request → sync replay")
	require.Equal(t, "POST", dispatcher.req.Method)
	require.Equal(t, `{"hello":"world"}`, string(dispatcher.req.Body))
	require.Equal(t, origID, dispatcher.req.Query.Get("__replay_of"),
		"replay must inject __replay_of marker (§7.4.1)")
	// Оригинальные query-параметры должны сохраниться рядом с __replay_of.
	require.Equal(t, "1", dispatcher.req.Query.Get("x"))
	require.Equal(t, "2", dispatcher.req.Query.Get("y"))

	// 5. Audit: одна запись о replay привязана к log_id.
	entries, err := auditRepo.List(ctx, port.AuditFilter{TargetID: origID, Limit: 10})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, domain.ActionNodeReplay, entries[0].Action)
	require.Equal(t, "log", entries[0].TargetType)
	require.Equal(t, actor.UserID, entries[0].UserID)
}

// capturingDispatcher — port.ReceiverDispatcher, который запоминает первый
// вызов и возвращает заданный ответ. Используется, чтобы не поднимать
// реальный Receiver-HTTP-сервер.
type capturingDispatcher struct {
	mu       sync.Mutex
	req      *port.DispatchRequest
	response *port.DispatchResponse
}

func (c *capturingDispatcher) Dispatch(_ context.Context, req port.DispatchRequest) (*port.DispatchResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Копируем, чтобы не делиться слайсом Body со внутренним состоянием.
	clone := req
	c.req = &clone
	return c.response, nil
}
