package http

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/metrics"
	"nexus/internal/receiver/usecase"
	senderv1 "nexus/proto/sender/v1"
)

// e2eNodeReader всегда отдаёт один статический request-узел.
type e2eNodeReader struct{}

func (e2eNodeReader) Get(_ context.Context, _, path string) (*domain.Node, error) {
	return &domain.Node{
		Path:             path,
		RootMethod:       domain.RootMethodRequest,
		IncomingMethod:   domain.HTTPMethodPOST,
		OutgoingMethod:   domain.HTTPMethodPOST,
		URLMode:          domain.URLModeStatic,
		TargetURL:        "http://self/api/v1/request/" + path, // не используется loopback-сендером
		AuthType:         domain.AuthTypeNone,
		IncomingAuthType: domain.IncomingAuthTypeNone,
		Status:           domain.NodeStatusEnabled,
	}, nil
}

// loopbackSender имитирует target_url == собственный ingress: на каждый Send он
// делает РЕАЛЬНЫЙ HTTP-запрос обратно в тот же Receiver, перенося полученные
// заголовки (включая инкрементированный X-Nexus-Hops). Так воспроизводится
// настоящая рекурсия запросов через шину.
type loopbackSender struct {
	baseURL string
	calls   atomic.Int32
}

func (s *loopbackSender) Send(ctx context.Context, req *senderv1.SendRequest) (*senderv1.SendResponse, error) {
	s.calls.Add(1)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.baseURL+"/api/v1/request/"+req.GetNodePath(), bytes.NewReader(req.GetBody()))
	if err != nil {
		return nil, err
	}
	for k, v := range req.GetHeaders() {
		httpReq.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return &senderv1.SendResponse{StatusCode: int32(resp.StatusCode), Body: body}, nil
}

// TestLoopProtection_E2E_TerminatesAt508 — сквозной прогон защиты от
// зацикливания (§32): запрос с target_url, ведущим на сам Receiver, обрывается
// 508 Loop Detected ровно за max_hops проходов, без бесконечной рекурсии.
func TestLoopProtection_E2E_TerminatesAt508(t *testing.T) {
	const maxHops = 5

	gin.SetMode(gin.TestMode)
	sender := &loopbackSender{}
	m := metrics.New("receiver")

	routeUC := usecase.NewRouteUsecase(e2eNodeReader{}, sender, maxHops, logging.NewNoop())
	routeAsyncUC := usecase.NewRouteAsyncUsecase(e2eNodeReader{}, nil, "nexus.async", maxHops, logging.NewNoop())
	h := New(routeUC, routeAsyncUC, 1<<20, 1<<20, m, logging.NewNoop())

	r := gin.New()
	h.Register(r)
	srv := httptest.NewServer(r)
	defer srv.Close()
	sender.baseURL = srv.URL

	// Старт цепочки: клиент без hop-заголовка.
	resp, err := http.Post(srv.URL+"/api/v1/request/demo/loop", "application/json", nil)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	// Самый глубокий виток вернул 508 — он проксируется обратно до клиента.
	assert.Equal(t, http.StatusLoopDetected, resp.StatusCode)
	// Рекурсия ограничена: Sender вызван ровно maxHops раз (на (maxHops+1)-м
	// входе запрос отклоняется ДО обращения к Sender).
	assert.Equal(t, int32(maxHops), sender.calls.Load())
	// Метрика петли инкрементирована (хотя бы один раз — на самом глубоком витке).
	assert.GreaterOrEqual(t, testutil.ToFloat64(m.LoopDetectedTotal.WithLabelValues("sync")), float64(1))
}
