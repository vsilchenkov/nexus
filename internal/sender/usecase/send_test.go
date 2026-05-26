package usecase

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/sender/usecase/port"
)

// stubHTTPCaller — программируемый ответ. Если Body/Status — функция,
// дёрнем её на каждой попытке (для retry-сценариев).
type stubHTTPCaller struct {
	mu        sync.Mutex
	calls     int
	responses []*port.HTTPResponse // последовательность по попыткам
	errs      []error
}

func (s *stubHTTPCaller) Do(_ context.Context, _ *port.HTTPRequest) (*port.HTTPResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	successCount int
	failureCount int
	allowErr     error
}

func (b *stubBreaker) Allow(_ context.Context, _ string) (bool, error) {
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
		ClickHouseTable: "databus.log_partner_echo",
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

func TestSend_Success_RecordsLogAndUpdatesBreaker(t *testing.T) {
	t.Parallel()

	httpc := &stubHTTPCaller{
		responses: []*port.HTTPResponse{
			{StatusCode: 200, Body: []byte(`{"ok":true}`), Headers: map[string]string{"X-Echo": "1"}},
		},
	}
	logw := &stubLogWriter{}
	cb := &stubBreaker{allow: true}

	uc := NewSendUsecase(httpc, logw, cb, logging.NewNoop())
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
	assert.Equal(t, "databus.log_partner_echo", logw.written[0].table)
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

func TestSend_CircuitBreakerOpen_Returns503WithoutHTTPCall(t *testing.T) {
	t.Parallel()

	httpc := &stubHTTPCaller{} // не должен быть дёрнут
	logw := &stubLogWriter{}
	cb := &stubBreaker{allow: false}

	uc := NewSendUsecase(httpc, logw, cb, logging.NewNoop())
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

func TestSend_4xx_NoRetryAndRecordsFailure(t *testing.T) {
	t.Parallel()

	httpc := &stubHTTPCaller{
		responses: []*port.HTTPResponse{
			{StatusCode: 404, Body: []byte(`not found`)},
		},
	}
	logw := &stubLogWriter{}
	cb := &stubBreaker{allow: true}

	uc := NewSendUsecase(httpc, logw, cb, logging.NewNoop())
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

	// Breaker: 4xx — это failure (Done=false → RecordFailure).
	assert.Equal(t, 0, cb.successCount)
	assert.Equal(t, 1, cb.failureCount)
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

	uc := NewSendUsecase(httpc, logw, cb, logging.NewNoop())
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

	uc := NewSendUsecase(httpc, logw, cb, logging.NewNoop())
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

	uc := NewSendUsecase(httpc, logw, cb, logging.NewNoop())
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

	uc := NewSendUsecase(httpc, logw, nil /* CB */, logging.NewNoop())
	out := uc.Send(context.Background(), baseInput())
	// noopBreaker.Allow=true, ничего не падает — это и есть проверка.
	assert.Equal(t, int32(200), out.StatusCode)
}
