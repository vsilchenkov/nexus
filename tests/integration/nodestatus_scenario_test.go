//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/nodestatus"
	"nexus/internal/sender/usecase"
	senderport "nexus/internal/sender/usecase/port"
	webredis "nexus/internal/web/adapter/out/redis"
)

// scenarioHTTPCaller — программируемый upstream: статус следующего ответа
// задаётся между вызовами Handle.
type scenarioHTTPCaller struct {
	mu     sync.Mutex
	status int32
}

func (s *scenarioHTTPCaller) set(status int32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = status
}

func (s *scenarioHTTPCaller) Do(_ context.Context, _ *senderport.HTTPRequest) (*senderport.HTTPResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &senderport.HTTPResponse{StatusCode: s.status, Body: []byte("x")}, nil
}

// scenarioLogWriter — no-op LogWriter (лог-контур не участвует в сценарии).
type scenarioLogWriter struct{}

func (scenarioLogWriter) Write(context.Context, string, *domain.LogRecord) {}
func (scenarioLogWriter) Flush(context.Context) error                      { return nil }

// scenarioNodeReader — фиксированный узел.
type scenarioNodeReader struct{ node *domain.Node }

func (r *scenarioNodeReader) GetByPath(context.Context, string) (*domain.Node, error) {
	return r.node, nil
}

// scenarioDLQ — no-op DLQProducer (не-2xx уходят в DLQ, здесь это не предмет проверки).
type scenarioDLQ struct{}

func (scenarioDLQ) Produce(context.Context, string, string, []byte, map[string]string) error {
	return nil
}

// TestNodeStatus_DegradedLifecycle_Redis (§52, сценарий инцидента §50.4):
// последовательность реальных доставок через AsyncProcessor.Handle с записью
// исхода в реальный Redis. Серия 422 (протухшие FCM-токены) → бейдж degraded
// (узел жив, НЕ down); затем 500 → down; затем 200 → ok. Проверяет цепочку
// классификация → кодек → Redis → декод целиком, глазами Web-читателя.
func TestNodeStatus_DegradedLifecycle_Redis(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	client, cleanup := startRedis(t, ctx)
	defer cleanup()

	logger := logging.NewNoop()
	// Порог 1 (§52.8) = «любой тяжёлый отказ сразу down»: сценарий проверяет
	// кодировку значения и чтение на стороне Web, а не накопление отказов —
	// с дефолтными 10 первый же down читался бы как degraded и тест проверял
	// бы совсем другое. Порог покрыт своими тестами в platform/nodestatus.
	writer := nodestatus.NewRedisWriter(client, 1, logger) // Sender-сторона
	reader := webredis.NewNodeStatusReaderRedis(client, logger)

	const nodePath = "site/push"
	node := &domain.Node{
		Path:            nodePath,
		Status:          domain.NodeStatusEnabled,
		TimeoutMs:       1000,
		ClickHouseTable: "nexus.log_site_push",
	}
	httpc := &scenarioHTTPCaller{}
	send := usecase.NewSendUsecase(httpc, scenarioLogWriter{}, nil, logger, 64<<20)
	proc := usecase.NewAsyncProcessor(&scenarioNodeReader{node: node}, send, scenarioDLQ{},
		nil, writer, "nexus.async.dlq", nil, logger)

	deliver := func(id string, status int32) {
		t.Helper()
		httpc.set(status)
		env, err := json.Marshal(usecase.Envelope{
			ID:         id,
			NodePath:   nodePath,
			Method:     "POST",
			TargetURL:  "https://push-ms.example.com/send",
			Body:       []byte(`{"token":"x"}`),
			ReceivedAt: time.Now().UTC(),
		})
		require.NoError(t, err)
		proc.Handle(ctx, env, nil)
	}

	outcome := func() domain.NodeOutcome {
		t.Helper()
		got, err := reader.GetLastOutcomes(ctx, []string{nodePath})
		require.NoError(t, err)
		v, ok := got[nodePath]
		require.True(t, ok, "после доставки исход должен быть в Redis")
		return v
	}

	// Серия 422 NotRegistered (§50.4) — узел жив и отвечает: degraded, не down.
	for i, id := range []string{"m-1", "m-2", "m-3"} {
		deliver(id, 422)
		require.Equal(t, domain.NodeOutcomeDegraded, outcome(),
			"422 #%d → degraded", i+1)
	}

	// Узел действительно упал (5xx) → down.
	deliver("m-4", 500)
	require.Equal(t, domain.NodeOutcomeDown, outcome(), "500 → down")

	// Узел восстановился (2xx) → ok.
	deliver("m-5", 200)
	require.Equal(t, domain.NodeOutcomeOK, outcome(), "200 → ok")
}
