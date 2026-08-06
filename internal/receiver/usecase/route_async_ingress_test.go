package usecase

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
)

// ingressNode — узел заданного корневого метода и статуса, минимально годный для
// маршрутизации.
func ingressNode(root domain.RootMethod, status domain.NodeStatus) *domain.Node {
	return &domain.Node{
		Path:             "demo/path",
		RootMethod:       root,
		Status:           status,
		URLMode:          domain.URLModeStatic,
		TargetURL:        "https://example.com/hook",
		IncomingMethod:   domain.HTTPMethodAny,
		OutgoingMethod:   domain.HTTPMethodPOST,
		AuthType:         domain.AuthTypeNone,
		IncomingAuthType: domain.IncomingAuthTypeNone,
	}
}

// TestRouteAsync_ExternalIngressRequiresAsyncNode — §82.3: внешний
// /api/v1/requestAsync/ принимает только узлы с root_method=requestAsync.
//
// До §82 проверки не было вовсе: послабление делалось ради §3.6 (paused-узел
// копит запросы в очереди), но выдавалось безусловно — то есть наружу. Боевое
// следствие: узел acs_sigur с root_method=request принимал ~11 async-запросов
// в минуту от клиента, застрявшего на одной записи журнала больше суток, и
// отбить его настройкой узла было нечем.
func TestRouteAsync_ExternalIngressRequiresAsyncNode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		root       domain.RootMethod
		wantErr    error
		wantQueued bool
	}{
		{
			name:       "async-узел проходит",
			root:       domain.RootMethodRequestAsync,
			wantQueued: true,
		},
		{
			name:    "sync-узел отбивается",
			root:    domain.RootMethodRequest,
			wantErr: domain.ErrNodeNotAsyncIngress,
		},
		{
			// Побочный, но нужный эффект: у pull-узла входящего HTTP нет вовсе.
			// Короткая форма §78.1 его честно отдавала 404, а async-эндпоинт
			// принимал — из-за того же отсутствия проверки.
			name:    "pull-узел RabbitMQAsync отбивается",
			root:    domain.RootMethodRabbitMQAsync,
			wantErr: domain.ErrNodeNotAsyncIngress,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			producer := &stubProducer{}
			u := NewRouteAsyncUsecase(
				stubNodeReader{node: ingressNode(tc.root, domain.NodeStatusEnabled)},
				producer, "nexus.async", 5, logging.NewNoop())

			res, err := u.RouteAsync(context.Background(), RouteInput{
				NodePath:      "demo/path",
				Method:        "POST",
				ExternalAsync: true,
			})

			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				assert.Zero(t, producer.calls, "в очередь ничего уйти не должно")
				return
			}
			require.NoError(t, err)
			require.NotNil(t, res)
			assert.Equal(t, 1, producer.calls)
		})
	}
}

// TestRouteAsync_InternalCallersBypassIngressGate — границы гейта §82.3.
//
// RouteAsync зовут не только из внешнего эндпоинта, и обоим остальным путям
// требование root_method=requestAsync противопоказано. Тест держит эти границы
// явно: без него сужение гейта до «любой async-вызов» выглядело бы безобидным,
// а ломало бы §3.6 и §16.
func TestRouteAsync_InternalCallersBypassIngressGate(t *testing.T) {
	t.Parallel()

	t.Run("§3.6: paused sync-узел копит запросы в очереди", func(t *testing.T) {
		t.Parallel()
		producer := &stubProducer{}
		u := NewRouteAsyncUsecase(
			stubNodeReader{node: ingressNode(domain.RootMethodRequest, domain.NodeStatusPaused)},
			producer, "nexus.async", 5, logging.NewNoop())

		// ExternalAsync не ставится: sync-handler зовёт RouteAsync со своим
		// RouteInput после ErrNodePaused.
		res, err := u.RouteAsync(context.Background(), RouteInput{NodePath: "demo/path", Method: "POST"})
		require.NoError(t, err)
		assert.True(t, res.Queued)
		assert.Equal(t, 1, producer.calls)
	})

	t.Run("§16: callback на sync-узел по-прежнему проходит", func(t *testing.T) {
		t.Parallel()
		// Граница осознанная: callback защищён не root_method'ом, а обязательной
		// HMAC-подписью, а узел под callback может быть законно request-узлом.
		node := ingressNode(domain.RootMethodRequest, domain.NodeStatusEnabled)
		node.IncomingAuthType = domain.IncomingAuthTypeWebhookSignature
		node.IncomingAuthCredentials = "shared-secret"
		node.WebhookSignatureHeader = "X-Hub-Signature-256"
		node.WebhookSignaturePrefix = "sha256="

		body := []byte(`{"event":"ping"}`)
		hdr := http.Header{}
		hdr.Set("X-Hub-Signature-256", "sha256="+sign("shared-secret", string(body)))

		producer := &stubProducer{}
		u := NewRouteAsyncUsecase(stubNodeReader{node: node}, producer, "nexus.async", 5, logging.NewNoop())

		res, err := u.RouteAsync(context.Background(), RouteInput{
			NodePath:        "demo/path",
			Method:          "POST",
			Header:          hdr,
			Body:            body,
			RequireCallback: true,
		})
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.Equal(t, 1, producer.calls)
	})
}
