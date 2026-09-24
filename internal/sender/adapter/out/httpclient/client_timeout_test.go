package httpclient

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/logging"
	"nexus/internal/sender/usecase/port"
)

// silentTLSListener — TCP принимается, но на ClientHello никто не отвечает:
// TLS-рукопожатие не завершается никогда. Так выглядит «таймаут соединения» —
// дедлайн истекает, а соединения с получателем так и не случилось.
func silentTLSListener(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	var (
		mu    sync.Mutex
		conns []net.Conn
		wg    sync.WaitGroup
	)
	wg.Go(func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	})
	t.Cleanup(func() {
		_ = ln.Close() // закрытие listener'а завершает цикл Accept
		wg.Wait()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	return "https://" + ln.Addr().String() + "/"
}

// TestClient_Do_ResponseTimeoutMarking — §107.3: port.ErrResponseTimeout
// получает только дедлайн, истёкший ПОСЛЕ установки соединения (получатель
// принял запрос и молчит либо не отдал тело). Дедлайн на рукопожатии и отказ
// соединения — признаки того, что получателя нет, и помечаться не должны.
//
// Во всех случаях с дедлайном цепочка сохраняется: failureReason, 504 клиенту
// и out.Timeout держатся на errors.Is(err, context.DeadlineExceeded).
func TestClient_Do_ResponseTimeoutMarking(t *testing.T) {
	t.Parallel()

	silent := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(silent.Close)
	stalledBody := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(stalledBody.Close)

	tests := []struct {
		name         string
		url          string
		wantMarked   bool
		wantDeadline bool
	}{
		{"receiver accepted and is silent", silent.URL, true, true},
		{"headers sent, body never ends (SSE)", stalledBody.URL, true, true},
		{"tls handshake never completes", silentTLSListener(t), false, true},
		{"connection refused", "http://127.0.0.1:1/", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := testCfg()
			// Рукопожатие обязано упереться в дедлайн вызова, а не в свой таймаут.
			cfg.TLSHandshakeTimeoutMs = 10_000
			c := New(cfg, logging.NewNoop(), 0)

			resp, err := c.Do(context.Background(), &port.HTTPRequest{
				Method: http.MethodGet, URL: tt.url, TimeoutMs: 200,
			})

			require.Error(t, err)
			assert.Nil(t, resp)
			assert.Equal(t, tt.wantMarked, errors.Is(err, port.ErrResponseTimeout), "err=%v", err)
			assert.Equal(t, tt.wantDeadline, errors.Is(err, context.DeadlineExceeded), "err=%v", err)
		})
	}
}
