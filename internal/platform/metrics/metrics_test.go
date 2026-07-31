package metrics_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/metrics"
)

// TestSetNodeLastRequestOutcome (§41/§52): gauge = 0 (ok) / 1 (degraded) /
// 2 (down); последний вызов перезаписывает значение.
func TestSetNodeLastRequestOutcome(t *testing.T) {
	t.Parallel()
	m := metrics.New("sender")

	m.SetNodeLastRequestOutcome("svc/ok", domain.NodeOutcomeOK)
	if v := testutil.ToFloat64(m.NodeLastRequestError.WithLabelValues("svc/ok")); v != 0 {
		t.Fatalf("ok node gauge = %v, want 0", v)
	}
	m.SetNodeLastRequestOutcome("svc/degraded", domain.NodeOutcomeDegraded)
	if v := testutil.ToFloat64(m.NodeLastRequestError.WithLabelValues("svc/degraded")); v != 1 {
		t.Fatalf("degraded node gauge = %v, want 1", v)
	}
	m.SetNodeLastRequestOutcome("svc/bad", domain.NodeOutcomeDown)
	if v := testutil.ToFloat64(m.NodeLastRequestError.WithLabelValues("svc/bad")); v != 2 {
		t.Fatalf("down node gauge = %v, want 2", v)
	}
	// Последний успешный вызов гасит «Down».
	m.SetNodeLastRequestOutcome("svc/bad", domain.NodeOutcomeOK)
	if v := testutil.ToFloat64(m.NodeLastRequestError.WithLabelValues("svc/bad")); v != 0 {
		t.Fatalf("after recovery gauge = %v, want 0", v)
	}
}

func TestNew_RegistersAllRequiredMetrics(t *testing.T) {
	t.Parallel()
	m := metrics.New("receiver")

	// CounterVec/HistogramVec не печатают строки без хотя бы одной серии —
	// поэтому сначала «прикасаемся» к каждому ряду §6, потом проверяем дамп.
	m.RequestsTotal.WithLabelValues("request", "demo", "200").Inc()
	m.RequestDuration.WithLabelValues("request", "demo").Observe(0.01)
	m.KafkaLag.WithLabelValues("nexus.async", "0", "sender").Set(0)
	m.CHBufferSize.WithLabelValues("nexus.logs").Set(0)
	m.CHErrorsTotal.WithLabelValues("nexus.logs", "insert").Add(0)

	rr := httptest.NewRecorder()
	m.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, rr.Code)
	body := rr.Body.String()

	for _, want := range []string{
		"nexus_requests_total",
		"nexus_request_duration_seconds",
		"nexus_kafka_lag",
		"nexus_clickhouse_buffer_size",
		"nexus_clickhouse_errors_total",
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
	r.Any("/api/v1/request/*path", func(c *gin.Context) {
		// Имитируем работу — observe в гистограмме должен быть > 0.
		time.Sleep(2 * time.Millisecond)
		c.Status(http.StatusOK)
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/request/partner/orders", nil)
	r.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	dump := dumpMetrics(t, m.Handler())

	// requests_total{method="request",node="partner/orders",status="200"} == 1.
	requireSample(t, dump,
		`nexus_requests_total{method="request",node="partner/orders",service="receiver",status="200"} 1`)

	// duration: количество наблюдений ровно 1.
	requireSample(t, dump,
		`nexus_request_duration_seconds_count{method="request",node="partner/orders",service="receiver"} 1`)
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
	// На /health и /metrics не должно быть ни одной серии nexus_requests_total.
	if strings.Contains(dump, "nexus_requests_total{") {
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
		`nexus_requests_total{method="GET /api/nodes/:id",node="",service="web",status="404"} 1`)
}

// ---- helpers ----

func dumpMetrics(t *testing.T, h http.Handler) string {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, rr.Code)
	return rr.Body.String()
}

// findServiceLabel ищет любую серию nexus_requests_total и возвращает
// её service-label (для подтверждения изоляции реестров).
func findServiceLabel(t *testing.T, h http.Handler) string {
	t.Helper()
	dump := dumpMetrics(t, h)
	for line := range strings.SplitSeq(dump, "\n") {
		if !strings.HasPrefix(line, "nexus_requests_total{") {
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
	t.Fatalf("no nexus_requests_total series in /metrics:\n%s", dump)
	return ""
}

func requireSample(t *testing.T, dump, want string) {
	t.Helper()
	for line := range strings.SplitSeq(dump, "\n") {
		if strings.HasPrefix(line, want) {
			return
		}
	}
	t.Fatalf("expected sample not found:\nwant: %s\n\nactual /metrics:\n%s", want, dump)
}

// §70.7: метка идентификатора ноды называется nexus_instance, а НЕ instance —
// последнюю занимает scrape Prometheus (адрес цели), и одноимённая метка
// приложения была бы переименована в exported_instance.
//
// Пустой идентификатор метку не добавляет: она входит в идентичность ряда, и её
// появление на действующей ноде разорвало бы историю графиков.
func TestNew_InstanceLabel(t *testing.T) {
	t.Parallel()

	withID := metrics.New("web", metrics.WithInstance("kz"))
	withID.RequestsTotal.WithLabelValues("request", "svc/hook", "200").Inc()
	dump := dumpMetrics(t, withID.Handler())
	if !strings.Contains(dump, `nexus_instance="kz"`) {
		t.Errorf("метка nexus_instance не выставлена:\n%s", dump)
	}
	if strings.Contains(dump, `,instance="`) || strings.Contains(dump, `{instance="`) {
		t.Errorf("метка instance зарезервирована scrape'ом и не должна использоваться:\n%s", dump)
	}

	plain := metrics.New("web", metrics.WithInstance(""))
	plain.RequestsTotal.WithLabelValues("request", "svc/hook", "200").Inc()
	if dump := dumpMetrics(t, plain.Handler()); strings.Contains(dump, "nexus_instance") {
		t.Errorf("пустой идентификатор не должен добавлять метку:\n%s", dump)
	}
}

// §74.3: состояние схемы PostgreSQL публикуется двумя рядами. Ряд
// nexus_pg_schema_ahead=1 — признак незавершённого отката (код старее схемы),
// на нём висит алерт NexusSchemaAheadOfBinary.
func TestSetSchemaState(t *testing.T) {
	t.Parallel()

	ahead := metrics.New("web")
	ahead.SetSchemaState(32, true)
	dump := dumpMetrics(t, ahead.Handler())
	requireSample(t, dump, `nexus_pg_schema_version{service="web"} 32`)
	requireSample(t, dump, `nexus_pg_schema_ahead{service="web"} 1`)

	// Штатное состояние: схема согласована с кодом.
	normal := metrics.New("receiver")
	normal.SetSchemaState(32, false)
	dump = dumpMetrics(t, normal.Handler())
	requireSample(t, dump, `nexus_pg_schema_version{service="receiver"} 32`)
	requireSample(t, dump, `nexus_pg_schema_ahead{service="receiver"} 0`)
}
