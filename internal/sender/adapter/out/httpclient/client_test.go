package httpclient

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/config"
	"nexus/internal/platform/logging"
	"nexus/internal/sender/usecase/port"
)

// testCfg возвращает базовый SenderHTTPClientConfig для httptest-сценариев.
// Таймауты подобраны коротко, чтобы тест "timeout" завершался за < 1 сек.
func testCfg() *config.SenderHTTPClientConfig {
	return &config.SenderHTTPClientConfig{
		TimeoutMs:             2000,
		MaxIdleConns:          10,
		MaxIdleConnsPerHost:   5,
		IdleConnTimeoutSec:    30,
		DialTimeoutMs:         1000,
		TLSHandshakeTimeoutMs: 1000,
	}
}

func TestClient_Do_HappyPath_200(t *testing.T) {
	t.Parallel()

	var (
		gotMethod string
		gotHeader string
		gotBody   []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotHeader = r.Header.Get("X-Forwarded-For")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("X-Custom", "value-1")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := New(testCfg(), logging.NewNoop())
	resp, err := c.Do(context.Background(), &port.HTTPRequest{
		Method:    "POST",
		URL:       srv.URL + "/x",
		Headers:   map[string]string{"X-Forwarded-For": "1.2.3.4", "Content-Type": "application/json"},
		Body:      []byte(`{"a":1}`),
		TimeoutMs: 1000,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.Equal(t, int32(200), resp.StatusCode)
	assert.Equal(t, "POST", gotMethod)
	assert.Equal(t, "1.2.3.4", gotHeader, "request headers пробрасываются")
	assert.Equal(t, `{"a":1}`, string(gotBody), "request body передан как есть")
	assert.Equal(t, `{"ok":true}`, string(resp.Body))
	assert.Equal(t, "value-1", resp.Headers["X-Custom"], "response headers собираются (первое значение)")
	assert.Equal(t, "application/json", resp.Headers["Content-Type"])
}

func TestClient_Do_PreservesNon2xxStatus(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		_, _ = w.Write([]byte("service unavailable"))
	}))
	defer srv.Close()

	c := New(testCfg(), logging.NewNoop())
	resp, err := c.Do(context.Background(), &port.HTTPRequest{
		Method: "GET",
		URL:    srv.URL,
		Body:   nil,
	})
	require.NoError(t, err, "5xx ответ — это не error из Do, статус передаётся в resp.StatusCode")
	assert.Equal(t, int32(503), resp.StatusCode)
	assert.Equal(t, "service unavailable", string(resp.Body))
}

func TestClient_Do_TimeoutFromRequest(t *testing.T) {
	t.Parallel()

	// Сервер отвечает с задержкой 200ms; задаём TimeoutMs=50ms через req.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(200 * time.Millisecond):
			w.WriteHeader(200)
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	c := New(testCfg(), logging.NewNoop())
	resp, err := c.Do(context.Background(), &port.HTTPRequest{
		Method:    "GET",
		URL:       srv.URL,
		TimeoutMs: 50,
	})
	require.Error(t, err)
	assert.Nil(t, resp)
	// net/http возвращает url.Error с "context deadline exceeded" или "Client.Timeout".
	msg := err.Error()
	assert.True(t,
		strings.Contains(msg, "deadline") ||
			strings.Contains(msg, "Timeout") ||
			strings.Contains(msg, "timeout"),
		"ожидался timeout-error, получено: %q", msg)
}

func TestClient_Do_DefaultsTimeoutWhenZero(t *testing.T) {
	t.Parallel()

	// При TimeoutMs <= 0 Do использует default 30s (см. client.go:55). Здесь
	// важно убедиться, что вызов не падает мгновенно и проходит успешно.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	c := New(testCfg(), logging.NewNoop())
	resp, err := c.Do(context.Background(), &port.HTTPRequest{
		Method:    "GET",
		URL:       srv.URL,
		TimeoutMs: 0,
	})
	require.NoError(t, err)
	assert.Equal(t, int32(200), resp.StatusCode)
}

func TestClient_Do_ConnectionRefused(t *testing.T) {
	t.Parallel()

	// 127.0.0.1:1 практически гарантированно даёт connection refused.
	c := New(testCfg(), logging.NewNoop())
	resp, err := c.Do(context.Background(), &port.HTTPRequest{
		Method:    "GET",
		URL:       "http://127.0.0.1:1/",
		TimeoutMs: 500,
	})
	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestClient_Do_InvalidMethod_ReturnsBuildError(t *testing.T) {
	t.Parallel()

	// Method содержит запрещённый символ → http.NewRequestWithContext
	// возвращает ошибку "net/http: invalid method".
	c := New(testCfg(), logging.NewNoop())
	resp, err := c.Do(context.Background(), &port.HTTPRequest{
		Method: "BAD METHOD",
		URL:    "http://example.com/",
	})
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "build http request")
}

func TestClient_Do_ContextCancel(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	c := New(testCfg(), logging.NewNoop())
	_, err := c.Do(ctx, &port.HTTPRequest{
		Method:    "GET",
		URL:       srv.URL,
		TimeoutMs: 5000,
	})
	require.Error(t, err, "при cancel'е родительского контекста Do должен вернуть error")
}

func TestClient_Do_LargeResponseBody(t *testing.T) {
	t.Parallel()

	const size = 64 * 1024
	payload := strings.Repeat("a", size)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	c := New(testCfg(), logging.NewNoop())
	resp, err := c.Do(context.Background(), &port.HTTPRequest{
		Method:    "GET",
		URL:       srv.URL,
		TimeoutMs: 2000,
	})
	require.NoError(t, err)
	assert.Equal(t, size, len(resp.Body), "большое тело ответа целиком прочитано")
}
