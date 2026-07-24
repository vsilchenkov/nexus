package usecase

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/sender/usecase/port"
)

// stubHTTPCaller — программируемый ответ. Если Body/Status — функция,
// дёрнем её на каждой попытке (для retry-сценариев).
type stubHTTPCaller struct {
	mu          sync.Mutex
	calls       int
	responses   []*port.HTTPResponse // последовательность по попыткам
	errs        []error
	lastReqBody []byte // §68: тело последнего запроса — проверяем проброску байтов
}

func (s *stubHTTPCaller) Do(_ context.Context, req *port.HTTPRequest) (*port.HTTPResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if req != nil {
		s.lastReqBody = req.Body
	}
	i := s.calls
	s.calls++
	var (
		resp *port.HTTPResponse
		err  error
	)
	if i < len(s.responses) {
		resp = s.responses[i]
	}
	if i < len(s.errs) {
		err = s.errs[i]
	}
	return resp, err
}

// stubLogWriter — собирает записи Write по таблицам, чтобы можно было
// проверить статус/duration/attempts_details.
type stubLogWriter struct {
	mu      sync.Mutex
	written []writtenLog
	flushed int
}

type writtenLog struct {
	table string
	rec   *domain.LogRecord
}

func (s *stubLogWriter) Write(_ context.Context, table string, rec *domain.LogRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.written = append(s.written, writtenLog{table: table, rec: rec})
}
func (s *stubLogWriter) Flush(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushed++
	return nil
}

// stubBreaker — программируемый CircuitBreaker.
type stubBreaker struct {
	allow        bool
	allowCount   int // §55: dry-run не должен спрашивать breaker вообще
	successCount int
	failureCount int
	allowErr     error
}

func (b *stubBreaker) Allow(_ context.Context, _ string) (bool, error) {
	b.allowCount++
	return b.allow, b.allowErr
}
func (b *stubBreaker) RecordSuccess(_ context.Context, _ string) error {
	b.successCount++
	return nil
}
func (b *stubBreaker) RecordFailure(_ context.Context, _ string) error {
	b.failureCount++
	return nil
}

func baseInput() SendInput {
	return SendInput{
		ID:              "send-1",
		NodePath:        "partner/echo",
		RootMethod:      domain.RootMethodRequest,
		TargetURL:       "https://api.example.com/hook?a=1",
		Method:          "POST",
		Body:            []byte(`{"k":"v"}`),
		TimeoutMs:       1000,
		RetryCount:      0,
		RetryBackoffMs:  10,
		ClickHouseTable: "nexus.log_partner_echo",
		LoggingEnabled:  true, // дефолт §22: узел логирует
	}
}

func TestMd5hex_DeterministicAndHex(t *testing.T) {
	t.Parallel()

	h1 := md5hex([]byte("hello"))
	h2 := md5hex([]byte("hello"))
	assert.Equal(t, h1, h2)
	// MD5 = 32 hex.
	assert.Len(t, h1, 32)
	for _, c := range h1 {
		ok := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
		assert.True(t, ok, "lowercase hex, got %q", c)
	}
	assert.NotEqual(t, h1, md5hex([]byte("HELLO")))
	// Пустой ввод — детерминированный MD5("").
	assert.Equal(t, "d41d8cd98f00b204e9800998ecf8427e", md5hex(nil))
}

func TestExtractQuery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, in, want string
	}{
		{"with query", "https://x/y?a=1&b=2", "a=1&b=2"},
		{"no query", "https://x/y", ""},
		{"empty query marker", "https://x/y?", ""},
		{"only path", "/just/path?p=1", "p=1"},
		{"broken url", "://bad", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, extractQuery(tc.in))
		})
	}
}

// TestAppendRedirectNote (§50): сводка редиректов дописывается к reason, при
// смене метода — предупреждение о потере тела; пустой список не трогает reason.
func TestAppendRedirectNote(t *testing.T) {
	t.Parallel()

	up := port.RedirectHop{
		Status: 301, From: "http://x/a", To: "https://x/a",
		FromMethod: "GET", ToMethod: "GET", SchemeChange: "upgrade",
	}
	dropped := port.RedirectHop{
		Status: 301, From: "http://x/a", To: "https://x/a",
		FromMethod: "POST", ToMethod: "GET", SchemeChange: "upgrade",
	}

	assert.Equal(t, "OK", appendRedirectNote("OK", nil), "нет редиректов — reason не тронут")

	got := appendRedirectNote("OK", []port.RedirectHop{up})
	assert.Contains(t, got, "OK · ")
	assert.Contains(t, got, "http://x/a → https://x/a")
	assert.NotContains(t, got, "тело запроса потеряно")

	got = appendRedirectNote("OK", []port.RedirectHop{dropped})
	assert.Contains(t, got, "тело запроса потеряно (POST→GET)")

	got = appendRedirectNote("", []port.RedirectHop{up})
	assert.NotContains(t, got, " · ", "пустой reason — без разделителя")
	assert.Contains(t, got, "редирект")
}

func TestSend_Success_RecordsLogAndUpdatesBreaker(t *testing.T) {
	t.Parallel()

	httpc := &stubHTTPCaller{
		responses: []*port.HTTPResponse{
			{StatusCode: 200, Body: []byte(`{"ok":true}`), Headers: map[string]string{"X-Echo": "1"}},
		},
	}
	logw := &stubLogWriter{}
	cb := &stubBreaker{allow: true}

	uc := NewSendUsecase(httpc, logw, cb, logging.NewNoop(), 64<<20)
	in := baseInput()
	in.LogResponseBody = true

	out := uc.Send(context.Background(), in)

	assert.Equal(t, int32(200), out.StatusCode)
	assert.Equal(t, "1", out.Headers["X-Echo"])
	assert.Equal(t, []byte(`{"ok":true}`), out.Body)
	assert.Equal(t, int32(1), out.Attempts)
	assert.Empty(t, out.Error)

	// Лог записан в правильную таблицу с done=true.
	require.Len(t, logw.written, 1)
	rec := logw.written[0].rec
	assert.Equal(t, "nexus.log_partner_echo", logw.written[0].table)
	assert.True(t, rec.Done)
	assert.Equal(t, int32(200), rec.Status)
	assert.Equal(t, "OK", rec.Reason)
	assert.Equal(t, `{"ok":true}`, rec.Response, "LogResponseBody=true → тело сохранено")
	// Одна попытка — attempts_details не нужен (см. send.go: только при >1 или fail).
	assert.Empty(t, rec.AttemptsDetails)

	// Breaker: успешный вызов → RecordSuccess, не RecordFailure.
	assert.Equal(t, 1, cb.successCount)
	assert.Equal(t, 0, cb.failureCount)
}

// stubHostResolver — фейковый HostResolver (§67): отдаёт фиксированное имя,
// считает вызовы.
type stubHostResolver struct {
	host  string
	calls int
	ips   []string
}

func (s *stubHostResolver) Lookup(ip string) string {
	s.calls++
	s.ips = append(s.ips, ip)
	return s.host
}

func TestSend_ClientHost_FilledFromResolver(t *testing.T) {
	t.Parallel()

	httpc := &stubHTTPCaller{responses: []*port.HTTPResponse{{StatusCode: 200}}}
	logw := &stubLogWriter{}
	hr := &stubHostResolver{host: "srv-1c.vz78.vozovoz.ru"}

	uc := NewSendUsecase(httpc, logw, nil, logging.NewNoop(), 64<<20, WithHostResolver(hr))
	in := baseInput()
	in.ClientIP = "192.168.86.246"
	out := uc.Send(context.Background(), in)

	assert.Equal(t, int32(200), out.StatusCode)
	require.Len(t, logw.written, 1)
	assert.Equal(t, "srv-1c.vz78.vozovoz.ru", logw.written[0].rec.ClientHost,
		"§67: client_host заполняется из резолвера")
	require.Len(t, hr.ips, 1)
	assert.Equal(t, "192.168.86.246", hr.ips[0], "резолвится именно ClientIP")
}

func TestSend_ClientHost_NoopWithoutResolver(t *testing.T) {
	t.Parallel()

	httpc := &stubHTTPCaller{responses: []*port.HTTPResponse{{StatusCode: 200}}}
	logw := &stubLogWriter{}

	// Без WithHostResolver (и с nil) — noop: пустой client_host, без паник.
	uc := NewSendUsecase(httpc, logw, nil, logging.NewNoop(), 64<<20, WithHostResolver(nil))
	uc.Send(context.Background(), baseInput())

	require.Len(t, logw.written, 1)
	assert.Empty(t, logw.written[0].rec.ClientHost)
}

func TestSend_CircuitBreakerOpen_Returns503WithoutHTTPCall(t *testing.T) {
	t.Parallel()

	httpc := &stubHTTPCaller{} // не должен быть дёрнут
	logw := &stubLogWriter{}
	cb := &stubBreaker{allow: false}

	uc := NewSendUsecase(httpc, logw, cb, logging.NewNoop(), 64<<20)
	out := uc.Send(context.Background(), baseInput())

	assert.Equal(t, int32(503), out.StatusCode)
	assert.Contains(t, out.Error, "circuit breaker open")
	assert.Equal(t, 0, httpc.calls, "при open-breaker HTTP-вызов не должен идти")

	// Запись лога есть — с reason=circuit_breaker_open и status=0.
	require.Len(t, logw.written, 1)
	rec := logw.written[0].rec
	assert.Equal(t, int32(0), rec.Status)
	assert.False(t, rec.Done)
	assert.Equal(t, "circuit_breaker_open", rec.Reason)
	assert.Equal(t, int32(0), rec.Attempts)
}

func TestSend_4xx_NoRetry_BreakerStaysHealthy(t *testing.T) {
	t.Parallel()

	httpc := &stubHTTPCaller{
		responses: []*port.HTTPResponse{
			{StatusCode: 404, Body: []byte(`not found`)},
		},
	}
	logw := &stubLogWriter{}
	cb := &stubBreaker{allow: true}

	uc := NewSendUsecase(httpc, logw, cb, logging.NewNoop(), 64<<20)
	in := baseInput()
	in.RetryCount = 3 // не должно сработать на 4xx

	out := uc.Send(context.Background(), in)

	assert.Equal(t, int32(404), out.StatusCode)
	assert.Equal(t, int32(1), out.Attempts, "4xx не ретраится — одна попытка")
	assert.Equal(t, 1, httpc.calls)

	require.Len(t, logw.written, 1)
	rec := logw.written[0].rec
	assert.False(t, rec.Done)
	assert.Equal(t, int32(404), rec.Status)
	assert.Equal(t, "HTTP 404", rec.Reason)

	// Breaker (§50.4): 4xx — ошибка данных/клиента, узел жив и ответил →
	// RecordSuccess, НЕ failure. Иначе серия 4xx (напр. 422 NotRegistered
	// протухших FCM-токенов) открывала бы breaker и блокировала валидные
	// запросы (боевой инцидент site/push). В лог при этом пишется done=false.
	assert.Equal(t, 1, cb.successCount)
	assert.Equal(t, 0, cb.failureCount)
}

// TestSend_BreakerHealth_5xxFails_4xxDoesNot (§50.4): breaker открывают только
// транспортные ошибки и 5xx; серия 4xx счётчик отказов не растит.
func TestSend_BreakerHealth_5xxFails_4xxDoesNot(t *testing.T) {
	t.Parallel()

	mk := func(status int32) (*stubBreaker, SendOutput) {
		httpc := &stubHTTPCaller{responses: []*port.HTTPResponse{{StatusCode: status}}}
		cb := &stubBreaker{allow: true}
		uc := NewSendUsecase(httpc, &stubLogWriter{}, cb, logging.NewNoop(), 64<<20)
		return cb, uc.Send(context.Background(), baseInput())
	}

	tests := []struct {
		name        string
		status      int32
		wantSuccess int
		wantFailure int
	}{
		{"422 (данные клиента) → healthy", 422, 1, 0},
		{"429 (rate limit клиента) → healthy", 429, 1, 0},
		{"500 → failure", 500, 0, 1},
		{"503 → failure", 503, 0, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cb, out := mk(tt.status)
			assert.Equal(t, tt.status, out.StatusCode)
			assert.Equal(t, tt.wantSuccess, cb.successCount)
			assert.Equal(t, tt.wantFailure, cb.failureCount)
		})
	}
}

func TestSend_5xxRetriesUntilSuccess(t *testing.T) {
	t.Parallel()

	httpc := &stubHTTPCaller{
		responses: []*port.HTTPResponse{
			{StatusCode: 502, Body: []byte("bad gateway")},
			{StatusCode: 503, Body: []byte("unavailable")},
			{StatusCode: 200, Body: []byte("ok")},
		},
	}
	logw := &stubLogWriter{}
	cb := &stubBreaker{allow: true}

	uc := NewSendUsecase(httpc, logw, cb, logging.NewNoop(), 64<<20)
	in := baseInput()
	in.RetryCount = 3     // итого 4 попытки максимум
	in.RetryBackoffMs = 1 // быстрый backoff для теста

	t0 := time.Now()
	out := uc.Send(context.Background(), in)
	assert.Less(t, time.Since(t0), 2*time.Second, "тест не должен зависать дольше пары секунд")

	assert.Equal(t, int32(200), out.StatusCode)
	assert.Equal(t, int32(3), out.Attempts)
	assert.Equal(t, 3, httpc.calls)

	require.Len(t, logw.written, 1)
	rec := logw.written[0].rec
	assert.True(t, rec.Done)
	// attempts_details JSON присутствует, потому что попыток было > 1.
	assert.NotEmpty(t, rec.AttemptsDetails, "ожидаем JSON попыток после ретраев")
	assert.True(t, strings.Contains(rec.AttemptsDetails, "HTTP 502") ||
		strings.Contains(rec.AttemptsDetails, "502"),
		"в attempts_details должны быть статусы прежних попыток, got: %s", rec.AttemptsDetails)
	assert.Equal(t, 1, cb.successCount)
}

func TestSend_AllAttemptsFail_Returns0AndError(t *testing.T) {
	t.Parallel()

	httpc := &stubHTTPCaller{
		errs: []error{
			errors.New("conn refused"),
			errors.New("conn refused"),
		},
	}
	logw := &stubLogWriter{}
	cb := &stubBreaker{allow: true}

	uc := NewSendUsecase(httpc, logw, cb, logging.NewNoop(), 64<<20)
	in := baseInput()
	in.RetryCount = 1 // 2 попытки
	in.RetryBackoffMs = 1

	out := uc.Send(context.Background(), in)
	assert.Equal(t, int32(0), out.StatusCode)
	assert.Equal(t, "conn refused", out.Error)
	assert.Equal(t, int32(2), out.Attempts)
	assert.Equal(t, 2, httpc.calls)

	require.Len(t, logw.written, 1)
	rec := logw.written[0].rec
	assert.False(t, rec.Done)
	assert.Equal(t, int32(0), rec.Status)
	assert.Equal(t, "conn refused", rec.Reason)
	assert.Equal(t, 1, cb.failureCount)
}

func TestSend_LogRequestBody_StoresBodyInRecord(t *testing.T) {
	t.Parallel()

	httpc := &stubHTTPCaller{
		responses: []*port.HTTPResponse{{StatusCode: 200, Body: []byte("ok")}},
	}
	logw := &stubLogWriter{}
	cb := &stubBreaker{allow: true}

	uc := NewSendUsecase(httpc, logw, cb, logging.NewNoop(), 64<<20)
	in := baseInput()
	in.LogRequestBody = true
	in.LogResponseBody = false

	uc.Send(context.Background(), in)

	require.Len(t, logw.written, 1)
	rec := logw.written[0].rec
	assert.Equal(t, `{"k":"v"}`, rec.Request,
		"LogRequestBody=true → исходное тело должно осесть в записи лога")
	assert.Empty(t, rec.Response, "LogResponseBody=false → ответ не сохраняется")

	// Параметры query из TargetURL сохранены.
	assert.Equal(t, "a=1", rec.Parameters)
}

func TestSend_NoopBreakerWhenNil(t *testing.T) {
	t.Parallel()

	httpc := &stubHTTPCaller{
		responses: []*port.HTTPResponse{{StatusCode: 200, Body: []byte("ok")}},
	}
	logw := &stubLogWriter{}

	uc := NewSendUsecase(httpc, logw, nil /* CB */, logging.NewNoop(), 64<<20)
	out := uc.Send(context.Background(), baseInput())
	// noopBreaker.Allow=true, ничего не падает — это и есть проверка.
	assert.Equal(t, int32(200), out.StatusCode)
}

// --- §22: контроль логирования ---------------------------------------------

// §22.2: max_body_size режет ТОЛЬКО лог-копию тела (по рунам), результат — валидный
// UTF-8 + маркер. На ответ клиенту/коды не влияет (см. тесты Send ниже).
func TestTruncateRunes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		enabled bool
		max     int32
		want    string
	}{
		{"disabled passes through", "hello world", false, 5, "hello world"},
		{"max<=0 passes through", "hello", true, 0, "hello"},
		{"shorter than limit", "hi", true, 5, "hi"},
		{"exactly at limit", "hello", true, 5, "hello"},
		{"longer than limit", "hello world", true, 5, "hello" + truncationMarker},
		{"empty string", "", true, 5, ""},
		// Режем по РУНАМ, не по байтам: 6 кириллических рун = 12 байт.
		{"cyrillic by runes", "привет мир", true, 6, "привет" + truncationMarker},
		{"emoji by runes", "😀😀😀😀😀", true, 2, "😀😀" + truncationMarker},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := truncateRunes(tc.in, tc.enabled, tc.max)
			assert.Equal(t, tc.want, got)
			assert.True(t, utf8.ValidString(got), "результат — валидный UTF-8")
		})
	}
}

func TestSend_LoggingDisabled_NoWrite(t *testing.T) {
	t.Parallel()

	t.Run("success path", func(t *testing.T) {
		t.Parallel()
		httpc := &stubHTTPCaller{
			responses: []*port.HTTPResponse{{StatusCode: 200, Body: []byte("ok")}},
		}
		logw := &stubLogWriter{}
		uc := NewSendUsecase(httpc, logw, &stubBreaker{allow: true}, logging.NewNoop(), 64<<20)
		in := baseInput()
		in.LoggingEnabled = false
		in.LogRequestBody = true
		in.LogResponseBody = true

		out := uc.Send(context.Background(), in)

		assert.Equal(t, int32(200), out.StatusCode, "ответ клиенту отдаётся как обычно")
		assert.Empty(t, logw.written, "при LoggingEnabled=false лог в ClickHouse не пишется")
	})

	t.Run("circuit breaker open path", func(t *testing.T) {
		t.Parallel()
		logw := &stubLogWriter{}
		uc := NewSendUsecase(&stubHTTPCaller{}, logw, &stubBreaker{allow: false}, logging.NewNoop(), 64<<20)
		in := baseInput()
		in.LoggingEnabled = false

		out := uc.Send(context.Background(), in)

		assert.Equal(t, int32(503), out.StatusCode)
		assert.Empty(t, logw.written, "даже CB-open путь не пишет лог при выключенном логировании")
	})
}

// §22.2: max_body_size режет ТОЛЬКО лог-копию (request+response) по рунам; КЛИЕНТ
// получает ПОЛНОЕ тело ответа (200), checksum по полному телу.
func TestSend_MaxBodySize_TruncatesLogOnly_ClientFull(t *testing.T) {
	t.Parallel()

	reqBody := []byte(strings.Repeat("я", 100))  // 100 рун > лимит 10
	respBody := []byte(strings.Repeat("😀", 100)) // 100 рун > лимит 10
	httpc := &stubHTTPCaller{
		responses: []*port.HTTPResponse{{StatusCode: 200, Body: respBody}},
	}
	logw := &stubLogWriter{}
	uc := NewSendUsecase(httpc, logw, &stubBreaker{allow: true}, logging.NewNoop(), 64<<20)

	in := baseInput()
	in.Body = reqBody
	in.LogRequestBody = true
	in.LogResponseBody = true
	in.MaxBodySizeEnabled = true
	in.MaxBodySize = 10

	out := uc.Send(context.Background(), in)

	// Клиент получает ПОЛНЫЙ ответ — лимит на ответ не влияет.
	assert.Equal(t, int32(200), out.StatusCode)
	assert.Equal(t, respBody, out.Body, "клиент получает полное тело ответа")
	assert.Equal(t, 1, httpc.calls, "upstream вызывается как обычно")

	require.Len(t, logw.written, 1)
	rec := logw.written[0].rec
	assert.True(t, rec.Done)
	// В логе — усечённые копии + маркер.
	assert.Equal(t, strings.Repeat("я", 10)+truncationMarker, rec.Request)
	assert.Equal(t, strings.Repeat("😀", 10)+truncationMarker, rec.Response)
	assert.True(t, utf8.ValidString(rec.Request))
	assert.True(t, utf8.ValidString(rec.Response))
	// checksum — по ПОЛНОМУ телу.
	assert.Equal(t, md5hex(reqBody), rec.ChecksumRequest)
	assert.Equal(t, md5hex(respBody), rec.ChecksumResponse)
}

// §43-rev: тело ответа превышает ТРАНСПОРТНЫЙ лимит (config) → httpclient ставит
// TooLarge, Send отдаёт клиенту 502, лог done=0 + reason, тело/checksum не пишутся.
func TestSend_ResponseTooLarge_Rejects502(t *testing.T) {
	t.Parallel()

	httpc := &stubHTTPCaller{
		responses: []*port.HTTPResponse{{StatusCode: 200, TooLarge: true}},
	}
	logw := &stubLogWriter{}
	uc := NewSendUsecase(httpc, logw, &stubBreaker{allow: true}, logging.NewNoop(), 50_000)

	in := baseInput()
	in.LogResponseBody = true

	out := uc.Send(context.Background(), in)

	assert.Equal(t, int32(502), out.StatusCode, "ответ больше транспортного лимита → 502")
	assert.Empty(t, out.Body, "огромный ответ клиенту НЕ отдаётся")
	assert.Contains(t, out.Error, "exceeds transport limit")
	require.Len(t, logw.written, 1)
	rec := logw.written[0].rec
	assert.False(t, rec.Done)
	assert.EqualValues(t, 502, rec.Status)
	assert.Contains(t, rec.Reason, "exceeds transport limit")
	assert.Empty(t, rec.Response, "тело ответа не дочитано — в лог не пишется")
	assert.Empty(t, rec.ChecksumResponse, "checksum нет (тело не прочитано)")
}

// §42-доп: истинные размеры тел — в БАЙТАХ по полному телу (до усечения
// лог-копии и независимо от LogRequestBody/LogResponseBody); при транспортной
// ошибке и TooLarge размер ответа остаётся 0.
func TestSend_BodySizes(t *testing.T) {
	t.Parallel()

	t.Run("полные байты при усечении и выключенном логировании тел", func(t *testing.T) {
		t.Parallel()
		reqBody := []byte(strings.Repeat("я", 100))  // 100 рун = 200 байт
		respBody := []byte(strings.Repeat("😀", 100)) // 100 рун = 400 байт
		httpc := &stubHTTPCaller{
			responses: []*port.HTTPResponse{{StatusCode: 200, Body: respBody}},
		}
		logw := &stubLogWriter{}
		uc := NewSendUsecase(httpc, logw, &stubBreaker{allow: true}, logging.NewNoop(), 64<<20)

		in := baseInput()
		in.Body = reqBody
		in.LogRequestBody = true
		in.LogResponseBody = false // тело ответа в лог не пишем — размер всё равно есть
		in.MaxBodySizeEnabled = true
		in.MaxBodySize = 10

		uc.Send(context.Background(), in)

		require.Len(t, logw.written, 1)
		rec := logw.written[0].rec
		assert.EqualValues(t, len(reqBody), rec.RequestSize, "байты полного запроса, не руны и не усечённая копия")
		assert.EqualValues(t, len(respBody), rec.ResponseSize, "байты полного ответа при LogResponseBody=false")
		assert.Equal(t, strings.Repeat("я", 10)+truncationMarker, rec.Request, "копия усечена, размер — нет")
		assert.Empty(t, rec.Response)
	})

	t.Run("транспортная ошибка — ResponseSize 0", func(t *testing.T) {
		t.Parallel()
		httpc := &stubHTTPCaller{errs: []error{errors.New("dial tcp: refused")}}
		logw := &stubLogWriter{}
		uc := NewSendUsecase(httpc, logw, &stubBreaker{allow: true}, logging.NewNoop(), 64<<20)

		in := baseInput()
		uc.Send(context.Background(), in)

		require.Len(t, logw.written, 1)
		rec := logw.written[0].rec
		assert.EqualValues(t, len(in.Body), rec.RequestSize, "размер запроса известен и при ошибке")
		assert.Zero(t, rec.ResponseSize)
	})

	t.Run("TooLarge — ResponseSize 0 (тело не дочитано)", func(t *testing.T) {
		t.Parallel()
		httpc := &stubHTTPCaller{
			responses: []*port.HTTPResponse{{StatusCode: 200, TooLarge: true}},
		}
		logw := &stubLogWriter{}
		uc := NewSendUsecase(httpc, logw, &stubBreaker{allow: true}, logging.NewNoop(), 50_000)

		in := baseInput()
		uc.Send(context.Background(), in)

		require.Len(t, logw.written, 1)
		rec := logw.written[0].rec
		assert.EqualValues(t, len(in.Body), rec.RequestSize)
		assert.Zero(t, rec.ResponseSize, "истинный размер неизвестен — остаётся 0")
	})
}

// §22.2: под лимитом — обычное поведение (200, полное тело клиенту и в лог).
func TestSend_MaxBodySize_UnderLimit_PassesThrough(t *testing.T) {
	t.Parallel()

	respBody := []byte("short ok")
	httpc := &stubHTTPCaller{
		responses: []*port.HTTPResponse{{StatusCode: 200, Body: respBody}},
	}
	logw := &stubLogWriter{}
	uc := NewSendUsecase(httpc, logw, &stubBreaker{allow: true}, logging.NewNoop(), 64<<20)

	in := baseInput()
	in.Body = []byte("req")
	in.LogRequestBody = true
	in.LogResponseBody = true
	in.MaxBodySizeEnabled = true
	in.MaxBodySize = 1000

	out := uc.Send(context.Background(), in)

	assert.Equal(t, int32(200), out.StatusCode)
	assert.Equal(t, respBody, out.Body, "под лимитом ответ отдаётся полностью")
	require.Len(t, logw.written, 1)
	rec := logw.written[0].rec
	assert.True(t, rec.Done)
	assert.Equal(t, "req", rec.Request)
	assert.Equal(t, "short ok", rec.Response)
}

func TestSend_SpecialCharsAndJSON_StoredIntactWithoutLimit(t *testing.T) {
	t.Parallel()

	// Спецсимволы, кавычки, переводы строк, табы, NUL, unicode, emoji.
	reqBody := []byte("line1\nline2\ttab\x00nul\"quote\\back контроль 😀")
	jsonResp := []byte(`{"key":"v\"al","nested":{"arr":[1,2,3]},"u":"п\nр"}`)

	httpc := &stubHTTPCaller{
		responses: []*port.HTTPResponse{{StatusCode: 200, Body: jsonResp}},
	}
	logw := &stubLogWriter{}
	uc := NewSendUsecase(httpc, logw, &stubBreaker{allow: true}, logging.NewNoop(), 64<<20)

	in := baseInput()
	in.Body = reqBody
	in.LogRequestBody = true
	in.LogResponseBody = true
	// Лимит выключен — тела сохраняются как есть.

	uc.Send(context.Background(), in)

	require.Len(t, logw.written, 1)
	rec := logw.written[0].rec
	assert.Equal(t, string(reqBody), rec.Request, "спецсимволы сохраняются без изменений")
	assert.Equal(t, string(jsonResp), rec.Response, "JSON сохраняется как есть")
}

// §55: dry-run выполняет НАСТОЯЩИЙ HTTP-вызов тем же клиентом, но не оставляет
// следов на узле. Каждый гейт проверяется отдельно: забытый = боевой инцидент
// (лог в production-таблице / узел покрашен в Down / открытый breaker → 503
// реальному трафику).
func TestSend_DryRun_NoSideEffects(t *testing.T) {
	t.Parallel()

	httpc := &stubHTTPCaller{responses: []*port.HTTPResponse{{StatusCode: 200, Body: []byte(`{"ok":true}`)}}}
	logw := &stubLogWriter{}
	cb := &stubBreaker{allow: true}
	uc := NewSendUsecase(httpc, logw, cb, logging.NewNoop(), 64<<20)

	in := baseInput()
	in.DryRun = true
	in.LoggingEnabled = true // логирование узла ВКЛЮЧЕНО — dry-run всё равно не пишет

	out := uc.Send(context.Background(), in)

	assert.Equal(t, int32(200), out.StatusCode, "вызов настоящий — ответ реальный")
	assert.Equal(t, 1, httpc.calls, "HTTP-запрос обязан быть выполнен: тем же клиентом, тем же путём")
	assert.Empty(t, logw.written, "dry-run не пишет в production-таблицу даже при logging_enabled=true")
	assert.Zero(t, cb.allowCount, "dry-run не спрашивает breaker")
	assert.Zero(t, cb.successCount, "dry-run не засчитывает здоровье узла")
	assert.Zero(t, cb.failureCount)
}

// Главный риск §55: серия тестов по мёртвому адресу НЕ должна открыть breaker
// живого узла — иначе боевой трафик начнёт получать 503 из-за чужой отладки.
func TestSend_DryRun_FailuresDoNotOpenBreaker(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		resp *port.HTTPResponse
		err  error
	}{
		{name: "таймаут/транспорт", err: errors.New("context deadline exceeded")},
		{name: "5xx от узла", resp: &port.HTTPResponse{StatusCode: 502}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			httpc := &stubHTTPCaller{}
			if tt.resp != nil {
				httpc.responses = []*port.HTTPResponse{tt.resp}
			}
			if tt.err != nil {
				httpc.errs = []error{tt.err}
			}
			logw := &stubLogWriter{}
			cb := &stubBreaker{allow: true}
			uc := NewSendUsecase(httpc, logw, cb, logging.NewNoop(), 64<<20)

			in := baseInput()
			in.DryRun = true

			uc.Send(context.Background(), in)

			assert.Zero(t, cb.failureCount, "неудачный dry-run не портит здоровье узла")
			assert.Zero(t, cb.allowCount)
			assert.Empty(t, logw.written)
		})
	}
}

// Обратная сторона: dry-run не должен упираться в уже открытый breaker — иначе
// оператор увидит «circuit breaker open» вместо реальной причины (напр. таймаута).
func TestSend_DryRun_NotBlockedByOpenBreaker(t *testing.T) {
	t.Parallel()

	httpc := &stubHTTPCaller{responses: []*port.HTTPResponse{{StatusCode: 200}}}
	logw := &stubLogWriter{}
	cb := &stubBreaker{allow: false} // breaker узла открыт боевым трафиком
	uc := NewSendUsecase(httpc, logw, cb, logging.NewNoop(), 64<<20)

	in := baseInput()
	in.DryRun = true

	out := uc.Send(context.Background(), in)

	assert.Equal(t, int32(200), out.StatusCode)
	assert.Equal(t, 1, httpc.calls, "dry-run идёт к узлу, несмотря на открытый breaker")
	assert.NotEqual(t, int32(503), out.StatusCode)
}

// РЕГРЕССИЯ: боевой путь (DryRun=false) обязан работать как раньше — гейты §55
// не должны его задеть.
func TestSend_NormalCall_KeepsSideEffects(t *testing.T) {
	t.Parallel()

	t.Run("успех: пишет лог и засчитывает здоровье", func(t *testing.T) {
		t.Parallel()
		httpc := &stubHTTPCaller{responses: []*port.HTTPResponse{{StatusCode: 200}}}
		logw := &stubLogWriter{}
		cb := &stubBreaker{allow: true}
		uc := NewSendUsecase(httpc, logw, cb, logging.NewNoop(), 64<<20)

		uc.Send(context.Background(), baseInput()) // DryRun=false

		require.Len(t, logw.written, 1, "боевой вызов пишет в ClickHouse")
		assert.Equal(t, 1, cb.allowCount, "боевой вызов спрашивает breaker")
		assert.Equal(t, 1, cb.successCount)
	})

	t.Run("ошибка: открывает breaker", func(t *testing.T) {
		t.Parallel()
		httpc := &stubHTTPCaller{errs: []error{errors.New("dial tcp: i/o timeout")}}
		logw := &stubLogWriter{}
		cb := &stubBreaker{allow: true}
		uc := NewSendUsecase(httpc, logw, cb, logging.NewNoop(), 64<<20)

		uc.Send(context.Background(), baseInput())

		assert.Equal(t, 1, cb.failureCount, "боевой сбой по-прежнему влияет на breaker")
		require.Len(t, logw.written, 1)
	})
}

// buildMultipartBody собирает валидное multipart/form-data тело (файл + поле).
func buildMultipartBody(t *testing.T) (contentType string, body []byte) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", "invoice.pdf")
	require.NoError(t, err)
	_, err = fw.Write([]byte("BINARY-FILE-CONTENT-SECRET"))
	require.NoError(t, err)
	require.NoError(t, w.WriteField("comment", "please process"))
	require.NoError(t, w.Close())
	return w.FormDataContentType(), buf.Bytes()
}

// §68: multipart-запрос. Тело/вложение НЕ пишется в ClickHouse — вместо него
// плейсхолдер со сводкой частей; при этом checksum и размер считаются по полному
// телу, а сам байтовый поток уходит к внешнему узлу нетронутым.
func TestSend_MultipartRequest_PlaceholderInLogFullBodyProxied(t *testing.T) {
	t.Parallel()

	ct, mpBody := buildMultipartBody(t)
	respBody := []byte("ok")
	httpc := &stubHTTPCaller{responses: []*port.HTTPResponse{{StatusCode: 200, Body: respBody}}}
	logw := &stubLogWriter{}
	uc := NewSendUsecase(httpc, logw, &stubBreaker{allow: true}, logging.NewNoop(), 64<<20)

	in := baseInput()
	in.Body = mpBody
	in.Headers = map[string]string{"Content-Type": ct}
	in.LogRequestBody = true

	out := uc.Send(context.Background(), in)

	assert.Equal(t, int32(200), out.StatusCode)
	assert.Equal(t, respBody, out.Body, "клиент получает полный ответ")
	assert.Equal(t, mpBody, httpc.lastReqBody, "к внешнему узлу multipart-тело уходит байт-в-байт")

	require.Len(t, logw.written, 1)
	rec := logw.written[0].rec
	assert.True(t, domain.IsMultipartLogPlaceholder(rec.Request), "в лог — плейсхолдер, а не тело")
	assert.True(t, strings.HasPrefix(rec.Request, "multipart/form-data"))
	assert.NotContains(t, rec.Request, "BINARY-FILE-CONTENT-SECRET", "содержимое вложения в лог не попадает")
	assert.EqualValues(t, len(mpBody), rec.RequestSize, "истинный размер — по полному телу с вложением")
	assert.Equal(t, md5hex(mpBody), rec.ChecksumRequest, "checksum по полному телу")
}

// §68: max_body_size на multipart не действует — плейсхолдер сохраняется целиком,
// без маркера усечения (он и так короткий).
func TestSend_MultipartRequest_ExemptFromMaxBodySize(t *testing.T) {
	t.Parallel()

	ct, mpBody := buildMultipartBody(t)
	httpc := &stubHTTPCaller{responses: []*port.HTTPResponse{{StatusCode: 200}}}
	logw := &stubLogWriter{}
	uc := NewSendUsecase(httpc, logw, &stubBreaker{allow: true}, logging.NewNoop(), 64<<20)

	in := baseInput()
	in.Body = mpBody
	in.Headers = map[string]string{"Content-Type": ct}
	in.LogRequestBody = true
	in.MaxBodySizeEnabled = true
	in.MaxBodySize = 10 // маленький лимит — обычное тело был бы усечено

	uc.Send(context.Background(), in)

	require.Len(t, logw.written, 1)
	rec := logw.written[0].rec
	assert.True(t, domain.IsMultipartLogPlaceholder(rec.Request))
	assert.NotContains(t, rec.Request, truncationMarker, "плейсхолдер не режется по max_body_size")
}

// §68: логирование тела выключено — плейсхолдер тоже не пишется (гейт сохранён).
func TestSend_MultipartRequest_NoLogWhenBodyLoggingOff(t *testing.T) {
	t.Parallel()

	ct, mpBody := buildMultipartBody(t)
	httpc := &stubHTTPCaller{responses: []*port.HTTPResponse{{StatusCode: 200}}}
	logw := &stubLogWriter{}
	uc := NewSendUsecase(httpc, logw, &stubBreaker{allow: true}, logging.NewNoop(), 64<<20)

	in := baseInput()
	in.Body = mpBody
	in.Headers = map[string]string{"Content-Type": ct}
	in.LogRequestBody = false

	uc.Send(context.Background(), in)

	require.Len(t, logw.written, 1)
	rec := logw.written[0].rec
	assert.Empty(t, rec.Request, "тело не логируется → пусто, но размер сохранён")
	assert.EqualValues(t, len(mpBody), rec.RequestSize)
}

// §68: детект нечувствителен к регистру ключа заголовка (envelope/gRPC могут
// прислать "content-type").
func TestSend_MultipartRequest_LowercaseHeaderKey(t *testing.T) {
	t.Parallel()

	ct, mpBody := buildMultipartBody(t)
	httpc := &stubHTTPCaller{responses: []*port.HTTPResponse{{StatusCode: 200}}}
	logw := &stubLogWriter{}
	uc := NewSendUsecase(httpc, logw, &stubBreaker{allow: true}, logging.NewNoop(), 64<<20)

	in := baseInput()
	in.Body = mpBody
	in.Headers = map[string]string{"content-type": ct} // нижний регистр
	in.LogRequestBody = true

	uc.Send(context.Background(), in)

	require.Len(t, logw.written, 1)
	assert.True(t, domain.IsMultipartLogPlaceholder(logw.written[0].rec.Request))
}

// §68: симметрия для ответа с multipart Content-Type (multipart/mixed при
// скачивании и пр.) — в лог плейсхолдер, размер честный.
func TestSend_MultipartResponse_PlaceholderInLog(t *testing.T) {
	t.Parallel()

	ct, mpBody := buildMultipartBody(t)
	httpc := &stubHTTPCaller{responses: []*port.HTTPResponse{{
		StatusCode: 200,
		Headers:    map[string]string{"Content-Type": ct},
		Body:       mpBody,
	}}}
	logw := &stubLogWriter{}
	uc := NewSendUsecase(httpc, logw, &stubBreaker{allow: true}, logging.NewNoop(), 64<<20)

	in := baseInput()
	in.LogResponseBody = true

	out := uc.Send(context.Background(), in)

	assert.Equal(t, mpBody, out.Body, "клиент получает полный multipart-ответ")
	require.Len(t, logw.written, 1)
	rec := logw.written[0].rec
	assert.True(t, domain.IsMultipartLogPlaceholder(rec.Response), "ответ в логе — плейсхолдер")
	assert.NotContains(t, rec.Response, "BINARY-FILE-CONTENT-SECRET")
	assert.EqualValues(t, len(mpBody), rec.ResponseSize)
	assert.Equal(t, md5hex(mpBody), rec.ChecksumResponse)
}

// §68 РЕГРЕССИЯ: обычный (не-multipart) запрос логируется как раньше — плейсхолдер
// его не подменяет.
func TestSend_NonMultipartRequest_Unchanged(t *testing.T) {
	t.Parallel()

	httpc := &stubHTTPCaller{responses: []*port.HTTPResponse{{StatusCode: 200}}}
	logw := &stubLogWriter{}
	uc := NewSendUsecase(httpc, logw, &stubBreaker{allow: true}, logging.NewNoop(), 64<<20)

	in := baseInput()
	in.Body = []byte(`{"k":"v"}`)
	in.Headers = map[string]string{"Content-Type": "application/json"}
	in.LogRequestBody = true

	uc.Send(context.Background(), in)

	require.Len(t, logw.written, 1)
	rec := logw.written[0].rec
	assert.Equal(t, `{"k":"v"}`, rec.Request, "обычное тело в логе как есть")
	assert.False(t, domain.IsMultipartLogPlaceholder(rec.Request))
}
