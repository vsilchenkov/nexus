package healthcheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingChecker считает обращения — нужен, чтобы доказать, что дренаж
// отвечает БЕЗ опроса зависимостей.
type countingChecker struct {
	calls atomic.Int64
}

func (c *countingChecker) Name() string { return "counted" }

func (c *countingChecker) Check(context.Context) error {
	c.calls.Add(1)
	return nil
}

// TestReadyDraining (§93.5): после StartDraining readiness отвечает 503 с
// признаком draining, а liveness остаётся 200.
//
// Разделение принципиально: по /health docker решает, не убить ли контейнер, и
// 503 там означал бы, что штатная остановка выглядит как отказ процесса —
// контейнер прибили бы вместе с недоигранными запросами.
func TestReadyDraining(t *testing.T) {
	t.Parallel()

	h := New(nil, nil)
	r := gin.New()
	h.Register(r)

	// До дренажа — обычный ready.
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ready", nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.False(t, h.Draining())

	h.StartDraining()
	assert.True(t, h.Draining())

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ready", nil))
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), `"draining":true`)
	assert.Contains(t, w.Body.String(), `"ready":false`)

	// Liveness не меняется.
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))
	assert.Equal(t, http.StatusOK, w.Code)
}

// TestReadyDrainingSkipsChecks (§93.5): в режиме дренажа зависимости не
// опрашиваются.
//
// Это не оптимизация ради оптимизации: на остановке PostgreSQL и Redis могут
// уже закрываться, и опрос дал бы либо задержку на таймаут, либо ошибку в
// логах на ровном месте. Ответ балансировщику нужен немедленный и один и тот
// же независимо от состояния зависимостей.
func TestReadyDrainingSkipsChecks(t *testing.T) {
	t.Parallel()

	counter := &countingChecker{}
	h := New([]Checker{counter}, nil)
	r := gin.New()
	h.Register(r)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ready", nil))
	require.Equal(t, int64(1), counter.calls.Load(), "до дренажа зависимость опрашивается")

	h.StartDraining()
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ready", nil))

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Equal(t, int64(1), counter.calls.Load(), "в дренаже зависимости опрашиваться не должны")
}

// TestDrainWaitsPause (§93.5): Drain выдерживает паузу — за неё балансировщик
// успевает заметить 503 и перестать слать новые запросы.
func TestDrainWaitsPause(t *testing.T) {
	t.Parallel()

	h := New(nil, nil)
	start := time.Now()
	Drain(context.Background(), h, 60*time.Millisecond)

	assert.True(t, h.Draining())
	assert.GreaterOrEqual(t, time.Since(start), 60*time.Millisecond)
}

// TestDrainStopsOnContext (§93.5): отмена контекста прерывает паузу.
// Иначе жёсткий дедлайн остановки (docker stop -t) уходил бы целиком в сон.
func TestDrainStopsOnContext(t *testing.T) {
	t.Parallel()

	h := New(nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	Drain(ctx, h, 5*time.Second)

	assert.True(t, h.Draining(), "флаг взводится до ожидания, а не после")
	assert.Less(t, time.Since(start), time.Second)
}

// TestDrainNoPause (§93.5): без паузы (одиночная установка) Drain только
// помечает сервис и не задерживает остановку.
func TestDrainNoPause(t *testing.T) {
	t.Parallel()

	h := New(nil, nil)
	start := time.Now()
	Drain(context.Background(), h, 0)

	assert.True(t, h.Draining())
	assert.Less(t, time.Since(start), 50*time.Millisecond)
}

// TestDrainNilHandler (§93.5): nil-handler не роняет остановку.
func TestDrainNilHandler(t *testing.T) {
	t.Parallel()

	assert.NotPanics(t, func() { Drain(context.Background(), nil, time.Millisecond) })
}
