//go:build integration

package integration

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/clickhouse"
	"nexus/internal/platform/logging"
	"nexus/internal/sender/adapter/out/chlog"
	senderuc "nexus/internal/sender/usecase"
	senderport "nexus/internal/sender/usecase/port"
	webch "nexus/internal/web/adapter/out/clickhouse"
)

// TestClickHouse_LargeBody_LimitDisabledFull_LimitEnabledRejects — §42/§43,
// сценарий большого тела на уровне SendUsecase → ClickHouse.
//
//   - Лимит ВЫКЛЮЧЕН (§42): большой ответ доезжает целиком — клиент получает
//     полное тело (SendOutput.Body байт-в-байт), в логе тоже полное.
//   - Лимит ВКЛЮЧЁН (§43): ответ больше max_body_size → клиенту 502 (тело НЕ
//     отдаётся), запись лога done=0 с reason, тело ответа в лог не пишется,
//     checksum по полному телу.
func TestClickHouse_LargeBody_LimitDisabledFull_LimitEnabledRejects(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	conn, cfg, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	const table = "nexus_default.test_largebody"
	createNodeLogTable(t, ctx, conn, table)

	logger := logging.NewNoop()
	provider := clickhouse.StaticProvider(conn)
	writer := chlog.New(provider, cfg, logger)
	defer writer.Stop(ctx)
	reader := webch.NewLogReader(provider, logger)

	const size = 1 * 1024 * 1024 // 1 МБ
	respBody := bytes.Repeat([]byte("X"), size)

	uc := senderuc.NewSendUsecase(
		stubHTTP{resp: &senderport.HTTPResponse{StatusCode: 200, Body: respBody}},
		writer, nil, logger,
	)

	base := func(id string) senderuc.SendInput {
		return senderuc.SendInput{
			ID:              id,
			NodePath:        "largebody/test",
			RootMethod:      domain.RootMethodRequest,
			TargetURL:       "https://example.com/upstream",
			Method:          "POST",
			Body:            []byte(`{"req":"small"}`),
			TimeoutMs:       1000,
			ClickHouseTable: table,
			LogRequestBody:  true,
			LogResponseBody: true,
			LoggingEnabled:  true,
		}
	}

	const (
		idFull   = "33333333-0000-0000-0000-000000000001"
		idReject = "33333333-0000-0000-0000-000000000002"
		maxRunes = 1000
	)

	// 1) Лимит выключен — клиент получает ПОЛНЫЙ 1 МБ ответ.
	outFull := uc.Send(ctx, base(idFull))
	require.EqualValues(t, 200, outFull.StatusCode)
	require.Len(t, outFull.Body, size, "без лимита клиент получает полный ответ")
	require.True(t, bytes.Equal(outFull.Body, respBody), "тело клиента байт-в-байт равно ответу апстрима")

	// 2) Лимит включён, ответ 1 МБ > maxRunes → 502, тело не отдаётся.
	inReject := base(idReject)
	inReject.MaxBodySizeEnabled = true
	inReject.MaxBodySize = maxRunes
	outReject := uc.Send(ctx, inReject)
	require.EqualValues(t, 502, outReject.StatusCode, "превышение ответа → 502")
	require.Empty(t, outReject.Body, "огромный ответ клиенту не отдаётся")

	require.NoError(t, writer.Flush(ctx))

	// Ждём обе записи.
	deadline := time.Now().Add(30 * time.Second)
	var n uint64
	for time.Now().Before(deadline) {
		require.NoError(t, conn.QueryRow(ctx, "SELECT count() FROM "+table).Scan(&n))
		if n >= 2 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.GreaterOrEqual(t, n, uint64(2))

	// Запись без лимита — полное тело сохранено.
	recFull, err := reader.GetByID(ctx, table, idFull)
	require.NoError(t, err)
	require.Len(t, []rune(recFull.Response), size, "без лимита в логе полное тело")

	// Запись с лимитом — done=0, reason, тело НЕ записано, checksum по полному.
	recReject, err := reader.GetByID(ctx, table, idReject)
	require.NoError(t, err)
	require.EqualValues(t, 502, recReject.Status)
	require.False(t, recReject.Done)
	require.Contains(t, recReject.Reason, "response body exceeds max_body_size")
	require.Empty(t, recReject.Response, "тело ответа в лог не пишется при превышении")
	require.Equal(t, md5hexStr(respBody), recReject.ChecksumResponse,
		"checksum_response по полному телу")
	require.NotEmpty(t, strings.TrimSpace(recReject.ChecksumResponse))
}
