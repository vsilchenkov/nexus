package grpc

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/metrics"
	"nexus/internal/sender/usecase"
	"nexus/internal/sender/usecase/port"
	senderv1 "nexus/proto/sender/v1"
)

// stubHTTPCaller — фиксированный ответ либо транспортная ошибка.
type stubHTTPCaller struct {
	resp *port.HTTPResponse
	err  error
}

func (s *stubHTTPCaller) Do(context.Context, *port.HTTPRequest) (*port.HTTPResponse, error) {
	return s.resp, s.err
}

// stubLogWriter — no-op LogWriter (в тесте адаптера логирование выключено).
type stubLogWriter struct{}

func (stubLogWriter) Write(context.Context, string, *domain.LogRecord) {}
func (stubLogWriter) Flush(context.Context) error                      { return nil }

// stubNodeStatus фиксирует вызовы NodeStatusWriter. Send синхронен — мьютекс не нужен.
type stubNodeStatus struct {
	calls []struct {
		nodeID  string
		outcome domain.NodeOutcome
	}
}

// SetLastOutcome возвращает исход как есть: порог подряд идущих отказов (§52-доп)
// живёт в Redis-реализации, и подменять его здесь значило бы тестировать заглушку.
func (s *stubNodeStatus) SetLastOutcome(_ context.Context, nodeID string, outcome domain.NodeOutcome) domain.NodeOutcome {
	s.calls = append(s.calls, struct {
		nodeID  string
		outcome domain.NodeOutcome
	}{nodeID, outcome})
	return outcome
}

// TestServer_Send_NodeOutcome (§52): sync gRPC-путь классифицирует исход
// последнего вызова (ok/degraded/down) и пишет его в gauge (§41) и
// NodeStatusWriter (§46); nexus_request_incomplete_total сохраняет прежнюю
// семантику «любой не-2xx» (в т.ч. degraded 3xx/4xx).
func TestServer_Send_NodeOutcome(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		status         int32
		callErr        error
		wantOutcome    domain.NodeOutcome
		wantGauge      float64
		wantIncomplete float64
	}{
		{"200 → ok", 200, nil, domain.NodeOutcomeOK, 0, 0},
		{"302 → degraded", 302, nil, domain.NodeOutcomeDegraded, 1, 1},
		{"422 (§50.4) → degraded", 422, nil, domain.NodeOutcomeDegraded, 1, 1},
		{"500 → down", 500, nil, domain.NodeOutcomeDown, 2, 1},
		{"транспортная ошибка → down", 0, errors.New("dial tcp: refused"), domain.NodeOutcomeDown, 2, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			httpc := &stubHTTPCaller{err: tc.callErr}
			if tc.callErr == nil {
				httpc.resp = &port.HTTPResponse{StatusCode: tc.status, Body: []byte("x")}
			}
			uc := usecase.NewSendUsecase(httpc, stubLogWriter{}, nil, logging.NewNoop(), 64<<20)
			m := metrics.New("sender")
			ns := &stubNodeStatus{}
			srv := NewServer(uc, m, ns, logging.NewNoop())

			resp, err := srv.Send(context.Background(), &senderv1.SendRequest{
				Id:        "req-1",
				NodeId:    "f6693f87-0000-4000-8000-000000000001",
				NodePath:  "partner/echo",
				TeamSlug:  "partner",
				TargetUrl: "https://api.example.com/hook",
				Method:    "POST",
				TimeoutMs: 1000,
			})
			require.NoError(t, err)
			assert.Equal(t, tc.status, resp.GetStatusCode())

			gauge := testutil.ToFloat64(m.NodeLastRequestError.WithLabelValues("partner", "partner/echo"))
			assert.Equal(t, tc.wantGauge, gauge, "gauge §41/§52")

			incomplete := testutil.ToFloat64(
				m.RequestsIncompleteTotal.WithLabelValues("request", "partner", "partner/echo"))
			assert.Equal(t, tc.wantIncomplete, incomplete,
				"incomplete_total — прежняя семантика «любой не-2xx»")

			require.Len(t, ns.calls, 1, "§46: SetLastOutcome ровно раз на вызов")
			assert.Equal(t, "f6693f87-0000-4000-8000-000000000001", ns.calls[0].nodeID, "§101.6: исход пишется по id узла из запроса")
			assert.Equal(t, tc.wantOutcome, ns.calls[0].outcome)
		})
	}
}

// TestServer_Send_NoTeamSlug_NoOutcomeGauge (§101.9) — запрос без слога команды
// шлёт Receiver до §101. Счётчики пишутся с team="", а гаудж исхода — нет: после
// обновления Receiver'а ряд team="" больше не обновился бы и держал последний
// исход (DOWN и алерт NexusNodeDown) до рестарта Sender'а.
func TestServer_Send_NoTeamSlug_NoOutcomeGauge(t *testing.T) {
	t.Parallel()

	httpc := &stubHTTPCaller{resp: &port.HTTPResponse{StatusCode: 500, Body: []byte("x")}}
	uc := usecase.NewSendUsecase(httpc, stubLogWriter{}, nil, logging.NewNoop(), 64<<20)
	m := metrics.New("sender")
	ns := &stubNodeStatus{}
	srv := NewServer(uc, m, ns, logging.NewNoop())

	_, err := srv.Send(context.Background(), &senderv1.SendRequest{
		Id:        "req-1",
		NodeId:    "f6693f87-0000-4000-8000-000000000001",
		NodePath:  "partner/echo",
		TargetUrl: "https://api.example.com/hook",
		Method:    "POST",
		TimeoutMs: 1000,
	})
	require.NoError(t, err)

	assert.Zero(t, testutil.CollectAndCount(m.NodeLastRequestError), "гаудж без слога не пишется")
	assert.InDelta(t, 1, testutil.ToFloat64(
		m.RequestsTotal.WithLabelValues("request", "", "partner/echo", "500")), 0, "счётчик пишется с team=\"\"")
	require.Len(t, ns.calls, 1, "исход в Redis пишется по id узла, слог ему не нужен")
}

// §55: dry-run (тест конфига из UI) не оставляет следов на узле — ни в
// метриках, ни в гаудже исхода (§41/§52), ни в персистентном статусе (§46).
// Иначе неудачный тест покрасил бы живой узел в Down на дашборде и накрутил
// счётчики ошибок. Сам HTTP-вызов при этом настоящий.
func TestServer_Send_DryRun_NoMetricsNoNodeStatus(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		status  int32
		callErr error
	}{
		{"успешный тест", 200, nil},
		{"неудачный тест (5xx) — самый опасный случай", 500, nil},
		{"таймаут — боевой кейс §55", 0, errors.New("context deadline exceeded")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			httpc := &stubHTTPCaller{err: tc.callErr}
			if tc.callErr == nil {
				httpc.resp = &port.HTTPResponse{StatusCode: tc.status, Body: []byte("x")}
			}
			uc := usecase.NewSendUsecase(httpc, stubLogWriter{}, nil, logging.NewNoop(), 64<<20)
			m := metrics.New("sender")
			ns := &stubNodeStatus{}
			srv := NewServer(uc, m, ns, logging.NewNoop())

			resp, err := srv.Send(context.Background(), &senderv1.SendRequest{
				Id:        "dry-1",
				NodePath:  "partner/echo",
				TeamSlug:  "partner",
				TargetUrl: "https://api.example.com/hook",
				Method:    "POST",
				TimeoutMs: 1000,
				DryRun:    true,
			})
			require.NoError(t, err)
			assert.Equal(t, tc.status, resp.GetStatusCode(), "ответ настоящий — вызов выполнен")

			assert.Empty(t, ns.calls, "статус узла не персистится (§46) — узел не красится в Down")
			assert.Zero(t, testutil.CollectAndCount(m.RequestsTotal), "счётчик запросов не тронут")
			assert.Zero(t, testutil.CollectAndCount(m.RequestsIncompleteTotal), "счётчик ошибок не тронут")
			assert.Zero(t, testutil.CollectAndCount(m.RequestDuration), "гистограмма латентности не тронута")
		})
	}
}

// РЕГРЕССИЯ: боевой вызов (dry_run=false) по-прежнему пишет метрики и статус —
// гейт §55 не должен задеть основной путь.
func TestServer_Send_NormalCall_KeepsMetricsAndStatus(t *testing.T) {
	t.Parallel()

	uc := usecase.NewSendUsecase(
		&stubHTTPCaller{resp: &port.HTTPResponse{StatusCode: 500}},
		stubLogWriter{}, nil, logging.NewNoop(), 64<<20,
	)
	m := metrics.New("sender")
	ns := &stubNodeStatus{}
	srv := NewServer(uc, m, ns, logging.NewNoop())

	_, err := srv.Send(context.Background(), &senderv1.SendRequest{
		Id:        "req-1",
		NodePath:  "partner/echo",
		TargetUrl: "https://api.example.com/hook",
		Method:    "POST",
		TimeoutMs: 1000,
	})
	require.NoError(t, err)

	require.Len(t, ns.calls, 1, "боевой вызов персистит статус узла")
	assert.Equal(t, domain.NodeOutcomeDown, ns.calls[0].outcome)
	assert.NotZero(t, testutil.CollectAndCount(m.RequestsTotal), "боевые метрики пишутся")
}

// TestServer_Send_TeamSlugSplitsSameNamePaths (§101.3) — sync-вызовы двух узлов
// telegram разных команд пишут разные ряды. Слог несёт запрос: sync-путь узел из
// БД не читает. Receiver до §101 слога не пришлёт — ряд с team="", а не потеря.
func TestServer_Send_TeamSlugSplitsSameNamePaths(t *testing.T) {
	t.Parallel()
	httpc := &stubHTTPCaller{resp: &port.HTTPResponse{StatusCode: 502, Body: []byte("x")}}
	uc := usecase.NewSendUsecase(httpc, stubLogWriter{}, nil, logging.NewNoop(), 64<<20)
	m := metrics.New("sender")
	srv := NewServer(uc, m, &stubNodeStatus{}, logging.NewNoop())

	for _, team := range []string{"messengers", "messengers", "webhook", ""} {
		_, err := srv.Send(context.Background(), &senderv1.SendRequest{
			Id: "req-" + team, NodePath: "telegram", TeamSlug: team,
			TargetUrl: "https://api.example.com/hook", Method: "POST", TimeoutMs: 1000,
		})
		require.NoError(t, err)
	}

	require.Equal(t, 3, testutil.CollectAndCount(m.RequestsTotal))
	assert.InDelta(t, 2, testutil.ToFloat64(m.RequestsTotal.WithLabelValues("request", "messengers", "telegram", "502")), 0)
	assert.InDelta(t, 1, testutil.ToFloat64(m.RequestsTotal.WithLabelValues("request", "webhook", "telegram", "502")), 0)
	assert.InDelta(t, 1, testutil.ToFloat64(m.RequestsTotal.WithLabelValues("request", "", "telegram", "502")), 0)
	assert.InDelta(t, 2, testutil.ToFloat64(m.NodeLastRequestError.WithLabelValues("webhook", "telegram")), 0)
}
