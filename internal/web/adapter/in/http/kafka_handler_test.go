package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
	"nexus/internal/web/usecase/port"
)

// fakeLimiter — подменяет *ratelimit.Limiter в тестах (без Redis).
// Записывает последний ключ/лимит и возвращает заранее заданный вердикт.
type fakeLimiter struct {
	allow   bool
	err     error
	lastKey string
	lastLim int
	calls   int
}

func (f *fakeLimiter) Allow(_ context.Context, key string, limitPerMin int) (bool, error) {
	f.calls++
	f.lastKey = key
	f.lastLim = limitPerMin
	return f.allow, f.err
}

// withKafkaRL собирает движок: опционально кладёт session в контекст, затем
// rate-limit middleware и заглушку-handler (200).
func withKafkaRL(limiter rateLimiter, limit int, session *domain.Session) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	if session != nil {
		r.Use(func(c *gin.Context) {
			c.Set(ctxSessionKey, session)
			c.Next()
		})
	}
	r.Use(KafkaRateLimitMiddleware(limiter, limit))
	r.GET("/api/kafka/overview", func(c *gin.Context) { c.Status(http.StatusOK) })
	return r
}

func TestKafkaRateLimit_AllowsUnderLimit(t *testing.T) {
	t.Parallel()
	lim := &fakeLimiter{allow: true}
	r := withKafkaRL(lim, 60, &domain.Session{UserID: "u1", Role: domain.UserRoleAdmin})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/kafka/overview", nil))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "kafka:u1", lim.lastKey)
	assert.Equal(t, 60, lim.lastLim)
	assert.Equal(t, 1, lim.calls)
}

func TestKafkaRateLimit_BlocksOverLimit(t *testing.T) {
	t.Parallel()
	lim := &fakeLimiter{allow: false}
	r := withKafkaRL(lim, 60, &domain.Session{UserID: "u1", Role: domain.UserRoleAdmin})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/kafka/overview", nil))

	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	// localizedError тело: {"error": "..."} по ключу kafka.rate_limited.
	assert.Contains(t, w.Body.String(), "error")
}

func TestKafkaRateLimit_KeyPerUser(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		session *domain.Session
		wantKey string
	}{
		{"with session → per-user key", &domain.Session{UserID: "alice"}, "kafka:alice"},
		{"no session → anon key", nil, "kafka:anon"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			lim := &fakeLimiter{allow: true}
			r := withKafkaRL(lim, 60, c.session)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("GET", "/api/kafka/overview", nil))
			assert.Equal(t, http.StatusOK, w.Code)
			assert.Equal(t, c.wantKey, lim.lastKey)
		})
	}
}

func TestKafkaRateLimit_FailOpenOnRedisError(t *testing.T) {
	t.Parallel()
	// Лимитер сам fail-open'ит (возвращает allow=true при ошибке Redis); проверяем,
	// что middleware пропускает запрос, когда Allow вернул true несмотря на err.
	lim := &fakeLimiter{allow: true, err: assert.AnError}
	r := withKafkaRL(lim, 60, &domain.Session{UserID: "u1"})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/kafka/overview", nil))
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestKafkaRateLimit_DisabledWhenZero(t *testing.T) {
	t.Parallel()
	// limit=0 → Limiter.Allow вернёт true без обращения к Redis; middleware
	// пропускает. Фейк это эмулирует, проверяем сквозной проход.
	lim := &fakeLimiter{allow: true}
	r := withKafkaRL(lim, 0, &domain.Session{UserID: "u1"})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/kafka/overview", nil))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, 0, lim.lastLim)
}

// stubGroupLag / stubPausedNodes — источники §113.7 для проверки формы ответа.
type stubGroupLag map[string]int64

func (s stubGroupLag) GroupLag(_ context.Context, group, _ string) (int64, error) {
	return s[group], nil
}

// failingGroupLag — брокер не отвечает на запрос офсетов (§114.5).
type failingGroupLag struct{}

func (failingGroupLag) GroupLag(context.Context, string, string) (int64, error) {
	return 0, errors.New("dial tcp: connection refused")
}

type stubPausedNodes []port.NodeKeyRef

func (s stubPausedNodes) PausedNodes(context.Context) ([]port.NodeKeyRef, error) { return s, nil }

func kafkaOverviewJSON(t *testing.T, uc *usecase.KafkaMonitorUsecase) map[string]any {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/kafka/overview", NewKafkaHandler(uc, logging.NewNoop()).Overview)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/kafka/overview?range=1h", nil))
	require.Equal(t, http.StatusOK, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	return body
}

// TestKafkaOverview_PausedBacklogJSON (§113.7): очередь паузы — в
// summary.paused_backlog, узлы — массивом со ссылочными полями.
func TestKafkaOverview_PausedBacklogJSON(t *testing.T) {
	t.Parallel()
	groups := usecase.KafkaGroupNames{Main: "g", Paused: "g-paused", MainTopic: "a", PausedTopic: "a.paused"}
	uc := usecase.NewKafkaMonitorUsecase(nil, nil, nil, nil, usecase.KafkaThresholds{}, logging.NewNoop(),
		usecase.WithKafkaGroups(groups),
		usecase.WithKafkaGroupLag(stubGroupLag{"g": 4, "g-paused": 960}),
		usecase.WithPausedNodes(stubPausedNodes{{ID: "n1", Key: port.NodeKey{Team: "edo", Path: "sbis"}}}))

	body := kafkaOverviewJSON(t, uc)

	summary := body["summary"].(map[string]any)
	assert.EqualValues(t, 4, summary["current_lag"])
	assert.EqualValues(t, 960, summary["paused_backlog"])
	assert.Equal(t, true, body["lag_from_broker"])
	assert.Equal(t, true, body["paused_backlog_available"])
	assert.Equal(t, true, body["paused_nodes_available"])
	assert.EqualValues(t, 1, body["paused_nodes_total"])
	assert.Equal(t, []any{map[string]any{"id": "n1", "team": "edo", "path": "sbis"}}, body["paused_nodes"])
}

// TestKafkaOverview_BrokerUnreachableJSON (§114.5): брокер не ответил —
// очередь паузы помечена непрочитанной, а баннер называет причину.
func TestKafkaOverview_BrokerUnreachableJSON(t *testing.T) {
	t.Parallel()
	groups := usecase.KafkaGroupNames{Main: "g", Paused: "g-paused", MainTopic: "a", PausedTopic: "a.paused"}
	uc := usecase.NewKafkaMonitorUsecase(nil, nil, nil, nil, usecase.KafkaThresholds{}, logging.NewNoop(),
		usecase.WithKafkaGroups(groups), usecase.WithKafkaGroupLag(failingGroupLag{}))

	body := kafkaOverviewJSON(t, uc)

	assert.Equal(t, false, body["paused_backlog_available"])
	assert.Equal(t, false, body["kafka_available"])
	assert.Equal(t, false, body["lag_from_broker"])
	assert.Equal(t, map[string]any{"severity": "warn", "reason": "broker_unreachable"}, body["health"])
}

// stubTopicsAdmin — KafkaAdmin с готовым списком топиков (§114.6).
type stubTopicsAdmin []port.TopicInfo

func (s stubTopicsAdmin) Topics(context.Context) ([]port.TopicInfo, error) { return s, nil }
func (stubTopicsAdmin) BrokerHealth(context.Context) (port.BrokerHealth, error) {
	return port.BrokerHealth{}, nil
}
func (stubTopicsAdmin) Ping(context.Context) ([]port.BrokerPing, error) { return nil, nil }

// TestKafkaTopics_UncommittedPartitionsJSON (§114.6): uncommitted_partitions
// есть только у посторонней группы; служебного lag_uncommitted в ответе нет.
func TestKafkaTopics_UncommittedPartitionsJSON(t *testing.T) {
	t.Parallel()
	admin := stubTopicsAdmin{{Name: "nexus.async", ConsumerGroups: []port.TopicConsumerGroup{
		{Group: "g", LagTotal: 10, LagUncommitted: 4, UncommittedPartitions: 1},
		{Group: "debug-tool", LagTotal: 15, LagUncommitted: 3, UncommittedPartitions: 3},
	}}}
	uc := usecase.NewKafkaMonitorUsecase(nil, admin, nil, nil, usecase.KafkaThresholds{}, logging.NewNoop(),
		usecase.WithKafkaGroups(usecase.KafkaGroupNames{Main: "g", MainTopic: "nexus.async"}))
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/kafka/topics", NewKafkaHandler(uc, logging.NewNoop()).Topics)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/kafka/topics", nil))
	require.Equal(t, http.StatusOK, w.Code)

	var body struct {
		Topics []struct {
			ConsumerGroups []map[string]any `json:"consumer_groups"`
		} `json:"topics"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Len(t, body.Topics, 1)
	assert.Equal(t, []map[string]any{
		{"group": "g", "lag_total": 10.0, "members": 0.0, "role": "main"},
		{"group": "debug-tool", "lag_total": 12.0, "members": 0.0, "uncommitted_partitions": 3.0},
	}, body.Topics[0].ConsumerGroups)
}

// TestKafkaOverview_PausedNodesNeverNull: без очереди паузы paused_nodes —
// пустой массив, а не null.
func TestKafkaOverview_PausedNodesNeverNull(t *testing.T) {
	t.Parallel()
	uc := usecase.NewKafkaMonitorUsecase(nil, nil, nil, nil, usecase.KafkaThresholds{}, logging.NewNoop())
	body := kafkaOverviewJSON(t, uc)
	assert.Equal(t, []any{}, body["paused_nodes"])
	assert.Equal(t, false, body["lag_from_broker"])
}
