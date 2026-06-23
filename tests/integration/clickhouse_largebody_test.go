//go:build integration

package integration

import (
	"bytes"
	"context"
	"fmt"
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

// TestClickHouse_LargeBody_ClientFull_LogTruncated — §22, сценарий на большое тело.
//
// Параметр «Ограничить размер тела» (MaxBodySizeEnabled/MaxBodySize) усекает
// только КОПИЮ тела, сохраняемую в ClickHouse, и не влияет на ответ клиенту.
// Тест ставится на уровне SendUsecase → ClickHouse, где сходятся оба инварианта:
// SendOutput.Body — ровно то полное тело, которое Receiver вернёт наружу, а в
// CH пишется усечённая копия.
//
// Внешний узел возвращает 1 МБ. Проверяем:
//   - клиент получает ПОЛНЫЙ 1 МБ (SendOutput.Body байт-в-байт равен ответу);
//   - в логе response усечён до MaxBodySize рун + маркер «…(truncated)»;
//   - checksum_response посчитан по ПОЛНОМУ телу, не по усечённому.
func TestClickHouse_LargeBody_ClientFull_LogTruncated(t *testing.T) {
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

	const (
		id       = "33333333-0000-0000-0000-000000000001"
		maxRunes = 1000
	)
	in := senderuc.SendInput{
		ID:                 id,
		NodePath:           "largebody/test",
		RootMethod:         domain.RootMethodRequest,
		TargetURL:          "https://example.com/upstream",
		Method:             "POST",
		Body:               []byte(`{"req":"small"}`),
		TimeoutMs:          1000,
		ClickHouseTable:    table,
		LogRequestBody:     true,
		LogResponseBody:    true,
		LoggingEnabled:     true,
		MaxBodySizeEnabled: true,
		MaxBodySize:        maxRunes,
	}

	out := uc.Send(ctx, in)

	// Клиент получает ПОЛНОЕ тело (это и есть тело, которое Receiver отдаёт
	// наружу — на него лимит не распространяется).
	require.EqualValues(t, 200, out.StatusCode)
	require.Len(t, out.Body, size, "клиент должен получить полный 1 МБ")
	require.True(t, bytes.Equal(out.Body, respBody), "тело клиента байт-в-байт равно ответу апстрима")

	require.NoError(t, writer.Flush(ctx))

	// Ждём появления записи в ClickHouse.
	deadline := time.Now().Add(30 * time.Second)
	var n uint64
	for time.Now().Before(deadline) {
		require.NoError(t, conn.QueryRow(ctx,
			fmt.Sprintf("SELECT count() FROM %s WHERE ID = ?", table), id).Scan(&n))
		if n > 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.EqualValues(t, 1, n, "ожидаем ровно одну запись")

	rec, err := reader.GetByID(ctx, table, id)
	require.NoError(t, err)

	// В логе тело усечено до maxRunes рун + маркер.
	require.Equal(t, strings.Repeat("X", maxRunes)+"…(truncated)", rec.Response,
		"в логе response усечён до MaxBodySize рун + маркер")
	require.Less(t, len([]rune(rec.Response)), size, "сохранённое тело много меньше полного")

	// checksum считается по полному телу, не по усечённому.
	require.Equal(t, md5hexStr(respBody), rec.ChecksumResponse,
		"checksum_response — по полному телу")
}
