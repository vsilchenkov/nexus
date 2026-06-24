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

// TestClickHouse_LargeBody_TruncatesLog_And_TooLargeRejects — §22.2 + §43-rev на
// уровне SendUsecase → ClickHouse.
//
//   - max_body_size ВЫКЛЮЧЕН: большой ответ доезжает целиком (клиент + лог).
//   - max_body_size ВКЛЮЧЁН: клиент получает ПОЛНОЕ тело, в логе оно УСЕЧЕНО до
//     лимита (руны) + маркер; checksum по полному.
//   - ответ помечен TooLarge (превысил транспортный лимит config): клиенту 502,
//     лог done=0 + reason, тело/checksum не пишутся.
func TestClickHouse_LargeBody_TruncatesLog_And_TooLargeRejects(t *testing.T) {
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

	ucFull := senderuc.NewSendUsecase(
		stubHTTP{resp: &senderport.HTTPResponse{StatusCode: 200, Body: respBody}},
		writer, nil, logger, 64<<20,
	)
	ucTooLarge := senderuc.NewSendUsecase(
		stubHTTP{resp: &senderport.HTTPResponse{StatusCode: 200, TooLarge: true}},
		writer, nil, logger, 50_000,
	)

	base := func(id string) senderuc.SendInput {
		return senderuc.SendInput{
			ID: id, NodePath: "largebody/test", RootMethod: domain.RootMethodRequest,
			TargetURL: "https://example.com/upstream", Method: "POST", Body: []byte(`{"req":"small"}`),
			TimeoutMs: 1000, ClickHouseTable: table, LogRequestBody: true, LogResponseBody: true,
			LoggingEnabled: true,
		}
	}

	const (
		idFull   = "33333333-0000-0000-0000-000000000001"
		idTrunc  = "33333333-0000-0000-0000-000000000002"
		idReject = "33333333-0000-0000-0000-000000000003"
		maxRunes = 1000
	)

	// 1) Лимит выключен — клиент полный 1 МБ.
	outFull := ucFull.Send(ctx, base(idFull))
	require.EqualValues(t, 200, outFull.StatusCode)
	require.Len(t, outFull.Body, size, "без лимита клиент получает полный ответ")

	// 2) Лимит включён — клиент ПОЛНЫЙ ответ, лог усечён.
	inTrunc := base(idTrunc)
	inTrunc.MaxBodySizeEnabled = true
	inTrunc.MaxBodySize = maxRunes
	outTrunc := ucFull.Send(ctx, inTrunc)
	require.EqualValues(t, 200, outTrunc.StatusCode, "лимит на лог не меняет код")
	require.Len(t, outTrunc.Body, size, "клиент получает ПОЛНОЕ тело при включённом max_body_size")

	// 3) Ответ превысил транспортный лимит → 502.
	outReject := ucTooLarge.Send(ctx, base(idReject))
	require.EqualValues(t, 502, outReject.StatusCode, "TooLarge → 502")
	require.Empty(t, outReject.Body)

	require.NoError(t, writer.Flush(ctx))

	deadline := time.Now().Add(30 * time.Second)
	var n uint64
	for time.Now().Before(deadline) {
		require.NoError(t, conn.QueryRow(ctx, "SELECT count() FROM "+table).Scan(&n))
		if n >= 3 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.GreaterOrEqual(t, n, uint64(3))

	recFull, err := reader.GetByID(ctx, table, idFull)
	require.NoError(t, err)
	require.Len(t, []rune(recFull.Response), size, "без лимита в логе полное тело")

	recTrunc, err := reader.GetByID(ctx, table, idTrunc)
	require.NoError(t, err)
	require.EqualValues(t, 200, recTrunc.Status)
	require.True(t, recTrunc.Done)
	require.Equal(t, strings.Repeat("X", maxRunes)+"…(truncated)", recTrunc.Response, "лог усечён до лимита + маркер")
	require.Equal(t, md5hexStr(respBody), recTrunc.ChecksumResponse, "checksum по полному телу")

	recReject, err := reader.GetByID(ctx, table, idReject)
	require.NoError(t, err)
	require.EqualValues(t, 502, recReject.Status)
	require.False(t, recReject.Done)
	require.Contains(t, recReject.Reason, "exceeds transport limit")
	require.Empty(t, recReject.Response, "тело ответа не дочитано — в лог не пишется")
}
