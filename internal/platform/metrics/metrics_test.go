package metrics_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bus/internal/platform/metrics"
)

func TestNew_RegistersAllRequiredMetrics(t *testing.T) {
	t.Parallel()
	m := metrics.New("receiver")

	// CounterVec/HistogramVec не печатают строки без хотя бы одной серии —
	// поэтому сначала «прикасаемся» к каждому ряду §6, потом проверяем дамп.
	m.RequestsTotal.WithLabelValues("request", "demo", "200").Inc()
	m.RequestDuration.WithLabelValues("request", "demo").Observe(0.01)
	m.KafkaLag.WithLabelValues("databus.async", "0", "sender").Set(0)
	m.CHBufferSize.WithLabelValues("databus.logs").Set(0)
	m.CHErrorsTotal.WithLabelValues("databus.logs", "insert").Add(0)

	rr := httptest.NewRecorder()
	m.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, rr.Code)
	body := rr.Body.String()

	for _, want := range []string{
		"databus_requests_total",
		"databus_request_duration_seconds",
		"databus_kafka_lag",
		"databus_clickhouse_buffer_size",
		"databus_clickhouse_errors_total",
		// Standard runtime/process collectors registered into our isolated registry.
		"go_goroutines",
		"process_cpu_seconds_total",
	} {
		assert.Contains(t, body, want, "expected %q in /metrics output", want)
	}
}

func TestNew_ServiceLabelIsolated(t *testing.T) {
	t.Parallel()
	// Two independent registries — incrementing one must not leak into another.
	a := metrics.New("receiver")
	b := metrics.New("sender")

	a.RequestsTotal.WithLabelValues("request", "demo", "200").Inc()
	b.RequestsTotal.WithLabelValues("request", "demo", "200").Add(3)

	require.Equal(t, `service="receiver"`, findServiceLabel(t, a.Handler()))
	require.Equal(t, `service="sender"`, findServiceLabel(t, b.Handler()))
}

func TestGinMiddleware_RecordsRequestsAndDuration(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	m := metrics.New("receiver")

	r := gin.New()
	r.Use(metrics.GinMiddleware(m))
	r.Any("/v1/request/*path", func(c *gin.Context) {
		// Имитируем работу — observe в гистограмме должен быть > 0.
		time.Sleep(2 * time.Millisecond)
		c.Status(http.StatusOK)
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/request/partner/orders", nil)
	r.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	dump := dumpMetrics(t, m.Handler())

	// requests_total{method="request",node="partner/orders",status="200"} == 1.
	requireSample(t, dump,
		`databus_requests_total{method="request",node="partner/orders",service="receiver",status="200"} 1`)

	// duration: количество наблюдений ровно 1.
	requireSample(t, dump,
		`databus_request_duration_seconds_count{method="request",node="partner/orders",service="receiver"} 1`)
}

func TestGinMiddleware_SkipsHealthAndMetrics(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	m := metrics.New("web")

	r := gin.New()
	r.Use(metrics.GinMiddleware(m))
	r.GET("/health", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.GET("/metrics", func(c *gin.Context) { c.Status(http.StatusOK) })

	for _, p := range []string{"/health", "/metrics"} {
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, p, nil))
		require.Equal(t, http.StatusOK, rr.Code, p)
	}

	dump := dumpMetrics(t, m.Handler())
	// На /health и /metrics не должно быть ни одной серии databus_requests_total.
	if strings.Contains(dump, "databus_requests_total{") {
		// Допустим только строки с другим method (не /health, не /metrics).
		// Поскольку других маршрутов в тесте нет — серий быть не должно.
		t.Fatalf("/metrics and /health must not produce request series; got:\n%s", dump)
	}
}

func TestGinMiddleware_NonV1Path_UsesFullRoute(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	m := metrics.New("web")

	r := gin.New()
	r.Use(metrics.GinMiddleware(m))
	r.GET("/api/nodes/:id", func(c *gin.Context) { c.Status(http.StatusNotFound) })

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/nodes/abc-123", nil))
	require.Equal(t, http.StatusNotFound, rr.Code)

	dump := dumpMetrics(t, m.Handler())
	// node = "" (нет path-параметра /v1), method = "GET /api/nodes/:id" (template, не URL).
	requireSample(t, dump,
		`databus_requests_total{method="GET /api/nodes/:id",node="",service="web",status="404"} 1`)
}

// ---- helpers ----

func dumpMetrics(t *testing.T, h http.Handler) string {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, rr.Code)
	return rr.Body.String()
}

// findServiceLabel ищет любую серию databus_requests_total и возвращает
// её service-label (для подтверждения изоляции реестров).
func findServiceLabel(t *testing.T, h http.Handler) string {
	t.Helper()
	dump := dumpMetrics(t, h)
	for _, line := range strings.Split(dump, "\n") {
		if !strings.HasPrefix(line, "databus_requests_total{") {
			continue
		}
		i := strings.Index(line, `service="`)
		if i < 0 {
			continue
		}
		rest := line[i:]
		j := strings.Index(rest[len(`service="`):], `"`)
		if j < 0 {
			continue
		}
		return rest[:len(`service="`)+j+1]
	}
	t.Fatalf("no databus_requests_total series in /metrics:\n%s", dump)
	return ""
}

func requireSample(t *testing.T, dump, want string) {
	t.Helper()
	for _, line := range strings.Split(dump, "\n") {
		if strings.HasPrefix(line, want) {
			return
		}
	}
	t.Fatalf("expected sample not found:\nwant: %s\n\nactual /metrics:\n%s", want, dump)
}
