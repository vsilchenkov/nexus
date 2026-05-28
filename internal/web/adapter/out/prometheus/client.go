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

// instantScalar выполняет instant-query, ожидая скалярный результат
// (sum(...) → vector из одного элемента). Пустой vector → 0.
func (c *Client) instantScalar(ctx context.Context, query string) (float64, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	val, _, err := c.api.Query(ctx, query, time.Now())
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

// instantByNode выполняет instant-query вида `sum by (node)(...)` и собирает
// результат в map по метке node.
func (c *Client) instantByNode(ctx context.Context, query string) (map[string]float64, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	val, _, err := c.api.Query(ctx, query, time.Now())
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

// NodeThroughput — per-node in/out/errors за окно (ключ — метка node = path).
func (c *Client) NodeThroughput(ctx context.Context, window time.Duration) (map[string]port.NodeThroughput, error) {
	w := promRange(window)
	in, err := c.instantByNode(ctx,
		fmt.Sprintf(`sum by (node)(increase(nexus_requests_total{service="receiver"}[%s]))`, w))
	if err != nil {
		return nil, err
	}
	out, err := c.instantByNode(ctx,
		fmt.Sprintf(`sum by (node)(increase(nexus_requests_total{service="sender"}[%s]))`, w))
	if err != nil {
		return nil, err
	}
	errs, err := c.instantByNode(ctx,
		fmt.Sprintf(`sum by (node)(increase(nexus_requests_total{service="sender",status=~"0|[45].."}[%s]))`, w))
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
	return res, nil
}
