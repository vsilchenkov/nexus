// Package prometheus — Web-сторонний адаптер к Prometheus query API.
// Реализует port.PromMetrics: глобальные KPI, очередь Kafka и per-node
// throughput для дашбордов панели (§21, §6).
//
// Это клиент к серверу Prometheus (query/query_range), а не экспортёр
// /metrics самих сервисов.
package prometheus

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	papi "github.com/prometheus/client_golang/api"
	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"

	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// Client — обёртка над promv1.API с таймаутом на каждый запрос.
type Client struct {
	api     promv1.API
	timeout time.Duration
	logger  logging.Logger
}

var _ port.PromMetrics = (*Client)(nil)

// New создаёт клиент к Prometheus по адресу url (напр. http://prometheus:9090).
// Пустой url — ошибка: вызывающая сторона (app.go) не должна создавать клиент,
// если prometheus.url не задан.
func New(url string, timeout time.Duration, logger logging.Logger) (*Client, error) {
	if url == "" {
		return nil, fmt.Errorf("prometheus url is empty")
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	c, err := papi.NewClient(papi.Config{Address: url})
	if err != nil {
		return nil, fmt.Errorf("prometheus client: %w", err)
	}
	return &Client{api: promv1.NewAPI(c), timeout: timeout, logger: logger}, nil
}

// promRange форматирует длительность для PromQL range-selector ([Ns]).
func promRange(d time.Duration) string {
	secs := int64(d.Seconds())
	if secs <= 0 {
		secs = 1
	}
	return fmt.Sprintf("%ds", secs)
}

// instantScalar выполняет instant-query на момент time.Now(), ожидая
// скалярный результат (sum(...) → vector из одного элемента).
func (c *Client) instantScalar(ctx context.Context, query string) (float64, error) {
	return c.instantScalarAt(ctx, query, time.Now())
}

// instantScalarAt — как instantScalar, но на произвольный момент at. Пустой
// vector → 0.
func (c *Client) instantScalarAt(ctx context.Context, query string, at time.Time) (float64, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	val, _, err := c.api.Query(ctx, query, at)
	if err != nil {
		return 0, fmt.Errorf("prometheus query %q: %w", query, err)
	}
	vec, ok := val.(model.Vector)
	if !ok {
		return 0, fmt.Errorf("prometheus query %q: unexpected result type %T", query, val)
	}
	if len(vec) == 0 {
		return 0, nil
	}
	return float64(vec[0].Value), nil
}

// promLabel экранирует значение метки для PromQL-селектора (двойные кавычки):
// path узла может содержать спецсимволы, требующие экранирования.
func promLabel(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

// nonNegU64 округляет неотрицательное float-значение в uint64 (отрицательные
// и NaN → 0). Дубль usecase.f2u, чтобы не тащить зависимость на usecase.
func nonNegU64(v float64) uint64 {
	if math.IsNaN(v) || v <= 0 {
		return 0
	}
	return uint64(v + 0.5)
}

// quantileMs нормализует результат histogram_quantile (секунды) в мс:
// NaN/отрицательные (пустые бакеты) → 0.
func quantileMs(v float64) float64 {
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	return v * 1000
}

// instantByNode выполняет instant-query вида `sum by (node)(...)` на момент at
// и собирает результат в map по метке node.
func (c *Client) instantByNode(ctx context.Context, query string, at time.Time) (map[string]float64, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	val, _, err := c.api.Query(ctx, query, at)
	if err != nil {
		return nil, fmt.Errorf("prometheus query %q: %w", query, err)
	}
	vec, ok := val.(model.Vector)
	if !ok {
		return nil, fmt.Errorf("prometheus query %q: unexpected result type %T", query, val)
	}
	out := make(map[string]float64, len(vec))
	for _, s := range vec {
		node := string(s.Metric["node"])
		if node == "" {
			continue
		}
		out[node] += float64(s.Value)
	}
	return out, nil
}

// GlobalTotals — incoming (receiver) / outgoing (sender) / errors (sender 0|4xx|5xx).
func (c *Client) GlobalTotals(ctx context.Context, window time.Duration) (port.GlobalTotals, error) {
	w := promRange(window)
	var t port.GlobalTotals
	var err error
	if t.Incoming, err = c.instantScalar(ctx,
		fmt.Sprintf(`sum(increase(nexus_requests_total{service="receiver"}[%s]))`, w)); err != nil {
		return t, err
	}
	if t.Outgoing, err = c.instantScalar(ctx,
		fmt.Sprintf(`sum(increase(nexus_requests_total{service="sender"}[%s]))`, w)); err != nil {
		return t, err
	}
	if t.Errors, err = c.instantScalar(ctx,
		fmt.Sprintf(`sum(increase(nexus_requests_total{service="sender",status=~"0|[45].."}[%s]))`, w)); err != nil {
		return t, err
	}
	return t, nil
}

// KafkaQueue — суммарный lag всех партиций (мгновенно).
func (c *Client) KafkaQueue(ctx context.Context) (float64, error) {
	return c.instantScalar(ctx, `sum(nexus_kafka_lag)`)
}

// NodeErrors — per-node число «незавершённых» вызовов (done=0) за окно
// из nexus_request_incomplete_total (§22, Telegram-алерты).
func (c *Client) NodeErrors(ctx context.Context, window time.Duration) (map[string]float64, error) {
	return c.instantByNode(ctx,
		fmt.Sprintf(`sum by (node)(increase(nexus_request_incomplete_total{service="sender"}[%s]))`,
			promRange(window)), time.Now())
}

// NodeThroughput — per-node in/out/errors за период (since, until] (ключ —
// метка node = path). Окно = until-since, запрос вычисляется на момент until
// (поддержка произвольного календарного периода, §28 Пункт 4).
func (c *Client) NodeThroughput(ctx context.Context, since, until time.Time) (map[string]port.NodeThroughput, error) {
	w := promRange(until.Sub(since))
	in, err := c.instantByNode(ctx,
		fmt.Sprintf(`sum by (node)(increase(nexus_requests_total{service="receiver"}[%s]))`, w), until)
	if err != nil {
		return nil, err
	}
	out, err := c.instantByNode(ctx,
		fmt.Sprintf(`sum by (node)(increase(nexus_requests_total{service="sender"}[%s]))`, w), until)
	if err != nil {
		return nil, err
	}
	errs, err := c.instantByNode(ctx,
		fmt.Sprintf(`sum by (node)(increase(nexus_requests_total{service="sender",status=~"0|[45].."}[%s]))`, w), until)
	if err != nil {
		return nil, err
	}
	// p95 длительности исходящих (Sender) по узлам — гистограмма уже пишется
	// и sync, и async (§22, новой метрики не нужно). Значение в секундах → мс.
	p95, err := c.instantByNode(ctx,
		fmt.Sprintf(`histogram_quantile(0.95, sum by (node, le)(rate(nexus_request_duration_seconds_bucket{service="sender"}[%s])))`, w), until)
	if err != nil {
		return nil, err
	}

	res := make(map[string]port.NodeThroughput, len(out))
	merge := func(m map[string]float64, set func(*port.NodeThroughput, float64)) {
		for node, v := range m {
			t := res[node]
			set(&t, v)
			res[node] = t
		}
	}
	merge(in, func(t *port.NodeThroughput, v float64) { t.In = v })
	merge(out, func(t *port.NodeThroughput, v float64) { t.Out = v })
	merge(errs, func(t *port.NodeThroughput, v float64) { t.Errors = v })
	merge(p95, func(t *port.NodeThroughput, v float64) {
		if v > 0 { // NaN/отрицательные от histogram_quantile при пустых бакетах игнорируем
			t.P95ms = v * 1000
		}
	})
	return res, nil
}

// NodeSeries — спарклайн входящего трафика per-node одним range-запросом
// (§22). Возвращает по buckets точек на узел; недостающие — нули.
func (c *Client) NodeSeries(ctx context.Context, since, until time.Time, buckets int) (map[string][]float64, error) {
	if buckets <= 0 {
		buckets = 12
	}
	window := until.Sub(since)
	step := window / time.Duration(buckets)
	if step <= 0 {
		step = time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	start := since
	end := until
	query := fmt.Sprintf(`sum by (node)(increase(nexus_requests_total{service="receiver"}[%s]))`, promRange(step))
	val, _, err := c.api.QueryRange(ctx, query, promv1.Range{Start: start, End: end, Step: step})
	if err != nil {
		return nil, fmt.Errorf("prometheus query_range %q: %w", query, err)
	}
	matrix, ok := val.(model.Matrix)
	if !ok {
		return nil, fmt.Errorf("prometheus query_range %q: unexpected result type %T", query, val)
	}
	res := make(map[string][]float64, len(matrix))
	for _, stream := range matrix {
		node := string(stream.Metric["node"])
		if node == "" {
			continue
		}
		pts := make([]float64, 0, len(stream.Values))
		for _, sp := range stream.Values {
			pts = append(pts, float64(sp.Value))
		}
		res[node] = pts
	}
	return res, nil
}

// NodeKPI — сводка одного узла (§21) за период (since, until] из per-node
// счётчиков Sender'а. total/errors — increase() по nexus_requests_total /
// nexus_request_incomplete_total; delivered = total-errors; p95/p99 — через
// histogram_quantile по бакетам длительности. Запрос на момент until.
func (c *Client) NodeKPI(ctx context.Context, node string, since, until time.Time) (port.NodeKPI, error) {
	w := promRange(until.Sub(since))
	lbl := promLabel(node)
	sel := fmt.Sprintf(`{service="sender",node="%s"}`, lbl)

	total, err := c.instantScalarAt(ctx,
		fmt.Sprintf(`sum(increase(nexus_requests_total%s[%s]))`, sel, w), until)
	if err != nil {
		return port.NodeKPI{}, err
	}
	errs, err := c.instantScalarAt(ctx,
		fmt.Sprintf(`sum(increase(nexus_request_incomplete_total%s[%s]))`, sel, w), until)
	if err != nil {
		return port.NodeKPI{}, err
	}
	p95, err := c.instantScalarAt(ctx,
		fmt.Sprintf(`histogram_quantile(0.95, sum by (le)(rate(nexus_request_duration_seconds_bucket%s[%s])))`, sel, w), until)
	if err != nil {
		return port.NodeKPI{}, err
	}
	p99, err := c.instantScalarAt(ctx,
		fmt.Sprintf(`histogram_quantile(0.99, sum by (le)(rate(nexus_request_duration_seconds_bucket%s[%s])))`, sel, w), until)
	if err != nil {
		return port.NodeKPI{}, err
	}

	tot := nonNegU64(total)
	er := nonNegU64(errs)
	delivered := uint64(0)
	if tot > er {
		delivered = tot - er
	}
	return port.NodeKPI{
		Total:     tot,
		Delivered: delivered,
		Errors:    er,
		P95ms:     quantileMs(p95),
		P99ms:     quantileMs(p99),
	}, nil
}

// NodeChart — ряд графика одного узла за период (since, until], buckets точек
// (ASC, недостающие — нули). Count — все исходящие вызовы, Errors —
// «незавершённые» (non-2xx). Два range-запроса (count/errors) с шагом step;
// значения раскладываются по бакетам позиционно.
func (c *Client) NodeChart(ctx context.Context, node string, since, until time.Time, buckets int) ([]port.SeriesPoint, error) {
	if buckets <= 0 {
		buckets = 48
	}
	if !until.After(since) {
		return nil, fmt.Errorf("invalid window: until <= since")
	}
	step := until.Sub(since) / time.Duration(buckets)
	if step <= 0 {
		step = time.Minute
	}
	lbl := promLabel(node)
	sel := fmt.Sprintf(`{service="sender",node="%s"}`, lbl)
	stepSel := promRange(step)

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	// Start = since+step → первая точка покрывает (since, since+step], последняя
	// (until-step, until]; ровно buckets интервалов в окне, как в прежнем CH-ряде.
	rng := promv1.Range{Start: since.Add(step), End: until, Step: step}
	counts, err := c.rangeSeriesValues(ctx,
		fmt.Sprintf(`sum(increase(nexus_requests_total%s[%s]))`, sel, stepSel), rng)
	if err != nil {
		return nil, err
	}
	errsSeries, err := c.rangeSeriesValues(ctx,
		fmt.Sprintf(`sum(increase(nexus_request_incomplete_total%s[%s]))`, sel, stepSel), rng)
	if err != nil {
		return nil, err
	}

	bucketMs := step.Milliseconds()
	sinceMs := since.UnixMilli()
	out := make([]port.SeriesPoint, buckets)
	for i := range out {
		out[i] = port.SeriesPoint{TsMs: sinceMs + int64(i)*bucketMs}
		if i < len(counts) {
			out[i].Count = nonNegU64(counts[i])
		}
		if i < len(errsSeries) {
			out[i].Errors = nonNegU64(errsSeries[i])
		}
	}
	return out, nil
}

// KafkaOverview — сводка Kafka-трафика за окно (since, until] (§4.1 spec).
// Async-трафик выделяется фильтром method="requestAsync"; счётчики — increase()
// за окно на момент until, lag/in-flight — instant nexus_kafka_lag, p95 —
// histogram_quantile по длительности async-обработки Sender'а.
func (c *Client) KafkaOverview(ctx context.Context, since, until time.Time) (port.KafkaSummary, error) {
	w := promRange(until.Sub(since))
	var s port.KafkaSummary
	q := func(query string) (float64, error) { return c.instantScalarAt(ctx, query, until) }

	var err error
	if s.Produced, err = q(fmt.Sprintf(
		`sum(increase(nexus_requests_total{service="receiver",method="requestAsync"}[%s]))`, w)); err != nil {
		return s, err
	}
	if s.FailedProduced, err = q(fmt.Sprintf(
		`sum(increase(nexus_requests_total{service="receiver",method="requestAsync",status=~"0|5.."}[%s]))`, w)); err != nil {
		return s, err
	}
	if s.Consumed, err = q(fmt.Sprintf(
		`sum(increase(nexus_requests_total{service="sender",method="requestAsync"}[%s]))`, w)); err != nil {
		return s, err
	}
	if s.FailedConsumed, err = q(fmt.Sprintf(
		`sum(increase(nexus_request_incomplete_total{method="requestAsync"}[%s]))`, w)); err != nil {
		return s, err
	}
	if s.CurrentLag, err = c.instantScalarAt(ctx, `sum(nexus_kafka_lag)`, until); err != nil {
		return s, err
	}
	// §31: in-flight — реальный gauge (fetched, не committed) из Sender.
	if s.InFlight, err = c.instantScalarAt(ctx, `sum(nexus_kafka_in_flight)`, until); err != nil {
		return s, err
	}
	// §31: produce p95 — реальная длительность публикации в Kafka.
	p95, err := q(fmt.Sprintf(
		`histogram_quantile(0.95, sum by (le)(rate(nexus_kafka_produce_duration_seconds_bucket[%s])))`, w))
	if err != nil {
		return s, err
	}
	s.ProduceP95ms = quantileMs(p95)
	return s, nil
}

// kafkaSeriesQuery — PromQL для одной метрики Kafka-ряда (§4.2 spec) с шагом
// step. produced/consumed/errors — rate() (сообщений/сек), lag — gauge как есть.
// Пустая строка → метрика неизвестна (пропускается вызывающим).
func kafkaSeriesQuery(metric, step string) string {
	switch metric {
	case "produced":
		return fmt.Sprintf(`sum(rate(nexus_requests_total{service="receiver",method="requestAsync"}[%s]))`, step)
	case "consumed":
		return fmt.Sprintf(`sum(rate(nexus_requests_total{service="sender",method="requestAsync"}[%s]))`, step)
	case "errors":
		return fmt.Sprintf(`sum(rate(nexus_request_incomplete_total{method="requestAsync"}[%s]))`, step)
	case "lag":
		return `sum(nexus_kafka_lag)`
	default:
		return ""
	}
}

// KafkaTimeseries — ряды produced/consumed/errors/lag за окно (since, until] с
// шагом step (§4.2 spec). По одному range-запросу на запрошенную метрику; точки
// — (начало бакета, значение) в порядке возрастания времени.
func (c *Client) KafkaTimeseries(ctx context.Context, since, until time.Time, step time.Duration, metrics []string) (map[string][]port.KafkaPoint, error) {
	if step <= 0 {
		step = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	rng := promv1.Range{Start: since, End: until, Step: step}
	stepSel := promRange(step)
	out := make(map[string][]port.KafkaPoint, len(metrics))
	for _, m := range metrics {
		query := kafkaSeriesQuery(m, stepSel)
		if query == "" {
			continue
		}
		pts, err := c.rangeSeriesPoints(ctx, query, rng)
		if err != nil {
			return nil, err
		}
		out[m] = pts
	}
	return out, nil
}

// rangeSeriesPoints выполняет range-query со скалярным агрегатом (sum(...) →
// одна серия) и возвращает точки (ts, value) в порядке возрастания времени.
// Пустой результат → nil без ошибки.
func (c *Client) rangeSeriesPoints(ctx context.Context, query string, rng promv1.Range) ([]port.KafkaPoint, error) {
	val, _, err := c.api.QueryRange(ctx, query, rng)
	if err != nil {
		return nil, fmt.Errorf("prometheus query_range %q: %w", query, err)
	}
	matrix, ok := val.(model.Matrix)
	if !ok {
		return nil, fmt.Errorf("prometheus query_range %q: unexpected result type %T", query, val)
	}
	if len(matrix) == 0 {
		return nil, nil
	}
	pts := make([]port.KafkaPoint, 0, len(matrix[0].Values))
	for _, sp := range matrix[0].Values {
		v := float64(sp.Value)
		if math.IsNaN(v) || v < 0 {
			v = 0
		}
		pts = append(pts, port.KafkaPoint{TsMs: int64(sp.Timestamp), V: v})
	}
	return pts, nil
}

// rangeSeriesValues выполняет range-query со скалярным агрегатом (sum(...) →
// одна серия) и возвращает её значения в порядке возрастания времени. Пустой
// результат (нет данных за окно) → nil без ошибки.
func (c *Client) rangeSeriesValues(ctx context.Context, query string, rng promv1.Range) ([]float64, error) {
	val, _, err := c.api.QueryRange(ctx, query, rng)
	if err != nil {
		return nil, fmt.Errorf("prometheus query_range %q: %w", query, err)
	}
	matrix, ok := val.(model.Matrix)
	if !ok {
		return nil, fmt.Errorf("prometheus query_range %q: unexpected result type %T", query, val)
	}
	if len(matrix) == 0 {
		return nil, nil
	}
	vals := make([]float64, 0, len(matrix[0].Values))
	for _, sp := range matrix[0].Values {
		vals = append(vals, float64(sp.Value))
	}
	return vals, nil
}
