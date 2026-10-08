package prometheus

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
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
		case strings.Contains(q, "kafka_log_log_size"):
			// §75: размер топиков из JMX-агента. Две серии одного топика
			// (несколько брокеров/партиций в ответе без агрегации) должны
			// сложиться; сэмпл без метки topic и нулевой — отброситься.
			_, _ = w.Write([]byte(vectorResp(
				sample(`{"topic":"nexus.async"}`, "1000000000"),
				sample(`{"topic":"nexus.async"}`, "503238553"),
				sample(`{"topic":"nexus.async.dlq"}`, "12907"),
				sample(`{"topic":"nexus.logs.retry"}`, "0"),
				sample("{}", "999"))))
		case strings.Contains(q, "nexus_kafka_lag"):
			_, _ = w.Write([]byte(vectorResp(sample("{}", "312"))))
		case strings.Contains(q, "nexus_node_last_request_error"):
			// §41/§52: webhook/send — degraded (1), webhook/push — down (2).
			_, _ = w.Write([]byte(vectorResp(
				sample(`{"team":"hooks","node":"webhook/send"}`, "1"),
				sample(`{"team":"hooks","node":"webhook/push"}`, "2"))))
		case strings.Contains(q, "status=~") && strings.Contains(q, "by (team, node)"):
			// §101.9: тот же путь в соседней команде — отдельный ряд.
			_, _ = w.Write([]byte(vectorResp(
				sample(`{"team":"hooks","node":"webhook/send"}`, "2"),
				sample(`{"team":"other","node":"webhook/send"}`, "9"))))
		case strings.Contains(q, `service="receiver"`) && strings.Contains(q, "by (team, node)"):
			_, _ = w.Write([]byte(vectorResp(sample(`{"team":"hooks","node":"webhook/send"}`, "4201"))))
		case strings.Contains(q, `service="sender"`) && strings.Contains(q, "by (team, node)"):
			_, _ = w.Write([]byte(vectorResp(sample(`{"team":"hooks","node":"webhook/send"}`, "4198"))))
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

// queryRecorder — фейковый Prometheus, запоминающий запросы: часть контракта
// адаптера — сам PromQL (группировка, матчеры), а не только разбор ответа.
type queryRecorder struct {
	mu      sync.Mutex
	queries []string
}

func (r *queryRecorder) server(t *testing.T, resp string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_ = req.ParseForm()
		r.mu.Lock()
		r.queries = append(r.queries, req.FormValue("query"))
		r.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(resp))
	}))
}

func (r *queryRecorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.queries...)
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

	totals, err := c.GlobalTotals(context.Background(), 24*time.Hour, "")
	require.NoError(t, err)
	require.EqualValues(t, 1_240_000, totals.Incoming)
	require.EqualValues(t, 1_230_000, totals.Outgoing)
	require.EqualValues(t, 1845, totals.Errors)
}

// TestClient_GlobalTotals_TeamFilter (§101.9): итоги диагностики сравниваются
// с ClickHouse одной команды, поэтому каждый из трёх запросов обязан нести
// матчер её слога (с экранированием). Пустой слог фильтра не добавляет.
func TestClient_GlobalTotals_TeamFilter(t *testing.T) {
	t.Parallel()
	rec := &queryRecorder{}
	srv := rec.server(t, vectorResp(sample("{}", "1")))
	defer srv.Close()

	c, err := New(srv.URL, time.Second, logging.NewNoop())
	require.NoError(t, err)

	_, err = c.GlobalTotals(context.Background(), time.Hour, `mess"engers`)
	require.NoError(t, err)
	queries := rec.all()
	require.Len(t, queries, 3)
	for _, q := range queries {
		require.Contains(t, q, `,team="mess\"engers"}`)
	}

	_, err = c.GlobalTotals(context.Background(), time.Hour, "")
	require.NoError(t, err)
	for _, q := range rec.all()[3:] {
		require.NotContains(t, q, "team=")
	}
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

// TestClient_KafkaLag_MainGroupOnly (§113.4): lag в KPI, сводке и ряде — только
// основная группа и max по репликам (каждая публикует lag брокера целиком).
// Без опции — все группы, но тоже без удвоения репликами.
func TestClient_KafkaLag_MainGroupOnly(t *testing.T) {
	t.Parallel()
	rec := &queryRecorder{}
	srv := rec.server(t, vectorResp(sample("{}", "1")))
	defer srv.Close()

	c, err := New(srv.URL, time.Second, logging.NewNoop(), WithKafkaMainGroup(`nexus-"sender`))
	require.NoError(t, err)
	ctx := context.Background()
	_, err = c.KafkaQueue(ctx)
	require.NoError(t, err)
	_, err = c.KafkaOverview(ctx, time.Now().Add(-time.Hour), time.Now())
	require.NoError(t, err)

	const want = `sum(max by (group, topic, partition) (nexus_kafka_lag{group="nexus-\"sender"}))`
	var lagQueries []string
	for _, q := range rec.all() {
		if strings.Contains(q, "nexus_kafka_lag") {
			lagQueries = append(lagQueries, q)
		}
	}
	require.Equal(t, []string{want, want}, lagQueries)
	require.Equal(t, want, kafkaSeriesQuery("lag", "30s", c.lagQuery))

	all, err := New(srv.URL, time.Second, logging.NewNoop(), WithKafkaMainGroup(""))
	require.NoError(t, err)
	require.Equal(t, `sum(max by (group, topic, partition) (nexus_kafka_lag))`, all.lagQuery)
}

// TestClient_PausedNotCountedAsOutgoing (§113.5): откладывание из-за паузы —
// не исходящий вызов. Каждый запрос исходящих Sender'а несёт status!="paused";
// топ узлов страницы Kafka — только async-трафик.
func TestClient_PausedNotCountedAsOutgoing(t *testing.T) {
	t.Parallel()
	rec := &queryRecorder{}
	srv := rec.server(t, vectorResp(sample("{}", "1")))
	defer srv.Close()
	c, err := New(srv.URL, time.Second, logging.NewNoop())
	require.NoError(t, err)
	ctx := context.Background()
	since, until := time.Now().Add(-time.Hour), time.Now()

	_, err = c.GlobalTotals(ctx, time.Hour, "")
	require.NoError(t, err)
	_, err = c.NodeThroughput(ctx, since, until)
	require.NoError(t, err)
	_, err = c.KafkaOverview(ctx, since, until)
	require.NoError(t, err)
	before := len(rec.all())
	_, err = c.KafkaNodeThroughput(ctx, since, until)
	require.NoError(t, err)

	outgoing := 0
	for _, q := range rec.all() {
		if strings.Contains(q, `nexus_requests_total{service="sender"`) && !strings.Contains(q, "status=~") {
			outgoing++
			require.Contains(t, q, `status!="paused"`, q)
		}
	}
	require.Equal(t, 4, outgoing, "по одному из GlobalTotals, NodeThroughput, KafkaOverview, KafkaNodeThroughput")
	for _, q := range rec.all()[before:] {
		require.Contains(t, q, `method="requestAsync"`, q)
	}
	require.Contains(t, kafkaSeriesQuery("consumed", "30s", ""), `status!="paused"`)
}

// TestClient_NodeFailures: «Top failures» страницы Kafka — весь трафик. Ровно
// два запроса (исходящие и ошибки), без фильтра по методу: с method=
// "requestAsync" ошибки sync-узлов пропадали из блока (регрессия 1.41.0).
// Исходящие — без откладываний из-за паузы (§113.5), иначе доля ошибок
// размывается переносами.
func TestClient_NodeFailures(t *testing.T) {
	t.Parallel()
	rec := &queryRecorder{}
	srv := rec.server(t, vectorResp(sample(`{"team":"mcp","node":"mcp-1c"}`, "4")))
	defer srv.Close()
	c, err := New(srv.URL, time.Second, logging.NewNoop())
	require.NoError(t, err)

	got, err := c.NodeFailures(context.Background(), time.Now().Add(-time.Hour), time.Now())
	require.NoError(t, err)

	queries := rec.all()
	require.Len(t, queries, 2)
	for _, q := range queries {
		require.NotContains(t, q, "method=", q)
		require.Contains(t, q, "by (team, node)", q)
	}
	require.Contains(t, queries[0], `status!="paused"`)
	require.Contains(t, queries[1], `status=~"0|[45].."`)
	require.Equal(t, map[port.NodeKey]port.NodeThroughput{
		{Team: "mcp", Path: "mcp-1c"}: {Out: 4, Errors: 4},
	}, got)
}

// TestClient_KafkaTopicSizes (§75): размер собирается по метке topic. Нулевая
// серия остаётся в карте — «топик пуст» не должно превращаться в «источника
// нет» (на этом различии стоит sizes_available). Сэмпл без метки отбрасывается.
func TestClient_KafkaTopicSizes(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t)
	defer srv.Close()

	c, err := New(srv.URL, time.Second, logging.NewNoop())
	require.NoError(t, err)

	sizes, err := c.KafkaTopicSizes(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 1_503_238_553, sizes["nexus.async"])
	require.EqualValues(t, 12_907, sizes["nexus.async.dlq"])
	require.Contains(t, sizes, "nexus.logs.retry")
	require.EqualValues(t, 0, sizes["nexus.logs.retry"])
	require.Len(t, sizes, 3)
}

// TestClient_KafkaTopicSizes_NoMetric (§75): пока JMX-агент не поднят, запрос
// возвращает пустой вектор — это штатная деградация, а не ошибка.
func TestClient_KafkaTopicSizes_NoMetric(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(vectorResp()))
	}))
	defer srv.Close()

	c, err := New(srv.URL, time.Second, logging.NewNoop())
	require.NoError(t, err)

	sizes, err := c.KafkaTopicSizes(context.Background())
	require.NoError(t, err)
	require.Empty(t, sizes)
}

// TestClient_NodeThroughput (§101.9): ключ — пара (team, node). Тот же путь в
// соседней команде даёт свой ряд, а не прибавляется к чужому.
func TestClient_NodeThroughput(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t)
	defer srv.Close()

	c, err := New(srv.URL, time.Second, logging.NewNoop())
	require.NoError(t, err)

	m, err := c.NodeThroughput(context.Background(), time.Now().Add(-time.Hour), time.Now())
	require.NoError(t, err)
	hooks := port.NodeKey{Team: "hooks", Path: "webhook/send"}
	other := port.NodeKey{Team: "other", Path: "webhook/send"}
	require.Contains(t, m, hooks)
	require.EqualValues(t, 4201, m[hooks].In)
	require.EqualValues(t, 4198, m[hooks].Out)
	require.EqualValues(t, 2, m[hooks].Errors)
	require.EqualValues(t, 9, m[other].Errors)
	require.Zero(t, m[other].In)
	require.Len(t, m, 2)
}

// TestClient_NodeLastErrors (§41/§52): instant gauge последнего вызова
// per-node парсится в map как сырой float (0=ok, 1=degraded, 2=down);
// маппинг в NodeOutcome делает usecase.
func TestClient_NodeLastErrors(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t)
	defer srv.Close()

	c, err := New(srv.URL, time.Second, logging.NewNoop())
	require.NoError(t, err)

	m, err := c.NodeLastErrors(context.Background(), time.Now())
	require.NoError(t, err)
	require.EqualValues(t, 1, m[port.NodeKey{Team: "hooks", Path: "webhook/send"}])
	require.EqualValues(t, 2, m[port.NodeKey{Team: "hooks", Path: "webhook/push"}])
}

// TestClient_NodeErrors_SameNameTeams (§101.9) — боевой случай: путь telegram в
// командах messengers (1 747 ошибок) и webhook (8). До §101 это был один ряд
// 1 755, и уведомление приписывало его обеим командам. Ряд без team (собран до
// обновления) остаётся отдельным ключом, ряд без node (выход до резолва узла) —
// отбрасывается.
func TestClient_NodeErrors_SameNameTeams(t *testing.T) {
	t.Parallel()
	rec := &queryRecorder{}
	srv := rec.server(t, vectorResp(
		sample(`{"team":"messengers","node":"telegram"}`, "1747"),
		sample(`{"team":"webhook","node":"telegram"}`, "8"),
		sample(`{"node":"telegram"}`, "412"),
		sample(`{"team":"","node":""}`, "5")))
	defer srv.Close()

	c, err := New(srv.URL, time.Second, logging.NewNoop())
	require.NoError(t, err)

	m, err := c.NodeErrors(context.Background(), time.Hour)
	require.NoError(t, err)
	require.Equal(t, map[port.NodeKey]float64{
		{Team: "messengers", Path: "telegram"}: 1747,
		{Team: "webhook", Path: "telegram"}:    8,
		{Path: "telegram"}:                     412,
	}, m)
	require.Contains(t, rec.all()[0], "sum by (team, node)(")
}

// TestClient_NodeSeries_ByTeamAndNode (§101.9): спарклайны одноимённых узлов —
// два ряда; серия без метки node пропускается.
func TestClient_NodeSeries_ByTeamAndNode(t *testing.T) {
	t.Parallel()
	rec := &queryRecorder{}
	srv := rec.server(t, `{"status":"success","data":{"resultType":"matrix","result":[`+
		`{"metric":{"team":"messengers","node":"telegram"},"values":[[1700000060,"10"],[1700000120,"20"]]},`+
		`{"metric":{"team":"webhook","node":"telegram"},"values":[[1700000060,"1"]]},`+
		`{"metric":{},"values":[[1700000060,"99"]]}]}}`)
	defer srv.Close()

	c, err := New(srv.URL, time.Second, logging.NewNoop())
	require.NoError(t, err)

	m, err := c.NodeSeries(context.Background(), time.Now().Add(-2*time.Minute), time.Now(), 2)
	require.NoError(t, err)
	require.Equal(t, map[port.NodeKey][]float64{
		{Team: "messengers", Path: "telegram"}: {10, 20},
		{Team: "webhook", Path: "telegram"}:    {1},
	}, m)
	require.Contains(t, rec.all()[0], "sum by (team, node)(")
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
