package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/receiver/usecase"
	senderv1 "nexus/proto/sender/v1"
)

// countingSyncSender — Sender, считающий вызовы: 405 на подписку обязан
// обходиться без получателя.
type countingSyncSender struct{ calls atomic.Int32 }

func (s *countingSyncSender) Send(_ context.Context, _ *senderv1.SendRequest) (*senderv1.SendResponse, error) {
	s.calls.Add(1)
	return &senderv1.SendResponse{
		StatusCode: http.StatusOK,
		Body:       []byte(`{"jsonrpc":"2.0"}`),
		Headers:    map[string]string{"Content-Type": "application/json"},
	}, nil
}

// TestHandleSync_SSEDecline — §107.2 на уровне HTTP: подписка получает 405 с
// Allow и JSON-телом, получатель не вызывается, а журнал отказов §94 и метрика
// отказов молчат — статус задала настройка узла, шина отработала штатно.
// Короткий адрес §78 идёт через тот же handleSync.
func TestHandleSync_SSEDecline(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		incoming  domain.HTTPMethod
		method    string
		target    string
		wantCode  int
		wantAllow []string
		wantSend  bool
	}{
		{"ANY node declines subscription", domain.HTTPMethodAny, http.MethodGet, "/api/v1/request/mcp",
			http.StatusMethodNotAllowed, []string{"POST, PUT, PATCH, DELETE, HEAD, OPTIONS"}, false},
		{"short address §78 declines too", domain.HTTPMethodAny, http.MethodGet, "/api/v1/default/mcp",
			http.StatusMethodNotAllowed, []string{"POST, PUT, PATCH, DELETE, HEAD, OPTIONS"}, false},
		{"GET-only node sends empty Allow", domain.HTTPMethodGET, http.MethodGet, "/api/v1/request/mcp",
			http.StatusMethodNotAllowed, []string{""}, false},
		{"tool call reaches upstream", domain.HTTPMethodAny, http.MethodPost, "/api/v1/request/mcp",
			http.StatusOK, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gin.SetMode(gin.TestMode)
			node := &domain.Node{
				Path:             "mcp",
				RootMethod:       domain.RootMethodRequest,
				Status:           domain.NodeStatusEnabled,
				URLMode:          domain.URLModeStatic,
				TargetURL:        "http://mcp.example:8172/mcp",
				IncomingMethod:   tt.incoming,
				OutgoingMethod:   domain.HTTPMethodAny,
				AuthType:         domain.AuthTypeNone,
				IncomingAuthType: domain.IncomingAuthTypeNone,
				SSEDecline:       true,
			}
			sender := &countingSyncSender{}
			route := usecase.NewRouteUsecase(syncStubReader{node: node}, sender, 5, logging.NewNoop())
			h := New(route, nil, usecase.NewBodyLimitsProvider(0, 0, logging.NewNoop()), nil, logging.NewNoop())

			sink, m := &captureSink{}, &captureMetrics{}
			r := gin.New()
			r.Use(RejectLogMiddleware(sink, m))
			h.Register(r)

			req := httptest.NewRequest(tt.method, tt.target, strings.NewReader(""))
			req.Header.Set("Accept", "text/event-stream")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			require.Equal(t, tt.wantCode, w.Code)
			assert.Equal(t, tt.wantSend, sender.calls.Load() == 1, "вызов получателя")
			assert.Empty(t, sink.samples, "405 по настройке узла — не отказ шины")
			assert.Empty(t, m.calls, "метрика отказов не двигается")
			if tt.wantCode != http.StatusMethodNotAllowed {
				return
			}
			assert.Equal(t, tt.wantAllow, w.Header().Values("Allow"), "RFC 9110: 405 обязан нести Allow")
			assert.JSONEq(t, `{"error":"event stream is not supported by this node"}`, w.Body.String())
		})
	}
}
