package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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

// brBreaker — управляемый port.NodeBreaker для handler-тестов.
type brBreaker struct {
	state    port.BreakerState
	stateErr error
	resetErr error
}

func (b *brBreaker) State(_ context.Context, _ string) (port.BreakerState, error) {
	return b.state, b.stateErr
}
func (b *brBreaker) Reset(_ context.Context, _ string) (port.BreakerState, error) {
	return b.state, b.resetErr
}

type brStatusResetter struct{ err error }

func (s brStatusResetter) ClearLastOutcome(_ context.Context, _ string) error { return s.err }

// brEngine собирает движок с сессией (admin, team t1) и роутами breaker'а.
// breaker=nil воспроизводит инсталляцию без Redis.
func brEngine(node *domain.Node, breaker port.NodeBreaker) *gin.Engine {
	gin.SetMode(gin.TestMode)
	var status port.NodeStatusResetter
	if breaker != nil {
		status = brStatusResetter{}
	}
	uc := usecase.NewNodeBreakerUsecase(breaker, status, &aqNodeRepo{node: node},
		usecase.NewAuditUsecase(aqAuditRepo{}, logging.NewNoop()), logging.NewNoop())
	h := NewBreakerHandler(uc, logging.NewNoop())

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(ctxSessionKey, &domain.Session{UserID: "u1", Role: domain.UserRoleAdmin, CurrentTeamID: "t1"})
		c.Next()
	})
	r.GET("/api/nodes/:id/breaker", h.State)
	r.POST("/api/nodes/:id/breaker/reset", h.Reset)
	return r
}

func brNode() *domain.Node {
	return &domain.Node{ID: "n1", Path: "partner/echo", TeamID: "t1", Status: domain.NodeStatusEnabled}
}

func TestBreakerHandler_State_Open(t *testing.T) {
	t.Parallel()

	br := &brBreaker{state: port.BreakerState{
		Exists: true, State: domain.BreakerStateOpen, Failures: 5, Threshold: 5,
		OpenedAt: time.Now().Add(-5 * time.Second), RetryAfter: 25 * time.Second,
	}}
	w := httptest.NewRecorder()
	brEngine(brNode(), br).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/nodes/n1/breaker", nil))

	require.Equal(t, http.StatusOK, w.Code)
	var got breakerStateDTO
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.True(t, got.Available)
	assert.Equal(t, domain.BreakerStateOpen, got.State)
	assert.Equal(t, 5, got.Failures)
	assert.Equal(t, 5, got.Threshold)
	assert.Equal(t, int64(25000), got.RetryAfterMs, "обратный отсчёт уходит в миллисекундах")
}

// Без Redis чтение отвечает 200 с available=false: защиты в инсталляции нет
// вовсе, и это честный ответ, а не ошибка.
func TestBreakerHandler_State_WithoutRedis_Is200(t *testing.T) {
	t.Parallel()

	w := httptest.NewRecorder()
	brEngine(brNode(), nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/nodes/n1/breaker", nil))

	require.Equal(t, http.StatusOK, w.Code)
	var got breakerStateDTO
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.False(t, got.Available)
}

// А вот сбрасывать нечего — это 503 с внятной причиной.
func TestBreakerHandler_Reset_WithoutRedis_Is503(t *testing.T) {
	t.Parallel()

	w := httptest.NewRecorder()
	brEngine(brNode(), nil).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/nodes/n1/breaker/reset", nil))

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestBreakerHandler_State_RedisError_Is503(t *testing.T) {
	t.Parallel()

	br := &brBreaker{stateErr: errors.New("redis down")}
	w := httptest.NewRecorder()
	brEngine(brNode(), br).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/nodes/n1/breaker", nil))

	assert.Equal(t, http.StatusServiceUnavailable, w.Code, "неизвестное состояние не выдаём за «закрыт»")
}

func TestBreakerHandler_Reset_Ok(t *testing.T) {
	t.Parallel()

	br := &brBreaker{state: port.BreakerState{Exists: true, State: domain.BreakerStateOpen, Failures: 5}}
	w := httptest.NewRecorder()
	brEngine(brNode(), br).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/nodes/n1/breaker/reset", nil))

	require.Equal(t, http.StatusOK, w.Code)
	var got breakerResetDTO
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.True(t, got.WasOpen)
	assert.Equal(t, domain.BreakerStateOpen, got.PreviousState)
	assert.Equal(t, 5, got.PreviousFailures)
	assert.True(t, got.NodeStatusCleared)
}

func TestBreakerHandler_ForeignTeam_Is404(t *testing.T) {
	t.Parallel()

	node := brNode()
	node.TeamID = "other-team"
	eng := brEngine(node, &brBreaker{})

	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/nodes/n1/breaker", nil),
		httptest.NewRequest(http.MethodPost, "/api/nodes/n1/breaker/reset", nil),
	} {
		w := httptest.NewRecorder()
		eng.ServeHTTP(w, req)
		assert.Equal(t, http.StatusNotFound, w.Code, "%s", req.URL.Path)
	}
}

// §81.4.1/§87: состояние видно всем ролям, сброс — operator+. Гейт роли живёт
// в маршрутизации, поэтому тест собирает те же группы, что routes.go.
func TestBreakerRoutes_ViewerSeesStateButCannotReset(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	br := &brBreaker{state: port.BreakerState{Exists: true, State: domain.BreakerStateOpen}}
	uc := usecase.NewNodeBreakerUsecase(br, brStatusResetter{}, &aqNodeRepo{node: brNode()},
		usecase.NewAuditUsecase(aqAuditRepo{}, logging.NewNoop()), logging.NewNoop())
	h := NewBreakerHandler(uc, logging.NewNoop())

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(ctxSessionKey, &domain.Session{UserID: "u1", Role: domain.UserRoleViewer, CurrentTeamID: "t1"})
		c.Next()
	})
	authed := r.Group("/api")
	authedOperator := authed.Group("/", RequireMinRole(domain.UserRoleOperator))
	authed.GET("/nodes/:id/breaker", h.State)
	authedOperator.POST("/nodes/:id/breaker/reset", h.Reset)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/nodes/n1/breaker", nil))
	assert.Equal(t, http.StatusOK, w.Code, "наблюдатель видит, почему узел молчит")

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/nodes/n1/breaker/reset", nil))
	assert.Equal(t, http.StatusForbidden, w.Code, "но снять защиту не может")
}
