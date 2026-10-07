package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/logging"
)

const senderMetricsText = `# HELP nexus_kafka_consumer_restarts_total Reader restarts.
# TYPE nexus_kafka_consumer_restarts_total counter
nexus_kafka_consumer_restarts_total{group="nexus-sender",reason="stalled"} 2
nexus_kafka_consumer_restarts_total{group="nexus-sender-paused",reason="pass_idle"} 1
nexus_kafka_consumer_restarts_total_created{group="nexus-sender"} 1.7e+09
# HELP nexus_kafka_commit_errors_total Rejected commits.
# TYPE nexus_kafka_commit_errors_total counter
nexus_kafka_commit_errors_total{group="nexus-sender"} 0
nexus_kafka_commit_errors_total{group="nexus-sender-paused"} 3
nexus_kafka_lag 10
`

// TestCounterSum (§114.L): сумма всех рядов счётчика; ряды с суффиксом и
// чужие метрики не попадают, нет ряда — ноль.
func TestCounterSum(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		text    string
		metric  string
		want    float64
		wantErr bool
	}{
		{name: "ряды с метками складываются", text: senderMetricsText, metric: metricConsumerRestarts, want: 3},
		{name: "второй счётчик", text: senderMetricsText, metric: metricCommitErrors, want: 3},
		{name: "ряда нет — счётчик ещё не рос", text: "nexus_kafka_lag 10\n", metric: metricCommitErrors, want: 0},
		{name: "ряд без меток", text: "nexus_kafka_commit_errors_total 4\n", metric: metricCommitErrors, want: 4},
		{name: "ряд с отметкой времени", text: "nexus_kafka_commit_errors_total{g=\"a\"} 5 1700000000000\n", metric: metricCommitErrors, want: 5},
		{name: "битое значение", text: "nexus_kafka_commit_errors_total{g=\"a\"} x\n", metric: metricCommitErrors, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := counterSum(tt.text, tt.metric)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.InDelta(t, tt.want, got, 1e-9)
		})
	}
}

func TestCounterDelta(t *testing.T) {
	t.Parallel()
	assert.InDelta(t, 2.0, counterDelta(3, 5), 1e-9)
	assert.InDelta(t, 0.0, counterDelta(5, 5), 1e-9)
	assert.InDelta(t, 1.0, counterDelta(5, 1), 1e-9, "Sender перезапускался — счётчик начался заново")
}

// TestSenderWatch: прирост между снимками, а несостоявшийся снимок — провал
// проверки, а не «нарушений нет».
func TestSenderWatch(t *testing.T) {
	t.Parallel()
	body := "nexus_kafka_consumer_restarts_total{group=\"g\"} 1\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	w := watchSender(context.Background(), srv.Client(), srv.URL)
	body = "nexus_kafka_consumer_restarts_total{group=\"g\"} 3\nnexus_kafka_commit_errors_total{group=\"g\"} 2\n"
	r := w.finish(context.Background())
	require.NotNil(t, r)
	assert.InDelta(t, 2.0, r.RestartsDelta, 1e-9)
	assert.InDelta(t, 2.0, r.CommitErrorsDelta, 1e-9)
	assert.Len(t, r.problems(), 2)

	assert.Nil(t, watchSender(context.Background(), srv.Client(), "").finish(context.Background()), "проверка выключена")

	down := watchSender(context.Background(), srv.Client(), "http://127.0.0.1:1/metrics").finish(context.Background())
	require.NotNil(t, down)
	assert.NotEmpty(t, down.Error)
	assert.NotEmpty(t, down.problems())
}

// fakeKafkaAPI — ответы /api/kafka/* для pollKafkaPage.
func fakeKafkaAPI(overview, topics string, err error) getJSONFn {
	return func(_ context.Context, path string, dst any) (time.Duration, error) {
		if err != nil {
			return time.Millisecond, err
		}
		body := topics
		if path == "/api/kafka/overview?range=1h" {
			body = overview
		}
		return 2 * time.Millisecond, json.Unmarshal([]byte(body), dst)
	}
}

// TestPollKafkaPage (§114.L): «брокер не ответил» под нагрузкой — любой из
// признаков §114.5; ошибка запроса — отдельный счётчик.
func TestPollKafkaPage(t *testing.T) {
	t.Parallel()
	okOv := `{"kafka_available":true,"health":{"reason":"healthy"}}`
	okTp := `{"kafka_available":true}`
	tests := []struct {
		name            string
		get             getJSONFn
		wantUnavailable bool
		wantFailed      bool
	}{
		{name: "брокер отвечает", get: fakeKafkaAPI(okOv, okTp, nil)},
		{name: "overview: kafka_available=false",
			get:             fakeKafkaAPI(`{"kafka_available":false,"health":{"reason":"healthy"}}`, okTp, nil),
			wantUnavailable: true},
		{name: "overview: broker_unreachable",
			get:             fakeKafkaAPI(`{"kafka_available":true,"health":{"reason":"broker_unreachable"}}`, okTp, nil),
			wantUnavailable: true},
		{name: "topics: kafka_available=false", get: fakeKafkaAPI(okOv, `{"kafka_available":false}`, nil),
			wantUnavailable: true},
		{name: "запрос упал", get: fakeKafkaAPI("", "", errors.New("status 429")), wantFailed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := pollKafkaPage(context.Background(), tt.get)
			assert.Equal(t, tt.wantUnavailable, p.unavailable)
			assert.Equal(t, tt.wantFailed, p.failed)
		})
	}
}

func TestKafkaPageReport_Problems(t *testing.T) {
	t.Parallel()
	assert.Empty(t, kafkaPageReport{Polls: 3, OverviewMaxMs: 3200}.problems())
	assert.Len(t, kafkaPageReport{Polls: 3, OverviewMaxMs: 5100}.problems(), 1, "overview дольше 5 с")
	assert.Len(t, kafkaPageReport{Polls: 3, Unavailable: 1}.problems(), 1)
	assert.Len(t, kafkaPageReport{Polls: 3, Failures: 2}.problems(), 1)
	assert.Len(t, kafkaPageReport{}.problems(), 1, "ни одного опроса — проверка не состоялась")
}

// TestKafkaPageProbe: проба опрашивает до stop и сводит опросы в отчёт;
// выключенная проба отчёта не даёт.
func TestKafkaPageProbe(t *testing.T) {
	t.Parallel()
	get := fakeKafkaAPI(`{"kafka_available":true,"health":{"reason":"healthy"}}`, `{"kafka_available":false}`, nil)
	p := startKafkaPageProbe(context.Background(), get, time.Millisecond, logging.NewNoop())
	time.Sleep(20 * time.Millisecond)
	r := p.stop()
	require.NotNil(t, r)
	assert.Positive(t, r.Polls)
	assert.Equal(t, r.Polls, r.Unavailable, "каждый опрос видел topics.kafka_available=false")
	assert.InDelta(t, 2.0, r.OverviewMaxMs, 1e-9)

	assert.Nil(t, startKafkaPageProbe(context.Background(), get, 0, logging.NewNoop()).stop())
}

// TestKafkaPageProbe_StopMidPoll: опрос, прерванный самим stop(), — не ошибка
// страницы. Иначе прогон падал бы случайно: stop() попадал бы в запрос в
// полёте примерно раз на сотню прогонов.
func TestKafkaPageProbe_StopMidPoll(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	inFlight := make(chan struct{})
	ok := fakeKafkaAPI(`{"kafka_available":true,"health":{"reason":"healthy"}}`, `{"kafka_available":true}`, nil)
	get := func(ctx context.Context, path string, dst any) (time.Duration, error) {
		if calls.Add(1) <= 2 { // первый опрос — overview и topics
			return ok(ctx, path, dst)
		}
		if path == "/api/kafka/overview?range=1h" {
			close(inFlight)
		}
		<-ctx.Done()
		return time.Millisecond, ctx.Err()
	}
	p := startKafkaPageProbe(context.Background(), get, time.Millisecond, logging.NewNoop())
	<-inFlight
	r := p.stop()
	require.NotNil(t, r)
	assert.Equal(t, 1, r.Polls, "прерванный опрос в отчёт не входит")
	assert.Zero(t, r.Failures)
	assert.Empty(t, r.problems())
}

// TestReportWatchProblems: нарушения §114.L валят прогон, выключенные
// проверки — нет.
func TestReportWatchProblems(t *testing.T) {
	t.Parallel()
	assert.Empty(t, (&report{}).watchProblems())
	rep := &report{
		KafkaPage:     &kafkaPageReport{Polls: 2, Unavailable: 1},
		SenderMetrics: &senderMetricsReport{RestartsDelta: 1},
	}
	assert.Len(t, rep.watchProblems(), 2)
}
