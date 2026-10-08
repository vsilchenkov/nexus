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
	api      promv1.API
	timeout  time.Duration
	logger   logging.Logger
	lagQuery string
}

var _ port.PromMetrics = (*Client)(nil)

// Option настраивает Client.
type Option func(*Client)

// WithKafkaMainGroup ограничивает lag (KPI «Очередь Kafka», сводка и ряд
// страницы Kafka) основной consumer-группой (§113.4). Очереди узлов на паузе,
// DLQ и повторов записи логов — отдельные группы, их отставание означает
// отложенную работу, а не застрявший Sender, и показывается отдельно.
func WithKafkaMainGroup(group string) Option {
	return func(c *Client) {
		if group != "" {
			c.lagQuery = kafkaLagQuery(fmt.Sprintf(`{group="%s"}`, promLabel(group)))
		}
	}
}

// kafkaLagQuery — суммарный lag по селектору. Sender публикует lag из брокера
// (§113.4), поэтому каждая реплика отдаёт одно и то же значение на партицию:
// max по репликам вместо sum, иначе две реплики удвоили бы очередь.
func kafkaLagQuery(selector string) string {
	return fmt.Sprintf(`sum(max by (group, topic, partition) (nexus_kafka_lag%s))`, selector)
}

// New создаёт клиент к Prometheus по адресу url (напр. http://prometheus:9090).
// Пустой url — ошибка: вызывающая сторона (app.go) не должна создавать клиент,
// если prometheus.url не задан.
//
// Без WithKafkaMainGroup lag считается по всем группам.
func New(url string, timeout time.Duration, logger logging.Logger, opts ...Option) (*Client, error) {
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
	cl := &Client{api: promv1.NewAPI(c), timeout: timeout, logger: logger, lagQuery: kafkaLagQuery("")}
	for _, o := range opts {
		o(cl)
	}
	return cl, nil
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

// promLabel экранирует значение метки для PromQL-селектора (двойные кавычки).
func promLabel(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

// teamMatcher — дополнительный матчер метки team внутри селектора (§101.9):
// пустой слог фильтра не добавляет.
func teamMatcher(team string) string {
	if team == "" {
		return ""
	}
	return fmt.Sprintf(`,team="%s"`, promLabel(team))
}

// quantileMs нормализует результат histogram_quantile (секунды) в мс:
// NaN/отрицательные (пустые бакеты) → 0.
func quantileMs(v float64) float64 {
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	return v * 1000
}

// instantVector выполняет instant-query на момент at и ждёт вектор.
func (c *Client) instantVector(ctx context.Context, query string, at time.Time) (model.Vector, error) {
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
	return vec, nil
}

// instantByLabel выполняет instant-query вида `sum by (<label>)(...)` на момент
// at и собирает результат в map по значению метки label. Сэмплы без метки
// пропускаются (агрегат без группировки не имеет отношения к ключу).
func (c *Client) instantByLabel(ctx context.Context, query, label string, at time.Time) (map[string]float64, error) {
	vec, err := c.instantVector(ctx, query, at)
	if err != nil {
		return nil, err
	}
	out := make(map[string]float64, len(vec))
	for _, s := range vec {
		key := string(s.Metric[model.LabelName(label)])
		if key == "" {
			continue
		}
		out[key] += float64(s.Value)
	}
	return out, nil
}

// nodeKeyOf — ключ узла по меткам ряда (§101.9). Ряд без метки node — выход
// до резолва узла (400/405/413/429, §101.3): к узлу он отношения не имеет.
func nodeKeyOf(m model.Metric) (port.NodeKey, bool) {
	path := string(m["node"])
	if path == "" {
		return port.NodeKey{}, false
	}
	return port.NodeKey{Team: string(m["team"]), Path: path}, true
}

// instantByNodeKey выполняет instant-query вида `… by (team, node)(…)` на
// момент at и собирает результат по паре меток.
func (c *Client) instantByNodeKey(ctx context.Context, query string, at time.Time) (map[port.NodeKey]float64, error) {
	vec, err := c.instantVector(ctx, query, at)
	if err != nil {
		return nil, err
	}
	out := make(map[port.NodeKey]float64, len(vec))
	for _, s := range vec {
		if key, ok := nodeKeyOf(s.Metric); ok {
			out[key] += float64(s.Value)
		}
	}
	return out, nil
}

// senderOut — матчер исходящих Sender'а без откладываний из-за паузы (§113.5):
// status="paused" — сообщение не отправлено, а переложено в очередь паузы. До
// §113 такие переносы шли в счётчик каждые 30 с и раздували consumed/outgoing в
// сотни раз; фильтр в запросе исправляет и уже накопленную историю.
const senderOut = `service="sender",status!="paused"`

// GlobalTotals — incoming (receiver) / outgoing (sender) / errors (sender 0|4xx|5xx).
// Непустой team сужает суммы до рядов команды (§101.9).
func (c *Client) GlobalTotals(ctx context.Context, window time.Duration, team string) (port.GlobalTotals, error) {
	w := promRange(window)
	tm := teamMatcher(team)
	var t port.GlobalTotals
	var err error
	if t.Incoming, err = c.instantScalar(ctx,
		fmt.Sprintf(`sum(increase(nexus_requests_total{service="receiver"%s}[%s]))`, tm, w)); err != nil {
		return t, err
	}
	if t.Outgoing, err = c.instantScalar(ctx,
		fmt.Sprintf(`sum(increase(nexus_requests_total{%s%s}[%s]))`, senderOut, tm, w)); err != nil {
		return t, err
	}
	if t.Errors, err = c.instantScalar(ctx,
		fmt.Sprintf(`sum(increase(nexus_requests_total{service="sender",status=~"0|[45].."%s}[%s]))`, tm, w)); err != nil {
		return t, err
	}
	return t, nil
}

// KafkaQueue — суммарный lag основной группы по всем партициям (мгновенно).
func (c *Client) KafkaQueue(ctx context.Context) (float64, error) {
	return c.instantScalar(ctx, c.lagQuery)
}

// NodeErrors — per-node число «незавершённых» вызовов (done=0) за окно
// из nexus_request_incomplete_total (§22, Telegram-алерты).
func (c *Client) NodeErrors(ctx context.Context, window time.Duration) (map[port.NodeKey]float64, error) {
	return c.instantByNodeKey(ctx,
		fmt.Sprintf(`sum by (team, node)(increase(nexus_request_incomplete_total{service="sender"}[%s]))`,
			promRange(window)), time.Now())
}

// NodeThroughput — per-node in/out/errors за период (since, until] (ключ —
// пара меток team и node, §101.9). Окно = until-since, запрос вычисляется на
// момент until (поддержка произвольного календарного периода, §28 Пункт 4).
func (c *Client) NodeThroughput(ctx context.Context, since, until time.Time) (map[port.NodeKey]port.NodeThroughput, error) {
	return c.nodeThroughput(ctx, since, until, "")
}

// KafkaNodeThroughput — то же, что NodeThroughput, только по async-трафику
// (method="requestAsync", §113.5): «Топ продюсеров» страницы Kafka. Без
// фильтра туда попадал sync-трафик, которого в Kafka нет.
func (c *Client) KafkaNodeThroughput(ctx context.Context, since, until time.Time) (map[port.NodeKey]port.NodeThroughput, error) {
	return c.nodeThroughput(ctx, since, until, `,method="requestAsync"`)
}

// NodeFailures — исходящие и ошибки Sender'а по узлам за период по всему
// трафику: «Top failures» страницы Kafka показывает и sync-узлы. Фильтра
// method="requestAsync" здесь нет намеренно: в 1.41.0 блок брал async-срез и
// пустел при живых ошибках sync-узлов.
func (c *Client) NodeFailures(ctx context.Context, since, until time.Time) (map[port.NodeKey]port.NodeThroughput, error) {
	out, errs, err := c.senderOutErrors(ctx, promRange(until.Sub(since)), until, "")
	if err != nil {
		return nil, err
	}
	return mergeThroughput(nil, out, errs, nil), nil
}

// nodeThroughput собирает per-node счётчики; mm — дополнительный матчер
// (",method=..." или пусто).
func (c *Client) nodeThroughput(ctx context.Context, since, until time.Time, mm string) (map[port.NodeKey]port.NodeThroughput, error) {
	w := promRange(until.Sub(since))
	in, err := c.instantByNodeKey(ctx,
		fmt.Sprintf(`sum by (team, node)(increase(nexus_requests_total{service="receiver"%s}[%s]))`, mm, w), until)
	if err != nil {
		return nil, err
	}
	out, errs, err := c.senderOutErrors(ctx, w, until, mm)
	if err != nil {
		return nil, err
	}
	// p95 длительности исходящих (Sender) по узлам — гистограмма уже пишется
	// и sync, и async (§22, новой метрики не нужно). Значение в секундах → мс.
	p95, err := c.instantByNodeKey(ctx,
		fmt.Sprintf(`histogram_quantile(0.95, sum by (team, node, le)(rate(nexus_request_duration_seconds_bucket{service="sender"%s}[%s])))`, mm, w), until)
	if err != nil {
		return nil, err
	}
	return mergeThroughput(in, out, errs, p95), nil
}

// senderOutErrors — per-node исходящие Sender'а (без откладываний из-за паузы)
// и ошибки (0|4xx|5xx) за окно w на момент at; mm — дополнительный матчер.
func (c *Client) senderOutErrors(ctx context.Context, w string, at time.Time, mm string) (out, errs map[port.NodeKey]float64, err error) {
	out, err = c.instantByNodeKey(ctx,
		fmt.Sprintf(`sum by (team, node)(increase(nexus_requests_total{%s%s}[%s]))`, senderOut, mm, w), at)
	if err != nil {
		return nil, nil, err
	}
	errs, err = c.instantByNodeKey(ctx,
		fmt.Sprintf(`sum by (team, node)(increase(nexus_requests_total{service="sender",status=~"0|[45].."%s}[%s]))`, mm, w), at)
	if err != nil {
		return nil, nil, err
	}
	return out, errs, nil
}

// mergeThroughput складывает ряды in/out/errors/p95 в строки по узлу.
func mergeThroughput(in, out, errs, p95 map[port.NodeKey]float64) map[port.NodeKey]port.NodeThroughput {

	res := make(map[port.NodeKey]port.NodeThroughput, len(out))
	merge := func(m map[port.NodeKey]float64, set func(*port.NodeThroughput, float64)) {
		for key, v := range m {
			t := res[key]
			set(&t, v)
			res[key] = t
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
	return res
}

// NodeLastErrors — per-node исход последнего исходящего вызова (§41, «Down»):
// instant gauge nexus_node_last_request_error на момент at. max by (team, node) —
// при нескольких репликах Sender узел «Down», если у любой последний вызов был
// ошибкой. Окно не используется (gauge мгновенный).
func (c *Client) NodeLastErrors(ctx context.Context, at time.Time) (map[port.NodeKey]float64, error) {
	return c.instantByNodeKey(ctx,
		`max by (team, node)(nexus_node_last_request_error{service="sender"})`, at)
}

// NodeSeries — спарклайн входящего трафика per-node одним range-запросом
// (§22). Возвращает по buckets точек на узел; недостающие — нули.
func (c *Client) NodeSeries(ctx context.Context, since, until time.Time, buckets int) (map[port.NodeKey][]float64, error) {
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
	query := fmt.Sprintf(`sum by (team, node)(increase(nexus_requests_total{service="receiver"}[%s]))`, promRange(step))
	val, _, err := c.api.QueryRange(ctx, query, promv1.Range{Start: start, End: end, Step: step})
	if err != nil {
		return nil, fmt.Errorf("prometheus query_range %q: %w", query, err)
	}
	matrix, ok := val.(model.Matrix)
	if !ok {
		return nil, fmt.Errorf("prometheus query_range %q: unexpected result type %T", query, val)
	}
	res := make(map[port.NodeKey][]float64, len(matrix))
	for _, stream := range matrix {
		key, ok := nodeKeyOf(stream.Metric)
		if !ok {
			continue
		}
		pts := make([]float64, 0, len(stream.Values))
		for _, sp := range stream.Values {
			pts = append(pts, float64(sp.Value))
		}
		res[key] = pts
	}
	return res, nil
}

// KafkaOverview — сводка Kafka-трафика за окно (since, until] (§4.1 spec).
// Async-трафик выделяется фильтром method="requestAsync"; счётчики — increase()
// за окно на момент until, lag (основной группы)/in-flight — instant, p95 —
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
		`sum(increase(nexus_requests_total{%s,method="requestAsync"}[%s]))`, senderOut, w)); err != nil {
		return s, err
	}
	if s.FailedConsumed, err = q(fmt.Sprintf(
		`sum(increase(nexus_request_incomplete_total{method="requestAsync"}[%s]))`, w)); err != nil {
		return s, err
	}
	if s.CurrentLag, err = c.instantScalarAt(ctx, c.lagQuery, until); err != nil {
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

// KafkaTopicSizes — размер топиков на дисках кластера, байт (§75). Источник —
// JMX-агент на брокере (kafka_log_log_size на партицию); суммирование по метке
// topic складывает партиции И их реплики.
//
// Нулевые значения СОХРАНЯЮТСЯ: у пустого топика размер честно равен нулю, и
// выбросить такую серию значило бы приравнять «топик пуст» к «источника нет» —
// на свежем кластере, где пусты все топики, это выдало бы работающий экспортёр
// за ненастроенный (наличие ключа = серия пришла, см. SizesAvailable в usecase).
// Отбрасываются только NaN и отрицательные: у gauge их быть не может, но int64
// ушёл бы в UI отрицательным размером.
//
// Метрики нет вовсе, пока на брокере не поднят JMX-агент (или Kafka чужая):
// это не ошибка, а пустая карта — «—» в колонке «Размер».
func (c *Client) KafkaTopicSizes(ctx context.Context) (map[string]int64, error) {
	raw, err := c.instantByLabel(ctx, `sum by (topic)(kafka_log_log_size)`, "topic", time.Now())
	if err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(raw))
	for topic, v := range raw {
		if math.IsNaN(v) || v < 0 {
			continue
		}
		out[topic] = int64(v + 0.5)
	}
	return out, nil
}

// kafkaSeriesQuery — PromQL для одной метрики Kafka-ряда (§4.2 spec) с шагом
// step. produced/consumed/errors — rate() (сообщений/сек), lag — gauge как есть
// (lagQuery клиента). Пустая строка → метрика неизвестна (пропускается вызывающим).
func kafkaSeriesQuery(metric, step, lagQuery string) string {
	switch metric {
	case "produced":
		return fmt.Sprintf(`sum(rate(nexus_requests_total{service="receiver",method="requestAsync"}[%s]))`, step)
	case "consumed":
		return fmt.Sprintf(`sum(rate(nexus_requests_total{%s,method="requestAsync"}[%s]))`, senderOut, step)
	case "errors":
		return fmt.Sprintf(`sum(rate(nexus_request_incomplete_total{method="requestAsync"}[%s]))`, step)
	case "lag":
		return lagQuery
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
		query := kafkaSeriesQuery(m, stepSel, c.lagQuery)
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
