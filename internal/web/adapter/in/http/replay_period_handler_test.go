package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
	"nexus/internal/web/usecase/port"
)

// rpLogReader — LogReader, запоминающий запрос, с которым его позвали: тест
// проверяет, что фильтр журнала доехал из query-параметров без потерь.
type rpLogReader struct {
	port.LogReader
	gotQ  port.LogQuery
	items []domain.ReplayCandidate
}

func (r *rpLogReader) ReplayCandidates(
	_ context.Context, q port.LogQuery, _ port.ReplayCursor, _ int,
) ([]domain.ReplayCandidate, port.ReplayCursor, error) {
	r.gotQ = q
	return r.items, port.ReplayCursor{}, nil
}

func (r *rpLogReader) Count(_ context.Context, q port.LogQuery) (uint64, error) {
	r.gotQ = q
	return uint64(len(r.items)), nil
}

func (r *rpLogReader) GetByID(_ context.Context, _, id string) (*domain.LogRecord, error) {
	return &domain.LogRecord{
		ID: id, Type: domain.RootMethodRequestAsync,
		Request: `{"ok":1}`, DateRequest: time.Now(), Done: true,
	}, nil
}

// rpDispatcher — считает реинжекции.
type rpDispatcher struct{ calls int }

func (d *rpDispatcher) Dispatch(_ context.Context, _ port.DispatchRequest) (*port.DispatchResponse, error) {
	d.calls++
	return &port.DispatchResponse{StatusCode: 200, Body: []byte(`{"ok":true}`)}, nil
}

func rpEngine(node *domain.Node, reader port.LogReader, disp port.ReceiverDispatcher) *gin.Engine {
	gin.SetMode(gin.TestMode)
	uc := usecase.NewReplayUsecase(reader, &aqNodeRepo{node: node}, disp, nil,
		usecase.NewAuditUsecase(aqAuditRepo{}, logging.NewNoop()), 10, logging.NewNoop())
	h := NewReplayHandler(uc, logging.NewNoop())

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(ctxSessionKey, &domain.Session{UserID: "u1", Role: domain.UserRoleAdmin, CurrentTeamID: "t1"})
		c.Next()
	})
	r.POST("/api/nodes/:id/logs/replay-period/plan", h.PlanPeriod)
	r.POST("/api/nodes/:id/logs/replay-period/run", h.RunPeriod)
	return r
}

func rpNode() *domain.Node {
	return &domain.Node{
		ID: "n1", TeamID: "t1", Path: "demo/async",
		Status: domain.NodeStatusEnabled, RootMethod: domain.RootMethodRequestAsync,
		IncomingMethod: domain.HTTPMethodPOST, ClickHouseTable: "test.demo",
		LoggingEnabled: true, LogRequestBody: true,
	}
}

func rpCandidate(id string) domain.ReplayCandidate {
	return domain.ReplayCandidate{
		ID: id, DateRequest: time.Now().Add(-time.Hour),
		Type: domain.RootMethodRequestAsync, HTTPMethod: "POST",
		BodyHead: `{"ok":1}`, StoredBytes: 8, RequestSize: 8,
	}
}

const rpWindow = "?from=2026-08-01T00%3A00%3A00Z&to=2026-08-02T00%3A00%3A00Z"

// TestReplayPeriod_FilterComesFromQueryParams — фильтр журнала приходит теми же
// query-параметрами, что у GET /logs, и доезжает до запроса без потерь. Второй
// парсер в теле означал бы, что предпросмотр показывает одно множество, а
// отправка берёт другое.
func TestReplayPeriod_FilterComesFromQueryParams(t *testing.T) {
	reader := &rpLogReader{items: []domain.ReplayCandidate{rpCandidate("a")}}
	r := rpEngine(rpNode(), reader, &rpDispatcher{})

	req := httptest.NewRequest(http.MethodPost,
		"/api/nodes/n1/logs/replay-period/plan"+rpWindow+"&method=v1%2FGet&status=ok&q=hello", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, "v1/Get", reader.gotQ.Method)
	assert.Equal(t, "ok", reader.gotQ.Status)
	assert.NotNil(t, reader.gotQ.QExpr, "полнотекстовый запрос обязан быть разобран, иначе q молча не действует")
	assert.Equal(t, "test.demo", reader.gotQ.Table)
	assert.Equal(t, "n1", reader.gotQ.NodeID)
}

// TestReplayPeriod_PlanDoesNotDispatch — предпросмотр не отправляет ничего.
func TestReplayPeriod_PlanDoesNotDispatch(t *testing.T) {
	disp := &rpDispatcher{}
	r := rpEngine(rpNode(), &rpLogReader{items: []domain.ReplayCandidate{rpCandidate("a")}}, disp)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/nodes/n1/logs/replay-period/plan"+rpWindow, nil))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Zero(t, disp.calls, "предпросмотр обязан быть read-only")

	var plan usecase.ReplayPeriodPlan
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &plan))
	assert.Equal(t, 1, plan.Eligible)
	assert.True(t, plan.Exact)
	// Границы возвращаются клиенту, чтобы он слал их обратно в каждый батч.
	assert.False(t, plan.From.IsZero())
	assert.False(t, plan.To.IsZero())
}

// TestReplayPeriod_RunDispatches — батч реально отправляет и отдаёт счётчики.
func TestReplayPeriod_RunDispatches(t *testing.T) {
	disp := &rpDispatcher{}
	r := rpEngine(rpNode(), &rpLogReader{items: []domain.ReplayCandidate{rpCandidate("a"), rpCandidate("b")}}, disp)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost,
		"/api/nodes/n1/logs/replay-period/run"+rpWindow, strings.NewReader(`{"limit":50}`)))

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, 2, disp.calls)

	var batch usecase.ReplayPeriodBatch
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &batch))
	assert.Equal(t, 2, batch.Replayed)
	assert.Nil(t, batch.NextCursor, "окно пройдено — курсора нет")
}

// TestReplayPeriod_ErrorMapping — отказы §85 отдаются понятными кодами, а не 500.
func TestReplayPeriod_ErrorMapping(t *testing.T) {
	tests := []struct {
		name   string
		node   func() *domain.Node
		url    string
		status int
	}{
		{
			name:   "без границ окна",
			node:   rpNode,
			url:    "/api/nodes/n1/logs/replay-period/run",
			status: http.StatusBadRequest,
		},
		{
			name: "синхронный узел",
			node: func() *domain.Node {
				n := rpNode()
				n.RootMethod = domain.RootMethodRequest
				return n
			},
			url:    "/api/nodes/n1/logs/replay-period/run" + rpWindow,
			status: http.StatusConflict,
		},
		{
			name: "тело не логируется",
			node: func() *domain.Node {
				n := rpNode()
				n.LogRequestBody = false
				return n
			},
			url:    "/api/nodes/n1/logs/replay-period/run" + rpWindow,
			status: http.StatusUnprocessableEntity,
		},
		{
			name: "узел отключён",
			node: func() *domain.Node {
				n := rpNode()
				n.Status = domain.NodeStatusDisabled
				return n
			},
			url:    "/api/nodes/n1/logs/replay-period/run" + rpWindow,
			status: http.StatusConflict,
		},
		{
			name:   "битый поисковый запрос",
			node:   rpNode,
			url:    "/api/nodes/n1/logs/replay-period/run" + rpWindow + "&q=%5B&q_regex=1",
			status: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			disp := &rpDispatcher{}
			r := rpEngine(tt.node(), &rpLogReader{}, disp)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, tt.url, nil))
			assert.Equal(t, tt.status, w.Code, w.Body.String())
			assert.Zero(t, disp.calls, "при отказе гейта отправки быть не должно")
		})
	}
}
