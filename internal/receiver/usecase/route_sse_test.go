package usecase

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	senderv1 "nexus/proto/sender/v1"
)

// countingSender — Sender, который считает вызовы: отказ от подписки обязан
// обойтись без получателя, иначе поток снова висел бы до таймаута узла.
type countingSender struct{ calls atomic.Int32 }

func (s *countingSender) Send(_ context.Context, _ *senderv1.SendRequest) (*senderv1.SendResponse, error) {
	s.calls.Add(1)
	return &senderv1.SendResponse{StatusCode: http.StatusOK, Body: []byte(`{}`)}, nil
}

// TestRoute_SSEDecline — §107.2: порядок проверок в checkNodeAcceptsSync.
// Выключенный узел остаётся «узла нет»; подписка отклоняется ДО паузы (не
// превращается в сообщение очереди) и ДО метода и авторизации.
func TestRoute_SSEDecline(t *testing.T) {
	t.Parallel()

	sse := http.Header{"Accept": []string{"text/event-stream"}}
	tests := []struct {
		name     string
		status   domain.NodeStatus
		flag     bool
		method   string
		incoming domain.IncomingAuthType
		wantErr  error
		wantSend bool
	}{
		{"subscription declined", domain.NodeStatusEnabled, true, http.MethodGet, domain.IncomingAuthTypeNone, domain.ErrStreamDeclined, false},
		{"flag off: subscription goes to sender", domain.NodeStatusEnabled, false, http.MethodGet, domain.IncomingAuthTypeNone, nil, true},
		{"tool call passes", domain.NodeStatusEnabled, true, http.MethodPost, domain.IncomingAuthTypeNone, nil, true},
		{"paused node declines instead of queueing", domain.NodeStatusPaused, true, http.MethodGet, domain.IncomingAuthTypeNone, domain.ErrStreamDeclined, false},
		{"disabled node stays hidden", domain.NodeStatusDisabled, true, http.MethodGet, domain.IncomingAuthTypeNone, domain.ErrNodeDisabled, false},
		{"declined before incoming auth", domain.NodeStatusEnabled, true, http.MethodGet, domain.IncomingAuthTypeToken, domain.ErrStreamDeclined, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			node := &domain.Node{
				Path:                    "mcp-1c",
				RootMethod:              domain.RootMethodRequest,
				IncomingMethod:          domain.HTTPMethodAny,
				OutgoingMethod:          domain.HTTPMethodAny,
				URLMode:                 domain.URLModeStatic,
				TargetURL:               "http://mcp.example:8172/mcp",
				AuthType:                domain.AuthTypeNone,
				IncomingAuthType:        tt.incoming,
				IncomingAuthCredentials: "secret",
				Status:                  tt.status,
				SSEDecline:              tt.flag,
			}
			sender := &countingSender{}
			u := NewRouteUsecase(stubNodeReader{node: node}, sender, 5, logging.NewNoop())

			_, err := u.Route(context.Background(), RouteInput{
				NodePath: "mcp-1c", Method: tt.method, Header: sse.Clone(),
			})

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantSend, sender.calls.Load() == 1, "вызов получателя")
		})
	}
}

// TestRoute_BreakerIgnoreTimeoutsForwarded (§107.3): sync-путь Sender'а узел из
// БД не читает — флаг обязан уехать полем запроса. Потеряй его buildSendRequest,
// настройка в карточке узла не действовала бы на sync-узлах вовсе.
func TestRoute_BreakerIgnoreTimeoutsForwarded(t *testing.T) {
	t.Parallel()
	for _, flag := range []bool{true, false} {
		node := methodTestNode()
		node.BreakerIgnoreTimeouts = flag
		sender := &capturingSender{}
		u := NewRouteUsecase(stubNodeReader{node: node}, sender, 5, logging.NewNoop())

		_, err := u.Route(context.Background(), RouteInput{NodePath: node.Path, Method: "POST"})

		require.NoError(t, err)
		require.NotNil(t, sender.last)
		assert.Equal(t, flag, sender.last.GetBreakerIgnoreTimeouts())
	}
}
