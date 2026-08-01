package receiver_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/logging"
	recvclient "nexus/internal/web/adapter/out/receiver"
	"nexus/internal/web/usecase/port"
)

// Диспетчер — путь replay (§7.4.1): Web отправляет сохранённый запрос обратно
// в шину через публичный вход Receiver. Ошибка в сборке адреса или потеря
// заголовков видна только на живом стенде, поэтому здесь — httptest-двойник
// Receiver, без сети и контейнеров.

type capturedRequest struct {
	method   string
	path     string
	rawQuery string
	headers  http.Header
	body     string
}

// newDispatcher поднимает фейковый Receiver и диспетчер, нацеленный на него.
func newDispatcher(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) (*recvclient.HTTPDispatcher, <-chan capturedRequest) {
	t.Helper()

	got := make(chan capturedRequest, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		if r.ContentLength > 0 {
			_, _ = r.Body.Read(buf)
		}
		got <- capturedRequest{
			method:   r.Method,
			path:     r.URL.Path,
			rawQuery: r.URL.RawQuery,
			headers:  r.Header.Clone(),
			body:     string(buf),
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	return recvclient.NewHTTPDispatcher(srv.URL, 5*time.Second, logging.NewNoop()), got
}

func TestDispatch_SyncPathAndQuery(t *testing.T) {
	t.Parallel()

	d, got := newDispatcher(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Node", "partner")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	resp, err := d.Dispatch(context.Background(), port.DispatchRequest{
		NodePath: "/partner/orders",
		Method:   http.MethodPut,
		Query:    url.Values{"__replay_of": []string{"abc"}},
		Headers:  map[string]string{"X-Custom": "v1"},
		Body:     []byte(`{"a":1}`),
	})
	require.NoError(t, err)

	req := <-got
	assert.Equal(t, http.MethodPut, req.method)
	assert.Equal(t, "/api/v1/request/partner/orders", req.path,
		"ведущий слэш пути узла не должен удваиваться в адресе")
	assert.Equal(t, "__replay_of=abc", req.rawQuery)
	assert.Equal(t, "v1", req.headers.Get("X-Custom"))
	assert.Equal(t, `{"a":1}`, req.body)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, `{"ok":true}`, string(resp.Body))
	assert.Equal(t, "partner", resp.Headers["X-Node"], "заголовки ответа доезжают до вызывающего")
}

func TestDispatch_AsyncUsesRequestAsyncRoot(t *testing.T) {
	t.Parallel()

	d, got := newDispatcher(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})

	resp, err := d.Dispatch(context.Background(), port.DispatchRequest{
		NodePath: "partner/orders",
		Async:    true,
		Body:     []byte(`{}`),
	})
	require.NoError(t, err)

	req := <-got
	assert.Equal(t, "/api/v1/requestAsync/partner/orders", req.path)
	assert.Equal(t, http.MethodPost, req.method, "пустой метод по умолчанию POST")
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
}

// Content-Type проставляется сам, только если тело — валидный JSON: replay
// хранит оригинальное тело, и объявить JSON'ом что попало нельзя.
func TestDispatch_ContentTypeInference(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		body    []byte
		headers map[string]string
		want    string
	}{
		{name: "json body", body: []byte(`{"a":1}`), want: "application/json"},
		{name: "не json", body: []byte("plain text"), want: ""},
		{name: "пустое тело", body: nil, want: ""},
		{
			name:    "явный заголовок не перетирается",
			body:    []byte(`{"a":1}`),
			headers: map[string]string{"Content-Type": "application/xml"},
			want:    "application/xml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			d, got := newDispatcher(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
			_, err := d.Dispatch(context.Background(), port.DispatchRequest{
				NodePath: "n/p",
				Headers:  tt.headers,
				Body:     tt.body,
			})
			require.NoError(t, err)

			req := <-got
			assert.Equal(t, tt.want, req.headers.Get("Content-Type"))
		})
	}
}

func TestDispatch_UnreachableReceiver(t *testing.T) {
	t.Parallel()

	// Диспетчер на заведомо закрытый порт: ошибка обязана дойти до вызывающего,
	// а не превратиться в пустой ответ.
	d := recvclient.NewHTTPDispatcher("http://127.0.0.1:1", time.Second, logging.NewNoop())

	resp, err := d.Dispatch(context.Background(), port.DispatchRequest{NodePath: "n/p"})
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "dispatch to receiver")
}

func TestDispatch_BaseURLTrailingSlash(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/request/n/p", r.URL.Path, "двойной слэш в адресе Receiver даст 404")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	d := recvclient.NewHTTPDispatcher(srv.URL+"/", time.Second, logging.NewNoop())
	_, err := d.Dispatch(context.Background(), port.DispatchRequest{NodePath: "n/p"})
	require.NoError(t, err)
}
