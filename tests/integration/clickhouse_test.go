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
	"nexus/internal/domain/logsearch"
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
		node_id String,
		request_size Int64,
		response_size Int64
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

	// §48: адаптер читает распарсенный QExpr, а не сырое Q.
	got, err = reader.Search(ctx, port.LogQuery{Table: table, QExpr: mustSearchExpr(t, "hello", logsearch.Options{}), Limit: 50})
	require.NoError(t, err)
	require.Len(t, got, 3, "all rows contain 'hello' in request body")

	got, err = reader.Search(ctx, port.LogQuery{Table: table, Done: "yes", Limit: 50})
	require.NoError(t, err)
	require.Len(t, got, 2)

	// Защита от SQL-инъекции: невалидное имя таблицы.
	_, err = reader.GetByID(ctx, "nexus_default.test_e2e; DROP TABLE foo--", "x")
	require.Error(t, err)
}

// mustSearchExpr — распарсенное поисковое выражение §48 для передачи в
// port.LogQuery.QExpr (адаптер сырое Q не использует).
func mustSearchExpr(t *testing.T, q string, opts logsearch.Options) *logsearch.Expr {
	t.Helper()
	e, err := logsearch.Parse(q, opts)
	require.NoError(t, err, "parse search query %q", q)
	return e
}

// TestClickHouse_SearchExtended_E2E — семантика расширенного поиска §48 на
// реальном ClickHouse: мини-язык (& | - префиксы), поиск по parameters,
// режимы Aa/ab|/.* и фильтр по method. Зеркальная in-memory семантика
// покрыта unit-тестами logsearch и matchLogFilter.
func TestClickHouse_SearchExtended_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	conn, cfg, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	const table = "nexus_default.test_search48"
	createNodeLogTable(t, ctx, conn, table)

	logger := logging.NewNoop()
	provider := clickhouse.StaticProvider(conn)
	writer := chlog.New(provider, cfg, logger)
	defer writer.Stop(ctx)

	now := time.Now().UTC().Truncate(time.Second)
	mk := func(id, url, method, params, req, resp string) *domain.LogRecord {
		return &domain.LogRecord{
			ID: id, Type: domain.RootMethodRequest,
			URL: url, Method: method, Parameters: params,
			Request: req, Response: resp,
			Status: 200, DateCreate: now, DateRequest: now,
			DateResponse: now, Duration: 1, Done: true,
			ChecksumRequest:  strings.Repeat("a", 32),
			ChecksumResponse: strings.Repeat("b", 32),
			Host:             "h", IP: "127.0.0.1", Attempts: 1, AttemptsDetails: "[]",
		}
	}
	const (
		id1 = "00000000-0000-0000-0000-000000000101" // orders: cat в теле, debug в params
		id2 = "00000000-0000-0000-0000-000000000102" // parcels: concatenate, timeout в ответе
		id3 = "00000000-0000-0000-0000-000000000103" // health: кириллица, PONG
		id4 = "00000000-0000-0000-0000-000000000104" // refunds: ошибка 500, done=0
	)
	writer.Write(ctx, table, mk(id1, "/v1/orders", "v1/orders", "id=42&debug=1", `{"cat":"grumpy"}`, `{"ok":true}`))
	writer.Write(ctx, table, mk(id2, "/v1/parcels", "v1/parcels", "token=abc", `{"concatenate":"x"}`, `{"error":"timeout"}`))
	writer.Write(ctx, table, mk(id3, "/health", "health", "", "Привет мир", "PONG"))
	failed := mk(id4, "/v2/refunds", "v2/refunds", "", `{"note":"failed refund"}`, `{"error":"boom"}`)
	failed.Status, failed.Done = 500, false
	writer.Write(ctx, table, failed)
	require.NoError(t, writer.Flush(ctx))

	deadline := time.Now().Add(20 * time.Second)
	var n uint64
	for time.Now().Before(deadline) {
		row := conn.QueryRow(ctx, fmt.Sprintf("SELECT count() FROM %s", table))
		require.NoError(t, row.Scan(&n))
		if n >= 4 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.EqualValues(t, 4, n)

	reader := webch.NewLogReader(provider, logger)
	ids := func(recs []*domain.LogRecord) []string {
		out := make([]string, 0, len(recs))
		for _, r := range recs {
			out = append(out, r.ID)
		}
		return out
	}
	search := func(q port.LogQuery) []string {
		t.Helper()
		q.Table, q.Limit = table, 50
		got, err := reader.Search(ctx, q)
		require.NoError(t, err)
		return ids(got)
	}

	tests := []struct {
		name string
		q    string
		opts logsearch.Options
		want []string // ожидаемые ID (порядок не важен)
	}{
		{name: "поиск по parameters", q: "debug=1", want: []string{id1}},
		{name: "AND", q: "orders & grumpy", want: []string{id1}},
		{name: "OR", q: "grumpy | timeout", want: []string{id1, id2}},
		{name: "NOT исключает", q: "-timeout", want: []string{id1, id3, id4}},
		{name: "NOT со скоупом поля", q: "-resp:error", want: []string{id1, id3}},
		{name: "три OR-группы", q: "grumpy | timeout | привет", want: []string{id1, id2, id3}},
		{name: "AND из трёх термов", q: "parcels & token & concat", want: []string{id2}},
		{name: "комбинация (И НЕ) ИЛИ поле", q: "error & -boom | url:health", want: []string{id2, id3}},
		{name: "экранированный & — литерал", q: `id=42\&debug`, want: []string{id1}},
		{name: "url: скоуп находит", q: "url:parcels", want: []string{id2}},
		{name: "resp: скоуп не находит текст из url", q: "resp:parcels", want: nil},
		{name: "params: скоуп", q: "params:token", want: []string{id2}},
		{name: "регистронезависимый дефолт", q: "pong", want: []string{id3}},
		{name: "Aa: регистрозависимый мисс", q: "pong", opts: logsearch.Options{CaseSensitive: true}, want: nil},
		{name: "Aa: регистрозависимый хит", q: "PONG", opts: logsearch.Options{CaseSensitive: true}, want: []string{id3}},
		{name: "ab|: целое слово не матчит внутри слова", q: "cat", opts: logsearch.Options{WholeWord: true}, want: []string{id1}},
		{name: "regex: альтернация", q: "time(out|r)", opts: logsearch.Options{Regex: true}, want: []string{id2}},
		{name: "regex: кириллица без учёта регистра", q: "ПРИВЕТ.*МИР", opts: logsearch.Options{Regex: true}, want: []string{id3}},
		{name: "кириллический case-fold в plain", q: "привет", want: []string{id3}},
	}
	for _, tt := range tests {
		got := search(port.LogQuery{QExpr: mustSearchExpr(t, tt.q, tt.opts)})
		require.ElementsMatch(t, tt.want, got, "case %q", tt.name)
	}

	// Фильтр по method (точное совпадение) и комбинация с q.
	require.ElementsMatch(t, []string{id1}, search(port.LogQuery{Method: "v1/orders"}))
	require.Empty(t, search(port.LogQuery{Method: "v1"}), "method — exact match, не префикс")
	require.ElementsMatch(t, []string{id2},
		search(port.LogQuery{Method: "v1/parcels", QExpr: mustSearchExpr(t, "timeout", logsearch.Options{})}))
	require.Empty(t,
		search(port.LogQuery{Method: "health", QExpr: mustSearchExpr(t, "timeout", logsearch.Options{})}))

	// Комбинации q со «старыми» фильтрами Status/Done (§48 не ломает §7.4).
	require.ElementsMatch(t, []string{id2, id4},
		search(port.LogQuery{QExpr: mustSearchExpr(t, "resp:error", logsearch.Options{})}))
	require.ElementsMatch(t, []string{id4},
		search(port.LogQuery{Status: "err", QExpr: mustSearchExpr(t, "resp:error", logsearch.Options{})}))
	require.ElementsMatch(t, []string{id2},
		search(port.LogQuery{Done: "yes", QExpr: mustSearchExpr(t, "resp:error", logsearch.Options{})}))
	require.Empty(t,
		search(port.LogQuery{Method: "v2/refunds", QExpr: mustSearchExpr(t, "-boom", logsearch.Options{})}),
		"негация исключает единственную запись метода")

	// §48.3: фасеты — DistinctMethods (сортировка, только непустые) и DateRange.
	methods, err := reader.DistinctMethods(ctx, table, "", 0)
	require.NoError(t, err)
	require.Equal(t, []string{"health", "v1/orders", "v1/parcels", "v2/refunds"}, methods,
		"отсортированный distinct без пустых")

	lo, hi, err := reader.DateRange(ctx, table, "")
	require.NoError(t, err)
	wantMs := now.UnixMilli()
	require.Equal(t, wantMs, lo, "min = единственная секунда фикстур")
	require.Equal(t, wantMs, hi, "max = единственная секунда фикстур")

	// Пустая таблица → 0/0 (guard от epoch-1970 у min()) и пустой methods.
	const emptyTable = "nexus_default.test_search48_empty"
	createNodeLogTable(t, ctx, conn, emptyTable)
	methods, err = reader.DistinctMethods(ctx, emptyTable, "", 0)
	require.NoError(t, err)
	require.Empty(t, methods)
	lo, hi, err = reader.DateRange(ctx, emptyTable, "")
	require.NoError(t, err)
	require.Zero(t, lo)
	require.Zero(t, hi)
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
		writer, nil, logger, 64<<20,
	)

	idSpecial := "22222222-0000-0000-0000-000000000001"
	idTrunc := "22222222-0000-0000-0000-000000000002"
	idDisabled := "22222222-0000-0000-0000-000000000003"

	// 1) Спецсимволы/JSON/большое тело — без лимита, сохраняем как есть.
	uc.Send(ctx, baseInput(idSpecial))

	// 2) §22.2: лимит 100 рун — лог-копия тела режется, но запись есть, ответ
	// клиенту полный. Тело запроса заведомо длиннее лимита (500 рун кириллицы).
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

	// Кейс 2 (§22.2): лог-копия обрезана до 100 рун + маркер; ответ доставлен
	// (200, done=1); checksum по ПОЛНОМУ телу.
	recTrunc, err := reader.GetByID(ctx, table, idTrunc)
	require.NoError(t, err)
	require.EqualValues(t, 200, recTrunc.Status, "ответ клиенту доставлен — лимит только на лог")
	require.True(t, recTrunc.Done)
	require.Equal(t, strings.Repeat("я", 100)+"…(truncated)", recTrunc.Request, "request в логе обрезан до 100 рун + маркер")
	require.Equal(t, md5hexStr([]byte(truncReq)), recTrunc.ChecksumRequest, "checksum по полному телу")
}

// md5hexStr дублирует send.md5hex (не экспортирован) для проверки checksum в тесте.
func md5hexStr(b []byte) string {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

// TestClickHouse_KeysetPagination_DenseSecond_E2E — keyset-пагинация (§44/45-fix):
// при сотнях строк с ОДИНАКОВЫМ date_request (секундная точность) простой курсор
// `date_request <= to` зацикливался на одной секунде (следующая страница — те же
// строки → дедуп → стопор скролла). Составной курсор (date_request, ID) должен
// перешагивать плотную секунду: все строки отдаются ровно по разу, без повторов.
func TestClickHouse_KeysetPagination_DenseSecond_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	conn, cfg, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	const table = "nexus_default.test_keyset"
	createNodeLogTable(t, ctx, conn, table)

	logger := logging.NewNoop()
	provider := clickhouse.StaticProvider(conn)
	writer := chlog.New(provider, cfg, logger)
	defer writer.Stop(ctx)

	// Все строки В ОДНОЙ СЕКУНДЕ — это и есть «плотная секунда».
	sec := time.Now().UTC().Truncate(time.Second)
	const total = 5
	for i := 1; i <= total; i++ {
		writer.Write(ctx, table, &domain.LogRecord{
			ID:               fmt.Sprintf("00000000-0000-0000-0000-00000000000%d", i),
			Type:             domain.RootMethodRequest,
			URL:              "https://example.com/u",
			Method:           "POST",
			Status:           200,
			DateCreate:       sec,
			DateRequest:      sec,
			DateResponse:     sec,
			Done:             true,
			ChecksumRequest:  strings.Repeat("a", 32),
			ChecksumResponse: strings.Repeat("b", 32),
			Host:             "h",
			IP:               "127.0.0.1",
			Attempts:         1,
			AttemptsDetails:  "[]",
		})
	}
	require.NoError(t, writer.Flush(ctx))

	deadline := time.Now().Add(20 * time.Second)
	var n uint64
	for time.Now().Before(deadline) {
		require.NoError(t, conn.QueryRow(ctx, fmt.Sprintf("SELECT count() FROM %s", table)).Scan(&n))
		if n >= total {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.EqualValues(t, total, n, "expected %d rows in %s", total, table)

	reader := webch.NewLogReader(provider, logger)

	// Пагинация keyset'ом с limit < total — все строки одной секунды.
	const limit = 2
	seen := map[string]bool{}
	var cursorMs int64
	var cursorID string
	for page := 0; page <= total+2; page++ {
		require.LessOrEqual(t, page, total+1, "keyset не должен зацикливаться на плотной секунде")
		q := port.LogQuery{Table: table, Limit: limit}
		if cursorID != "" {
			q.UntilMs = cursorMs
			q.BeforeID = cursorID
		}
		recs, err := reader.Search(ctx, q)
		require.NoError(t, err)
		if len(recs) == 0 {
			break
		}
		for _, r := range recs {
			require.False(t, seen[r.ID], "дубликат %s — keyset не перешагнул плотную секунду", r.ID)
			seen[r.ID] = true
		}
		oldest := recs[len(recs)-1]
		cursorMs = oldest.DateRequest.UnixMilli()
		cursorID = oldest.ID
		if len(recs) < limit {
			break
		}
	}
	require.Len(t, seen, total, "keyset-пагинация отдала все строки одной секунды ровно по разу")
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
