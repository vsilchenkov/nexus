package metrics_test

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/metrics"
)

// perNodeFamilies — per-node ряды §101.3. Список держит guard-тесты ниже честными:
// рефлексия, которая ничего не нашла, выглядела бы зелёной.
func perNodeFamilies() []string {
	return []string{
		"nexus_requests_total",
		"nexus_request_incomplete_total",
		"nexus_request_duration_seconds",
		"nexus_node_last_request_error",
		"nexus_async_ingress_rejected_total",
		"nexus_async_ack_render_failed_total",
		"nexus_rmq_messages_pulled_total",
		"nexus_rmq_pull_duration_seconds",
		"nexus_rmq_connection_state",
		"nexus_rmq_queue_depth",
		"nexus_rmq_consumer_count",
		"nexus_node_degraded",
		"nexus_dlq_reprocess_total",
		"nexus_replay_body_source_total",
		"nexus_paused_requeued_total",
	}
}

// TestPerNodeMetrics_CarryTeamLabel (§101.3) — у каждого вектора с меткой node
// есть и метка team. Путь уникален лишь внутри команды: вектор без team склеивает
// одноимённые узлы разных команд в один ряд — тот дефект, с которого начался §101
// (строка telegram без ссылки в «Top failures»).
func TestPerNodeMetrics_CarryTeamLabel(t *testing.T) {
	t.Parallel()
	m := metrics.New("sender")
	re := regexp.MustCompile(`fqName: "([^"]+)".*variableLabels: \{([^}]*)\}`)

	var withNode []string
	v := reflect.ValueOf(m).Elem()
	for i := range v.NumField() {
		if !v.Type().Field(i).IsExported() {
			continue
		}
		c, ok := v.Field(i).Interface().(prometheus.Collector)
		if !ok {
			continue
		}
		ch := make(chan *prometheus.Desc, 16)
		c.Describe(ch)
		close(ch)
		for d := range ch {
			sm := re.FindStringSubmatch(d.String())
			require.NotNil(t, sm, "формат prometheus.Desc изменился: %s", d)
			labels := strings.Split(sm[2], ",")
			if !slices.Contains(labels, "node") {
				continue
			}
			withNode = append(withNode, sm[1])
			assert.Contains(t, labels, "team", "%s: метка node без team", sm[1])
		}
	}
	assert.ElementsMatch(t, perNodeFamilies(), withNode)
}

// TestNodeLabels_TypedMethodsWriteBothLabels — каждый типизированный метод кладёт
// слог в team, а путь в node. Позиционный WithLabelValues внутри метода с
// перепутанным порядком молча записал бы путь в метку команды.
func TestNodeLabels_TypedMethodsWriteBothLabels(t *testing.T) {
	t.Parallel()
	m := metrics.New("sender")
	n := metrics.NodeLabels{Team: "team-x", Node: "node-y"}

	m.IncRequest("request", n, "200")
	m.ObserveRequestDuration("request", n, 0.1)
	m.IncRequestIncomplete("request", n)
	m.SetNodeLastRequestOutcome(n, domain.NodeOutcomeDown)
	m.IncAsyncIngressRejected(n)
	m.IncAckRenderFailed(n, "compile")
	m.IncRMQPulled(n, "ok")
	m.ObserveRMQPull(n, 0.1)
	m.SetRMQConnectionState(n, 2)
	m.SetRMQQueueStats(n, 5, 1)
	m.SetNodeDegraded(n, "auth", true)
	m.IncDLQReprocess(n, "dropped")
	m.IncReplayBodySource(n, "queue")
	m.IncPausedRequeued(n)

	families, err := m.Registry().Gather()
	require.NoError(t, err)
	var seen []string
	for _, f := range families {
		if !slices.Contains(perNodeFamilies(), f.GetName()) {
			continue
		}
		seen = append(seen, f.GetName())
		for _, metric := range f.GetMetric() {
			got := map[string]string{}
			for _, l := range metric.GetLabel() {
				got[l.GetName()] = l.GetValue()
			}
			assert.Equal(t, "team-x", got["team"], f.GetName())
			assert.Equal(t, "node-y", got["node"], f.GetName())
		}
	}
	assert.ElementsMatch(t, perNodeFamilies(), seen, "каждый per-node ряд пишется своим методом")
}

// TestNodeLabels_SameNameNodesSplit (§101.3) — боевой случай: telegram в командах
// messengers и webhook. До §101 оба узла писали в ряд node="telegram", и ошибки
// одной команды складывались с ошибками другой.
func TestNodeLabels_SameNameNodesSplit(t *testing.T) {
	t.Parallel()
	m := metrics.New("sender")
	messengers := metrics.NodeLabels{Team: "messengers", Node: "telegram"}
	webhook := metrics.NodeLabels{Team: "webhook", Node: "telegram"}

	for range 3 {
		m.IncRequest("requestAsync", messengers, "500")
	}
	m.IncRequest("requestAsync", webhook, "500")
	m.SetNodeLastRequestOutcome(messengers, domain.NodeOutcomeDown)
	m.SetNodeLastRequestOutcome(webhook, domain.NodeOutcomeOK)

	require.Equal(t, 2, testutil.CollectAndCount(m.RequestsTotal))
	assert.InDelta(t, 3, testutil.ToFloat64(m.RequestsTotal.WithLabelValues("requestAsync", "messengers", "telegram", "500")), 0)
	assert.InDelta(t, 1, testutil.ToFloat64(m.RequestsTotal.WithLabelValues("requestAsync", "webhook", "telegram", "500")), 0)
	assert.InDelta(t, 2, testutil.ToFloat64(m.NodeLastRequestError.WithLabelValues("messengers", "telegram")), 0)
	assert.InDelta(t, 0, testutil.ToFloat64(m.NodeLastRequestError.WithLabelValues("webhook", "telegram")), 0)
}

// TestPerNodeMetrics_WrittenOnlyThroughTypedMethods (§101.3) — вне пакета metrics
// per-node векторы не пишутся напрямую. Позиционный WithLabelValues с неверным
// числом значений паникует уже в рантайме (на бою — на первом запросе к узлу), а
// с перепутанным порядком молча пишет путь в метку команды. Тесты читают ряды
// через WithLabelValues законно — _test.go не проверяются.
func TestPerNodeMetrics_WrittenOnlyThroughTypedMethods(t *testing.T) {
	t.Parallel()
	re := regexp.MustCompile(`\.(RequestsTotal|RequestsIncompleteTotal|NodeLastRequestError|` +
		`AsyncIngressRejectedTotal|AckRenderFailedTotal|RequestDuration|RMQMessagesPulledTotal|` +
		`RMQPullDuration|RMQConnectionState|RMQQueueDepth|RMQConsumerCount|NodeDegraded|` +
		`DLQReprocessTotal|ReplayBodySourceTotal|PausedRequeuedTotal)\s*\.\s*` +
		`(WithLabelValues|With|GetMetricWithLabelValues|GetMetricWith|CurryWith|MustCurryWith)\(`)
	own, err := filepath.Abs(".")
	require.NoError(t, err)

	var offenders []string
	scanned := 0
	err = filepath.WalkDir(filepath.Join("..", ".."), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if abs, _ := filepath.Abs(path); abs == own {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		for _, loc := range re.FindAllIndex(src, -1) {
			line := 1 + bytes.Count(src[:loc[0]], []byte("\n"))
			offenders = append(offenders, fmt.Sprintf("%s:%d", filepath.ToSlash(path), line))
		}
		return nil
	})
	require.NoError(t, err)
	require.Greater(t, scanned, 100, "обход internal/ ничего не нашёл — guard был бы пустым")
	assert.Empty(t, offenders, "per-node метрики пишутся методами metrics.Metrics с NodeLabels")
}
