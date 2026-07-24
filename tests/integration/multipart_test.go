//go:build integration

package integration

import (
	"bytes"
	"context"
	"mime/multipart"
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

// buildIntegrationMultipart собирает валидное multipart/form-data тело
// (файл + текстовое поле) и его Content-Type с boundary.
func buildIntegrationMultipart(t *testing.T) (contentType string, body []byte) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", "invoice.pdf")
	require.NoError(t, err)
	_, err = fw.Write(bytes.Repeat([]byte("A"), 4096)) // «вложение» 4 КБ
	require.NoError(t, err)
	require.NoError(t, w.WriteField("comment", "process please"))
	require.NoError(t, w.Close())
	return w.FormDataContentType(), buf.Bytes()
}

// TestClickHouse_Multipart_PlaceholderNotStored — §68 на уровне SendUsecase →
// ClickHouse: тело multipart-запроса (и multipart-ответа) в CH НЕ хранится —
// вместо него плейсхолдер со сводкой частей; при этом request_size/response_size
// и checksum считаются по полному телу (вложение учтено). max_body_size на
// плейсхолдер не действует.
func TestClickHouse_Multipart_PlaceholderNotStored(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	conn, cfg, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	const table = "nexus_default.test_multipart"
	createNodeLogTable(t, ctx, conn, table)

	logger := logging.NewNoop()
	provider := clickhouse.StaticProvider(conn)
	writer := chlog.New(provider, cfg, logger)
	defer writer.Stop(ctx)
	reader := webch.NewLogReader(provider, logger)

	reqCT, reqBody := buildIntegrationMultipart(t)
	respCT, respBody := buildIntegrationMultipart(t)

	// Ответ с multipart Content-Type (проверяем симметрию §68 для ответов).
	uc := senderuc.NewSendUsecase(
		stubHTTP{resp: &senderport.HTTPResponse{
			StatusCode: 200,
			Headers:    map[string]string{"Content-Type": respCT},
			Body:       respBody,
		}},
		writer, nil, logger, 64<<20,
	)

	const (
		idReq  = "68000000-0000-0000-0000-000000000001"
		idResp = "68000000-0000-0000-0000-000000000002"
	)

	// 1) Multipart-ЗАПРОС + жёсткий max_body_size (должен игнорироваться).
	inReq := senderuc.SendInput{
		ID: idReq, NodePath: "multipart/test", RootMethod: domain.RootMethodRequest,
		TargetURL: "https://example.com/upload", Method: "POST",
		Headers:         map[string]string{"Content-Type": reqCT},
		Body:            reqBody,
		TimeoutMs:       1000,
		ClickHouseTable: table,
		LogRequestBody:  true, LogResponseBody: true,
		LoggingEnabled:     true,
		MaxBodySizeEnabled: true,
		MaxBodySize:        10, // маленький лимит — обычное тело был бы усечено
	}
	outReq := uc.Send(ctx, inReq)
	require.EqualValues(t, 200, outReq.StatusCode)
	require.Len(t, outReq.Body, len(respBody), "клиент получает полный multipart-ответ")

	// 2) Не-multipart запрос для контраста в той же таблице: его тело в CH
	//    хранится как есть (плейсхолдер подменяет только multipart).
	inResp := senderuc.SendInput{
		ID: idResp, NodePath: "multipart/test", RootMethod: domain.RootMethodRequest,
		TargetURL: "https://example.com/download", Method: "GET",
		Headers:         map[string]string{"Content-Type": "application/json"},
		Body:            []byte(`{"q":1}`),
		TimeoutMs:       1000,
		ClickHouseTable: table,
		LogRequestBody:  true, LogResponseBody: true,
		LoggingEnabled: true,
	}
	outResp := uc.Send(ctx, inResp)
	require.EqualValues(t, 200, outResp.StatusCode)

	require.NoError(t, writer.Flush(ctx))

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

	// Запрос: в логе плейсхолдер, размер/checksum по полному телу с вложением.
	recReq, err := reader.GetByID(ctx, table, idReq)
	require.NoError(t, err)
	require.True(t, domain.IsMultipartLogPlaceholder(recReq.Request), "тело запроса — плейсхолдер, не вложение")
	require.NotContains(t, recReq.Request, "AAAA", "содержимое вложения в CH не попадает")
	require.NotContains(t, recReq.Request, "…(truncated)", "плейсхолдер не режется по max_body_size")
	require.EqualValues(t, len(reqBody), recReq.RequestSize, "истинный размер — по полному телу с вложением")
	require.Equal(t, md5hexStr(reqBody), recReq.ChecksumRequest, "checksum по полному телу")

	// Ответ (первая запись тоже была с multipart-ответом): плейсхолдер + размер.
	require.True(t, domain.IsMultipartLogPlaceholder(recReq.Response), "тело ответа — плейсхолдер")
	require.EqualValues(t, len(respBody), recReq.ResponseSize)
	require.Equal(t, md5hexStr(respBody), recReq.ChecksumResponse)

	// Контраст: не-multipart запись хранит тело как есть.
	recResp, err := reader.GetByID(ctx, table, idResp)
	require.NoError(t, err)
	require.Equal(t, `{"q":1}`, recResp.Request, "обычное тело в CH как есть")
	require.False(t, domain.IsMultipartLogPlaceholder(recResp.Request))
}
