//go:build integration

package integration

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	chgo "github.com/ClickHouse/clickhouse-go/v2"
	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"nexus/internal/domain"
	"nexus/internal/platform/clickhouse"
	"nexus/internal/platform/config"
	"nexus/internal/platform/logging"
	"nexus/internal/sender/adapter/out/chlog"
	senderuc "nexus/internal/sender/usecase"
	senderport "nexus/internal/sender/usecase/port"
	webch "nexus/internal/web/adapter/out/clickhouse"
	"nexus/internal/web/usecase/port"
)

// stubHTTP — фиксированный ответ внешнего узла для сквозных тестов логирования.
type stubHTTP struct {
	resp *senderport.HTTPResponse
}

func (s stubHTTP) Do(_ context.Context, _ *senderport.HTTPRequest) (*senderport.HTTPResponse, error) {
	return s.resp, nil
}

// startClickHouse поднимает CH 24-alpine через generic testcontainer и
// возвращает готовое driver.Conn + cleanup. База "nexus_default" создаётся
// сразу (по умолчанию в Sender writer/LogReader работают с db.table-нотацией).
func startClickHouse(t *testing.T, ctx context.Context) (chdriver.Conn, *config.ClickHouseSection, func()) {
	t.Helper()

	req := testcontainers.ContainerRequest{
		Image: "clickhouse/clickhouse-server:24-alpine",
		ExposedPorts: []string{
			"9000/tcp", // native TCP
		},
		Env: map[string]string{
			"CLICKHOUSE_DB":                        "nexus_default",
			"CLICKHOUSE_USER":                      "default",
			"CLICKHOUSE_PASSWORD":                  "",
			"CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT": "1",
		},
		// CH alpine иногда логирует "Ready for connections." с точкой, иногда
		// без — ловим обоими шаблонами через простой ForListeningPort на native TCP.
		WaitingFor: wait.ForListeningPort("9000/tcp").
			WithStartupTimeout(180 * time.Second),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	require.NoError(t, err, "start clickhouse")

	host, err := c.Host(ctx)
	require.NoError(t, err)
	port, err := c.MappedPort(ctx, "9000/tcp")
	require.NoError(t, err)

	cfg := &config.ClickHouseSection{
		Host:             host,
		Port:             int(port.Num()),
		Database:         "nexus_default",
		User:             "default",
		Password:         "",
		BatchSize:        10,
		FlushIntervalSec: 1,
		BufferMaxSize:    1024,
		Workers:          1,
	}

	conn, err := chgo.Open(&chgo.Options{
		Addr: []string{fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)},
		Auth: chgo.Auth{Database: cfg.Database, Username: cfg.User, Password: cfg.Password},
	})
	require.NoError(t, err)

	// Listening-port готовится раньше, чем CH принимает native-handshake.
	// Делаем активный retry до 60 сек.
	deadline := time.Now().Add(60 * time.Second)
	var pingErr error
	for time.Now().Before(deadline) {
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		pingErr = conn.Ping(pingCtx)
		cancel()
		if pingErr == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	require.NoError(t, pingErr, "clickhouse ping")

	cleanup := func() {
		_ = conn.Close()
		_ = c.Terminate(ctx)
	}
	return conn, cfg, cleanup
}

// createNodeLogTable создаёт MergeTree-таблицу по схеме §4.3 ТЗ.
func createNodeLogTable(t *testing.T, ctx context.Context, conn chdriver.Conn, table string) {
	t.Helper()
	ddl := fmt.Sprintf(`CREATE TABLE %s (
		ID String,
		type String,
		http_method String,
		url String,
		method String,
		parameters String,
		request String,
		response String,
		status Int32,
		reason String,
		date_create Date,
		date_request DateTime,
		date_response DateTime,
		duration Int32,
		done UInt8,
		checksum_request FixedString(32),
		checksum_response FixedString(32),
		Host String,
		IP String,
		attempts Int32,
		attempts_details String,
		node_id String
	) ENGINE = MergeTree
	PARTITION BY toYYYYMM(date_create)
	ORDER BY (date_create, date_request, method)`, table)
	require.NoError(t, conn.Exec(ctx, ddl), "create log table %s", table)
}

// TestClickHouse_WriteAndRead — sender.chlog.Writer пишет батч; затем
// LogReaderCH из Web-стороны видит запись через GetByID и Search.
//
// Покрывает §4.3 (batch insert) и §7.4 (поиск/чтение записей) ТЗ
// в одном end-to-end сценарии.
func TestClickHouse_WriteAndRead(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	conn, cfg, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	const table = "nexus_default.test_e2e"
	createNodeLogTable(t, ctx, conn, table)

	logger := logging.NewNoop()
	provider := clickhouse.StaticProvider(conn)
	writer := chlog.New(provider, cfg, logger)
	defer writer.Stop(ctx)

	now := time.Now().UTC().Truncate(time.Second)

	makeRec := func(id, ip, host string, status int32, done bool) *domain.LogRecord {
		return &domain.LogRecord{
			ID:               id,
			Type:             domain.RootMethodRequest,
			URL:              "https://example.com/upstream",
			Method:           "POST",
			Parameters:       "k=v",
			Request:          `{"hello":"world"}`,
			Response:         `{"ok":true}`,
			Status:           status,
			Reason:           "",
			DateCreate:       now,
			DateRequest:      now,
			DateResponse:     now.Add(50 * time.Millisecond),
			Duration:         50,
			Done:             done,
			ChecksumRequest:  strings.Repeat("a", 32),
			ChecksumResponse: strings.Repeat("b", 32),
			Host:             host,
			IP:               ip,
			Attempts:         1,
			AttemptsDetails:  "[]",
		}
	}

	writer.Write(ctx, table, makeRec("00000000-0000-0000-0000-000000000001", "127.0.0.1", "h1", 200, true))
	writer.Write(ctx, table, makeRec("00000000-0000-0000-0000-000000000002", "10.0.0.1", "h2", 500, false))
	writer.Write(ctx, table, makeRec("00000000-0000-0000-0000-000000000003", "127.0.0.1", "h1", 200, true))

	require.NoError(t, writer.Flush(ctx))

	// Дожидаемся пока batch insert просочится в систему — небольшой запас
	// под flushAll race с background-горутинами.
	deadline := time.Now().Add(20 * time.Second)
	var n uint64
	for time.Now().Before(deadline) {
		row := conn.QueryRow(ctx, fmt.Sprintf("SELECT count() FROM %s", table))
		require.NoError(t, row.Scan(&n))
		if n >= 3 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.EqualValues(t, 3, n, "expected 3 rows in %s", table)

	reader := webch.NewLogReader(provider, logger)

	rec, err := reader.GetByID(ctx, table, "00000000-0000-0000-0000-000000000001")
	require.NoError(t, err)
	require.Equal(t, "https://example.com/upstream", rec.URL)
	require.EqualValues(t, 200, rec.Status)
	require.True(t, rec.Done)
	require.Equal(t, "127.0.0.1", rec.IP)

	_, err = reader.GetByID(ctx, table, "00000000-0000-0000-0000-00000000ffff")
	require.ErrorIs(t, err, domain.ErrNotFound)

	// Search — фильтрация по status=err и по IP.
	got, err := reader.Search(ctx, port.LogQuery{Table: table, Status: "err", Limit: 50})
	require.NoError(t, err)
	require.Len(t, got, 1, "one error row expected")
	require.EqualValues(t, 500, got[0].Status)
	require.False(t, got[0].Done)

	got, err = reader.Search(ctx, port.LogQuery{Table: table, IP: "127.0.0.1", Limit: 50})
	require.NoError(t, err)
	require.Len(t, got, 2, "two rows from 127.0.0.1")

	got, err = reader.Search(ctx, port.LogQuery{Table: table, Q: "hello", Limit: 50})
	require.NoError(t, err)
	require.Len(t, got, 3, "all rows contain 'hello' in request body")

	got, err = reader.Search(ctx, port.LogQuery{Table: table, Done: "yes", Limit: 50})
	require.NoError(t, err)
	require.Len(t, got, 2)

	// Защита от SQL-инъекции: невалидное имя таблицы.
	_, err = reader.GetByID(ctx, "nexus_default.test_e2e; DROP TABLE foo--", "x")
	require.Error(t, err)
}

// TestClickHouse_Logging_Scenarios — сквозной путь SendUsecase → chlog.Writer →
// ClickHouse (§22). Проверяет, что спецсимволы/JSON/большое тело сохраняются
// байт-в-байт, обрезка по символам работает на реальном драйвере, а при
// выключенном логировании запись в ClickHouse не появляется вообще.
func TestClickHouse_Logging_Scenarios(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	conn, cfg, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	const table = "nexus_default.test_logging"
	createNodeLogTable(t, ctx, conn, table)

	logger := logging.NewNoop()
	provider := clickhouse.StaticProvider(conn)
	writer := chlog.New(provider, cfg, logger)
	defer writer.Stop(ctx)
	reader := webch.NewLogReader(provider, logger)

	// Тело со спецсимволами, кавычками, переводами строк, табами, unicode и emoji.
	specialReq := "line1\nline2\ttab \"quote\" \\back контроль 😀 <tag> & ;DROP"
	specialResp := `{"k":"v\"al","nested":{"arr":[1,2,3]},"u":"привет 😀","big":"` +
		strings.Repeat("ы", 200_000) + `"}`

	baseInput := func(id string) senderuc.SendInput {
		return senderuc.SendInput{
			ID:              id,
			NodePath:        "logging/test",
			RootMethod:      domain.RootMethodRequest,
			TargetURL:       "https://example.com/upstream?x=1",
			Method:          "POST",
			Body:            []byte(specialReq),
			TimeoutMs:       1000,
			ClickHouseTable: table,
			LogRequestBody:  true,
			LogResponseBody: true,
			LoggingEnabled:  true,
		}
	}
	uc := senderuc.NewSendUsecase(
		stubHTTP{resp: &senderport.HTTPResponse{StatusCode: 200, Body: []byte(specialResp)}},
		writer, nil, logger,
	)

	idSpecial := "22222222-0000-0000-0000-000000000001"
	idTrunc := "22222222-0000-0000-0000-000000000002"
	idDisabled := "22222222-0000-0000-0000-000000000003"

	// 1) Спецсимволы/JSON/большое тело — без лимита, сохраняем как есть.
	uc.Send(ctx, baseInput(idSpecial))

	// 2) Лимит 100 символов — тела режутся, но запись есть. Тело запроса
	// заведомо длиннее лимита (500 рун кириллицы), ответ — большой JSON.
	truncReq := strings.Repeat("я", 500)
	in := baseInput(idTrunc)
	in.Body = []byte(truncReq)
	in.MaxBodySizeEnabled = true
	in.MaxBodySize = 100
	uc.Send(ctx, in)

	// 3) Логирование выключено — записи быть не должно.
	in = baseInput(idDisabled)
	in.LoggingEnabled = false
	uc.Send(ctx, in)

	require.NoError(t, writer.Flush(ctx))

	// Ждём, пока появятся 2 ожидаемые записи (special + trunc), disabled не считаем.
	deadline := time.Now().Add(20 * time.Second)
	var n uint64
	for time.Now().Before(deadline) {
		row := conn.QueryRow(ctx, fmt.Sprintf("SELECT count() FROM %s", table))
		require.NoError(t, row.Scan(&n))
		if n >= 2 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Кейс 3: выключенное логирование — строки с этим ID нет.
	var disabledCnt uint64
	require.NoError(t, conn.QueryRow(ctx,
		fmt.Sprintf("SELECT count() FROM %s WHERE ID = ?", table), idDisabled).Scan(&disabledCnt))
	require.EqualValues(t, 0, disabledCnt, "при LoggingEnabled=false запись не пишется")
	require.EqualValues(t, 2, n, "ожидаем ровно 2 записи (special + truncated), без disabled")

	// Кейс 1: спецсимволы/JSON/большое тело прочитались байт-в-байт.
	recSpecial, err := reader.GetByID(ctx, table, idSpecial)
	require.NoError(t, err)
	require.Equal(t, specialReq, recSpecial.Request, "request со спецсимволами сохранён без изменений")
	require.Equal(t, specialResp, recSpecial.Response, "большой JSON-ответ сохранён байт-в-байт")

	// Кейс 2: обрезка — ровно 100 рун + маркер, checksum по полному телу.
	recTrunc, err := reader.GetByID(ctx, table, idTrunc)
	require.NoError(t, err)
	require.Equal(t, strings.Repeat("я", 100)+"…(truncated)", recTrunc.Request, "request обрезан до 100 рун + маркер")
	require.Equal(t, string([]rune(specialResp)[:100])+"…(truncated)", recTrunc.Response, "response обрезан до 100 рун + маркер")
	require.Equal(t, md5hexStr([]byte(truncReq)), recTrunc.ChecksumRequest,
		"checksum считается по полному телу, не по обрезанному")
}

// md5hexStr дублирует send.md5hex (не экспортирован) для проверки checksum в тесте.
func md5hexStr(b []byte) string {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

// TestClickHouse_GetByID_Deterministic — при двух записях с одним ID
// (file-fallback restore + повторный INSERT, или ручной replay одной и той же
// записи) GetByID должен возвращать самую свежую по date_request, а не
// произвольную строку — это контракт UI: «открываю по id, вижу актуальное».
func TestClickHouse_GetByID_Deterministic(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	conn, cfg, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	const table = "nexus_default.test_getbyid"
	createNodeLogTable(t, ctx, conn, table)

	logger := logging.NewNoop()
	provider := clickhouse.StaticProvider(conn)
	writer := chlog.New(provider, cfg, logger)
	defer writer.Stop(ctx)

	now := time.Now().UTC().Truncate(time.Second)
	id := "11111111-1111-1111-1111-111111111111"

	mk := func(at time.Time, status int32, response string) *domain.LogRecord {
		return &domain.LogRecord{
			ID:               id,
			Type:             domain.RootMethodRequest,
			URL:              "https://example.com/upstream",
			Method:           "POST",
			Parameters:       "k=v",
			Request:          `{"hello":"world"}`,
			Response:         response,
			Status:           status,
			DateCreate:       at,
			DateRequest:      at,
			DateResponse:     at.Add(50 * time.Millisecond),
			Duration:         50,
			Done:             status < 400,
			ChecksumRequest:  strings.Repeat("a", 32),
			ChecksumResponse: strings.Repeat("b", 32),
			Host:             "h1",
			IP:               "127.0.0.1",
			Attempts:         1,
			AttemptsDetails:  "[]",
		}
	}

	writer.Write(ctx, table, mk(now.Add(-2*time.Hour), 500, `{"old":true}`))
	writer.Write(ctx, table, mk(now, 200, `{"new":true}`))
	require.NoError(t, writer.Flush(ctx))

	deadline := time.Now().Add(20 * time.Second)
	var n uint64
	for time.Now().Before(deadline) {
		row := conn.QueryRow(ctx, fmt.Sprintf("SELECT count() FROM %s WHERE ID = ?", table), id)
		require.NoError(t, row.Scan(&n))
		if n >= 2 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.EqualValues(t, 2, n, "two rows with the same ID expected")

	reader := webch.NewLogReader(provider, logger)
	rec, err := reader.GetByID(ctx, table, id)
	require.NoError(t, err)
	require.Equal(t, `{"new":true}`, rec.Response, "GetByID must return the most recent row by date_request")
	require.EqualValues(t, 200, rec.Status)
}
