package httpclient

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	extlog "github.com/vsilchenkov/logging"

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

	c := New(testCfg(), logging.NewNoop(), 0)
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

	c := New(testCfg(), logging.NewNoop(), 0)
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

	c := New(testCfg(), logging.NewNoop(), 0)
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

	c := New(testCfg(), logging.NewNoop(), 0)
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
	c := New(testCfg(), logging.NewNoop(), 0)
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
	c := New(testCfg(), logging.NewNoop(), 0)
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

	c := New(testCfg(), logging.NewNoop(), 0)
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

	c := New(testCfg(), logging.NewNoop(), 0)
	resp, err := c.Do(context.Background(), &port.HTTPRequest{
		Method:    "GET",
		URL:       srv.URL,
		TimeoutMs: 2000,
	})
	require.NoError(t, err)
	assert.Equal(t, size, len(resp.Body), "большое тело ответа целиком прочитано")
}

// §43-rev: ответ больше maxResponseBytes → TooLarge, тело НЕ дочитано в память
// (memory-safe); под лимитом — тело отдаётся целиком.
func TestDo_ResponseTooLarge_BoundedRead(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(strings.Repeat("a", 10_000)))
	}))
	defer srv.Close()

	t.Run("over limit → TooLarge, empty body", func(t *testing.T) {
		c := New(testCfg(), logging.NewNoop(), 1000) // лимит < 10000
		resp, err := c.Do(context.Background(), &port.HTTPRequest{Method: "GET", URL: srv.URL, TimeoutMs: 2000})
		require.NoError(t, err)
		assert.True(t, resp.TooLarge, "ответ больше лимита → TooLarge")
		assert.Empty(t, resp.Body, "тело не дочитано в память")
		assert.EqualValues(t, 200, resp.StatusCode)
	})

	t.Run("under limit → full body", func(t *testing.T) {
		c := New(testCfg(), logging.NewNoop(), 1<<20) // лимит > 10000
		resp, err := c.Do(context.Background(), &port.HTTPRequest{Method: "GET", URL: srv.URL, TimeoutMs: 2000})
		require.NoError(t, err)
		assert.False(t, resp.TooLarge)
		assert.Equal(t, 10_000, len(resp.Body))
	})
}

// bufLogger — логгер поверх bytes.Buffer с уровнем Info (NewNoop глушит Info/Warn).
func bufLogger() (logging.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	l := extlog.NewLogger(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	return l, buf
}

// TestClient_Do_FollowsRedirect_GET_Info (§50): GET-редирект следуется, метод не
// меняется → Info-лог; resp.Redirects несёт хоп; тело берётся с финального узла.
func TestClient_Do_FollowsRedirect_GET_Info(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("final"))
	}))
	defer upstream.Close()
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, upstream.URL+"/final", http.StatusFound) // 302
	}))
	defer front.Close()

	logger, buf := bufLogger()
	c := New(testCfg(), logger, 0)
	resp, err := c.Do(context.Background(), &port.HTTPRequest{
		Method: "GET", URL: front.URL, NodePath: "svc/hook", TimeoutMs: 2000,
	})
	require.NoError(t, err)
	assert.Equal(t, int32(200), resp.StatusCode)
	assert.Equal(t, "final", string(resp.Body), "тело с финального адреса")
	require.Len(t, resp.Redirects, 1)
	assert.Equal(t, 302, resp.Redirects[0].Status)
	assert.Equal(t, "GET", resp.Redirects[0].FromMethod)
	assert.Equal(t, "GET", resp.Redirects[0].ToMethod)
	assert.NotContains(t, resp.Redirects[0].From, "?", "URL отредачен без query")
	log := buf.String()
	assert.Contains(t, log, "sender follows external redirect")
	assert.Contains(t, log, "node=svc/hook")
	assert.NotContains(t, log, "changed method", "GET-редирект не меняет метод")
}

// TestClient_Do_Redirect_POST_301_ChangesMethod_Warn (§50): 301 на POST
// превращает его в GET (тело теряется) → Warn-лог + флаг в хопе.
func TestClient_Do_Redirect_POST_301_ChangesMethod_Warn(t *testing.T) {
	t.Parallel()

	var upstreamMethod string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamMethod = r.Method
		w.WriteHeader(200)
	}))
	defer upstream.Close()
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, upstream.URL+"/moved", http.StatusMovedPermanently) // 301
	}))
	defer front.Close()

	logger, buf := bufLogger()
	c := New(testCfg(), logger, 0)
	resp, err := c.Do(context.Background(), &port.HTTPRequest{
		Method: "POST", URL: front.URL, NodePath: "svc/hook", Body: []byte(`{"a":1}`), TimeoutMs: 2000,
	})
	require.NoError(t, err)
	assert.Equal(t, int32(200), resp.StatusCode)
	assert.Equal(t, "GET", upstreamMethod, "301 превратил POST в GET на финальном узле")
	require.Len(t, resp.Redirects, 1)
	assert.Equal(t, "POST", resp.Redirects[0].FromMethod)
	assert.Equal(t, "GET", resp.Redirects[0].ToMethod)
	assert.Contains(t, buf.String(), "sender redirect changed method (request body dropped)")
}

// TestClient_Do_TooManyRedirects_Errors (§50): бесконечный редирект на себя
// обрывается после лимита с ошибкой (как дефолт Go).
func TestClient_Do_TooManyRedirects_Errors(t *testing.T) {
	t.Parallel()

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/loop", http.StatusFound)
	}))
	defer srv.Close()

	c := New(testCfg(), logging.NewNoop(), 0)
	_, err := c.Do(context.Background(), &port.HTTPRequest{
		Method: "GET", URL: srv.URL, TimeoutMs: 2000,
	})
	require.Error(t, err, "цикл редиректов должен оборваться ошибкой")
	assert.Contains(t, err.Error(), "redirects")
}

// TestRedactURL_And_SchemeChange (§50): чистые хелперы — query отрезан, схема
// классифицирована.
func TestRedactURL_And_SchemeChange(t *testing.T) {
	t.Parallel()

	u, _ := url.Parse("http://x.io/a/b?token=secret&x=1")
	assert.Equal(t, "http://x.io/a/b", redactURL(u), "query отрезан (токен не течёт)")
	assert.Equal(t, "", redactURL(nil))

	mk := func(s string) *url.URL { u, _ := url.Parse(s); return u }
	assert.Equal(t, "upgrade", schemeChange(mk("http://x/a"), mk("https://x/a")))
	assert.Equal(t, "downgrade", schemeChange(mk("https://x/a"), mk("http://x/a")))
	assert.Equal(t, "same", schemeChange(mk("https://x/a"), mk("https://y/b")))
}
