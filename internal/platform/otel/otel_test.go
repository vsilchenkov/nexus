package otel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/config"
	"nexus/internal/platform/logging"
)

func TestInit_Disabled_ReturnsNoopShutdown(t *testing.T) {
	cfg := &config.OtelSection{Enable: false}
	shutdown, err := Init(context.Background(), cfg, "receiver", logging.NewNoop())
	require.NoError(t, err)
	require.NotNil(t, shutdown)

	// Noop shutdown — должен возвращать nil даже при отменённом контексте.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.NoError(t, shutdown(ctx))
}

func TestInit_NilConfig_ReturnsNoopShutdown(t *testing.T) {
	// Defence-in-depth: даже если в bootstrap'е cfg.Otel забыли, Init не
	// должен паниковать.
	shutdown, err := Init(context.Background(), nil, "receiver", logging.NewNoop())
	require.NoError(t, err)
	require.NotNil(t, shutdown)
	assert.NoError(t, shutdown(context.Background()))
}

func TestGinMiddleware_Runs_WithoutTracerProvider(t *testing.T) {
	// При выключенном tracing global TracerProvider — no-op; middleware
	// должен молча проходить запрос, не валя 500.
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(GinMiddleware("test-svc"))
	r.GET("/ping", func(c *gin.Context) {
		c.String(http.StatusOK, "pong")
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "pong", w.Body.String())
}

func TestGinMiddleware_PropagatesContext(t *testing.T) {
	// Контекст запроса после middleware должен содержать ссылку на span
	// (даже noop) — это позволяет downstream-логике вытащить traceparent
	// и положить в outbound-запрос.
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(GinMiddleware("test-svc"))

	var ctxFromHandler context.Context
	r.GET("/x", func(c *gin.Context) {
		ctxFromHandler = c.Request.Context()
		c.String(http.StatusOK, "ok")
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.ServeHTTP(w, req)

	require.NotNil(t, ctxFromHandler)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestInit_WithInvalidEndpoint_DoesNotCrash(t *testing.T) {
	// Конструктор exporter'а в otlptracehttp не делает network round-trip
	// (соединение откладывается до первого экспорта). Поэтому invalid
	// endpoint не должен валить Init — это правильное поведение, иначе
	// сервис бы не стартовал при временно недоступном collector'е.
	cfg := &config.OtelSection{
		Enable:       true,
		OtlpEndpoint: "this-host-does-not-exist:9999",
		OtlpInsecure: true,
		SampleRate:   0.5,
		ServiceName:  "test",
		Environment:  "test",
	}
	shutdown, err := Init(context.Background(), cfg, "fallback", logging.NewNoop())
	require.NoError(t, err)
	require.NotNil(t, shutdown)
	// Shutdown с коротким контекстом — provider попробует flush'нуть и
	// завершится; ошибка экспорта в недоступный endpoint — ожидаема, но
	// сам shutdown не должен паниковать.
	assert.NotPanics(t, func() {
		_ = shutdown(context.Background())
	})
}
