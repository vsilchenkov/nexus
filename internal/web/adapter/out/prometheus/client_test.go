package prometheus

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"nexus/internal/platform/logging"
)

// vectorResp формирует Prometheus instant-vector JSON.
// samples: пары (labels-json, value).
func vectorResp(samples ...string) string {
	return `{"status":"success","data":{"resultType":"vector","result":[` +
		strings.Join(samples, ",") + `]}}`
}

func sample(metric, value string) string {
	return fmt.Sprintf(`{"metric":%s,"value":[1700000000,%q]}`, metric, value)
}

// newTestServer поднимает фейковый Prometheus query API, отвечающий по
// содержимому PromQL-запроса.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		q := r.FormValue("query")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(q, "nexus_kafka_lag"):
			_, _ = w.Write([]byte(vectorResp(sample("{}", "312"))))
		case strings.Contains(q, "status=~") && strings.Contains(q, "by (node)"):
			_, _ = w.Write([]byte(vectorResp(sample(`{"node":"webhook/send"}`, "2"))))
		case strings.Contains(q, `service="receiver"`) && strings.Contains(q, "by (node)"):
			_, _ = w.Write([]byte(vectorResp(sample(`{"node":"webhook/send"}`, "4201"))))
		case strings.Contains(q, `service="sender"`) && strings.Contains(q, "by (node)"):
			_, _ = w.Write([]byte(vectorResp(sample(`{"node":"webhook/send"}`, "4198"))))
		case strings.Contains(q, "status=~"):
			_, _ = w.Write([]byte(vectorResp(sample("{}", "1845"))))
		case strings.Contains(q, `service="receiver"`):
			_, _ = w.Write([]byte(vectorResp(sample("{}", "1240000"))))
		case strings.Contains(q, `service="sender"`):
			_, _ = w.Write([]byte(vectorResp(sample("{}", "1230000"))))
		default:
			_, _ = w.Write([]byte(vectorResp())) // empty vector
		}
	}))
}

func TestClient_New_EmptyURL(t *testing.T) {
	t.Parallel()
	_, err := New("", time.Second, logging.NewNoop())
	require.Error(t, err)
}

func TestClient_GlobalTotals(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t)
	defer srv.Close()

	c, err := New(srv.URL, time.Second, logging.NewNoop())
	require.NoError(t, err)

	totals, err := c.GlobalTotals(context.Background(), 24*time.Hour)
	require.NoError(t, err)
	require.EqualValues(t, 1_240_000, totals.Incoming)
	require.EqualValues(t, 1_230_000, totals.Outgoing)
	require.EqualValues(t, 1845, totals.Errors)
}

func TestClient_KafkaQueue(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t)
	defer srv.Close()

	c, err := New(srv.URL, time.Second, logging.NewNoop())
	require.NoError(t, err)

	q, err := c.KafkaQueue(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 312, q)
}

func TestClient_NodeThroughput(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t)
	defer srv.Close()

	c, err := New(srv.URL, time.Second, logging.NewNoop())
	require.NoError(t, err)

	m, err := c.NodeThroughput(context.Background(), time.Now().Add(-time.Hour), time.Now())
	require.NoError(t, err)
	require.Contains(t, m, "webhook/send")
	require.EqualValues(t, 4201, m["webhook/send"].In)
	require.EqualValues(t, 4198, m["webhook/send"].Out)
	require.EqualValues(t, 2, m["webhook/send"].Errors)
}

func TestClient_NodeKPI(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		q := r.FormValue("query")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(q, "histogram_quantile(0.95"):
			_, _ = w.Write([]byte(vectorResp(sample("{}", "0.087")))) // 87 мс
		case strings.Contains(q, "histogram_quantile(0.99"):
			_, _ = w.Write([]byte(vectorResp(sample("{}", "0.142")))) // 142 мс
		case strings.Contains(q, "nexus_request_incomplete_total"):
			_, _ = w.Write([]byte(vectorResp(sample("{}", "2"))))
		case strings.Contains(q, "nexus_requests_total"):
			_, _ = w.Write([]byte(vectorResp(sample("{}", "100"))))
		default:
			_, _ = w.Write([]byte(vectorResp()))
		}
	}))
	defer srv.Close()

	c, err := New(srv.URL, time.Second, logging.NewNoop())
	require.NoError(t, err)

	kpi, err := c.NodeKPI(context.Background(), "webhook/send", time.Now().Add(-time.Hour), time.Now())
	require.NoError(t, err)
	require.EqualValues(t, 100, kpi.Total)
	require.EqualValues(t, 2, kpi.Errors)
	require.EqualValues(t, 98, kpi.Delivered) // total - errors
	require.InDelta(t, 87, kpi.P95ms, 0.5)
	require.InDelta(t, 142, kpi.P99ms, 0.5)
}

// matrixResp формирует Prometheus range-matrix JSON с одной серией.
func matrixResp(values ...string) string {
	return `{"status":"success","data":{"resultType":"matrix","result":[` +
		`{"metric":{},"values":[` + strings.Join(values, ",") + `]}]}}`
}

func TestClient_NodeChart(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		q := r.FormValue("query")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(q, "nexus_request_incomplete_total"):
			_, _ = w.Write([]byte(matrixResp(`[1700000060,"1"]`, `[1700000120,"3"]`)))
		case strings.Contains(q, "nexus_requests_total"):
			_, _ = w.Write([]byte(matrixResp(`[1700000060,"10"]`, `[1700000120,"20"]`)))
		default:
			_, _ = w.Write([]byte(matrixResp()))
		}
	}))
	defer srv.Close()

	c, err := New(srv.URL, time.Second, logging.NewNoop())
	require.NoError(t, err)

	since := time.Now().Add(-2 * time.Minute)
	until := time.Now()
	pts, err := c.NodeChart(context.Background(), "webhook/send", since, until, 2)
	require.NoError(t, err)
	require.Len(t, pts, 2)
	require.EqualValues(t, 10, pts[0].Count)
	require.EqualValues(t, 1, pts[0].Errors)
	require.EqualValues(t, 20, pts[1].Count)
	require.EqualValues(t, 3, pts[1].Errors)
}

func TestClient_EmptyVector(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(vectorResp()))
	}))
	defer srv.Close()

	c, err := New(srv.URL, time.Second, logging.NewNoop())
	require.NoError(t, err)
	q, err := c.KafkaQueue(context.Background())
	require.NoError(t, err)
	require.Zero(t, q)
}
