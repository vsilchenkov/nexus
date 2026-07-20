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

// --- минимальные стабы для построения AsyncQueueUsecase в http-тесте ---

type aqNodeRepo struct {
	node *domain.Node
	err  error
}

func (r *aqNodeRepo) Get(_ context.Context, _ string) (*domain.Node, error) { return r.node, r.err }
func (r *aqNodeRepo) GetByPath(_ context.Context, _ string) (*domain.Node, error) {
	return nil, domain.ErrNodeNotFound
}
func (r *aqNodeRepo) List(_ context.Context, _ port.ListNodesFilter) ([]*domain.Node, error) {
	return nil, nil
}
func (r *aqNodeRepo) Count(_ context.Context, _ string) (int, error) { return 0, nil }
func (r *aqNodeRepo) Create(_ context.Context, _ *domain.Node) error { return nil }
func (r *aqNodeRepo) Update(_ context.Context, _ *domain.Node) error { return nil }
func (r *aqNodeRepo) Delete(_ context.Context, _ string) error       { return nil }
func (r *aqNodeRepo) UpdateAllowedHostsSnapshot(_ context.Context, _ string, _ []string) error {
	return nil
}

type aqAuditRepo struct{}

func (aqAuditRepo) Write(_ context.Context, _ *domain.AuditEntry) error { return nil }
func (aqAuditRepo) List(_ context.Context, _ port.AuditFilter) ([]*domain.AuditEntry, error) {
	return nil, nil
}
func (aqAuditRepo) DeleteOlderThan(_ context.Context, _ time.Time) (int, error) { return 0, nil }

// aqEngine собирает движок с сессией (admin, team t1) и зарегистрированными
// async-queue роутами. peeker/cancel = nil → деградация/unavailable.
func aqEngine(node *domain.Node, nodeErr error) *gin.Engine {
	gin.SetMode(gin.TestMode)
	uc := usecase.NewAsyncQueueUsecase(nil, nil, nil, &aqNodeRepo{node: node, err: nodeErr},
		usecase.NewAuditUsecase(aqAuditRepo{}, logging.NewNoop()),
		"nexus-sender", "nexus.async",
		"nexus-sender-paused", "nexus.async.paused",
		time.Hour, 0, logging.NewNoop())
	h := NewAsyncQueueHandler(uc, logging.NewNoop())

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(ctxSessionKey, &domain.Session{UserID: "u1", Role: domain.UserRoleAdmin, CurrentTeamID: "t1"})
		c.Next()
	})
	g := r.Group("/api/nodes/:id/async-queue")
	g.GET("/messages", h.List)
	g.GET("/messages/body", h.Body)
	g.DELETE("/messages/:msgId", h.DeleteOne)
	g.POST("/purge", h.Purge)
	g.POST("/purge-failed", h.PurgeFailed)
	return r
}

func aqNode() *domain.Node {
	return &domain.Node{ID: "n1", Path: "partner/echo", TeamID: "t1", Status: domain.NodeStatusEnabled}
}

func TestAQHandler_List_DegradedWhenNoKafka(t *testing.T) {
	t.Parallel()
	r := aqEngine(aqNode(), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/nodes/n1/async-queue/messages", nil))
	require.Equal(t, http.StatusOK, w.Code)
	var resp queueListDTO
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.False(t, resp.KafkaAvailable, "без Kafka — деградация, не ошибка")
}

func TestAQHandler_NodeNotFound_404(t *testing.T) {
	t.Parallel()
	r := aqEngine(nil, domain.ErrNodeNotFound)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/nodes/zzz/async-queue/depth", nil))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestAQHandler_Body_BadParams_400(t *testing.T) {
	t.Parallel()
	r := aqEngine(aqNode(), nil)

	// offset отсутствует.
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/nodes/n1/async-queue/messages/body?partition=0", nil))
	assert.Equal(t, http.StatusBadRequest, w.Code)

	// partition отсутствует.
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/nodes/n1/async-queue/messages/body?offset=5", nil))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestAQHandler_Body_UnavailableWhenNoKafka_503(t *testing.T) {
	t.Parallel()
	r := aqEngine(aqNode(), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/nodes/n1/async-queue/messages/body?partition=0&offset=5", nil))
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestAQHandler_DeleteOne_UnavailableWhenNoRedis_503(t *testing.T) {
	t.Parallel()
	r := aqEngine(aqNode(), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/nodes/n1/async-queue/messages/m1", nil))
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestAQHandler_Purge_FromAfterTo_400(t *testing.T) {
	t.Parallel()
	r := aqEngine(aqNode(), nil)
	body := `{"from":"2026-06-18T12:00:00Z","to":"2026-06-18T10:00:00Z"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/nodes/n1/async-queue/purge", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}
