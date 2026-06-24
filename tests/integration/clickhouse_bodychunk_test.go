//go:build integration

package integration

import (
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

// TestClickHouse_BodyPreviewAndChunk — §42: чтение тела логов срезами по рунам.
//
// Сохраняем запись без лимита (полное тело в CH), затем проверяем:
//   - GetByIDPreview отдаёт превью (первые N рун) + ПОЛНЫЕ длины тел;
//   - GetBodyChunk режет тело по рунам (substringUTF8) — срез и total корректны
//     на многобайтовом UTF-8;
//   - последовательная подгрузка чанками (как при скачивании) собирает тело
//     байт-в-байт.
func TestClickHouse_BodyPreviewAndChunk(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	conn, cfg, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	const table = "nexus_default.test_bodychunk"
	createNodeLogTable(t, ctx, conn, table)

	logger := logging.NewNoop()
	provider := clickhouse.StaticProvider(conn)
	writer := chlog.New(provider, cfg, logger)
	defer writer.Stop(ctx)
	reader := webch.NewLogReader(provider, logger)

	// Многобайтовое тело: 6 рун * 2000 = 12000 рун (ASCII + кириллица), чтобы
	// проверить нарезку именно по рунам, а не по байтам.
	reqBody := "REQ:" + strings.Repeat("aбвгдe", 100)
	respBody := strings.Repeat("abcдеж", 2000)
	respRunes := []rune(respBody)

	uc := senderuc.NewSendUsecase(
		stubHTTP{resp: &senderport.HTTPResponse{StatusCode: 200, Body: []byte(respBody)}},
		writer, nil, logger, 64<<20,
	)

	const id = "44444444-0000-0000-0000-000000000001"
	in := senderuc.SendInput{
		ID:              id,
		NodePath:        "bodychunk/test",
		RootMethod:      domain.RootMethodRequest,
		TargetURL:       "https://example.com/upstream",
		Method:          "POST",
		Body:            []byte(reqBody),
		TimeoutMs:       1000,
		ClickHouseTable: table,
		LogRequestBody:  true,
		LogResponseBody: true,
		LoggingEnabled:  true,
		// Без лимита — в CH ложится полное тело.
	}
	uc.Send(ctx, in)
	require.NoError(t, writer.Flush(ctx))

	// Ждём появления записи.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		_, _, respLen, err := reader.GetByIDPreview(ctx, table, id, 10)
		if err == nil && respLen > 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Превью: первые previewRunes рун + полные длины.
	const previewRunes = 100
	rec, reqLen, respLen, err := reader.GetByIDPreview(ctx, table, id, previewRunes)
	require.NoError(t, err)
	require.Equal(t, string([]rune(reqBody)[:previewRunes]), rec.Request, "превью request — первые N рун")
	require.Equal(t, string(respRunes[:previewRunes]), rec.Response, "превью response — первые N рун")
	require.EqualValues(t, len([]rune(reqBody)), reqLen, "полная длина request в рунах")
	require.EqualValues(t, len(respRunes), respLen, "полная длина response в рунах")

	// Срез по рунам из середины.
	chunk, total, err := reader.GetBodyChunk(ctx, table, id, "response", 6, 6)
	require.NoError(t, err)
	require.EqualValues(t, len(respRunes), total)
	require.Equal(t, string(respRunes[6:12]), chunk, "срез [6:12] по рунам")

	// which вне whitelist → ошибка.
	_, _, err = reader.GetBodyChunk(ctx, table, id, "headers", 0, 10)
	require.Error(t, err)

	// Последовательная сборка чанками (как при скачивании) == полное тело.
	var sb strings.Builder
	offset := 0
	const chunkSize = 1000
	for {
		c, tot, err := reader.GetBodyChunk(ctx, table, id, "response", offset, chunkSize)
		require.NoError(t, err)
		n := len([]rune(c))
		if n == 0 {
			break
		}
		sb.WriteString(c)
		offset += n
		if int64(offset) >= tot {
			break
		}
	}
	require.Equal(t, respBody, sb.String(), "сборка чанками равна полному телу")
}
