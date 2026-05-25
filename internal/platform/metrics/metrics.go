// Package metrics — Prometheus-метрики DataBus (§6 ТЗ).
//
// Метрики не лежат в глобальной registry: каждый сервис создаёт свой
// экземпляр *Metrics в bootstrap'е и передаёт в middleware/usecase
// через DI. Это исключает кросс-сервисное «протекание» и упрощает unit-тесты.
//
// Минимальный набор (§6):
//   - databus_requests_total{service, method, node, status}
//   - databus_request_duration_seconds{service, method, node}
//   - databus_kafka_lag{topic, partition, group}
//   - databus_clickhouse_buffer_size{table}
//   - databus_clickhouse_errors_total{table, op}
//
// Дополнительно (для observability сборщика логов):
//   - databus_clickhouse_dropped_total{table, reason}
//   - databus_clickhouse_fallback_total{table, op}
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics — контейнер всех Prometheus-метрик одного сервиса.
//
// service — статическая метка (receiver/sender/web), проставляется
// в счётчиках через ConstLabels конструктора.
type Metrics struct {
	registry *prometheus.Registry
	service  string

	RequestsTotal       *prometheus.CounterVec
	RequestDuration     *prometheus.HistogramVec
	KafkaLag            *prometheus.GaugeVec
	CHBufferSize        *prometheus.GaugeVec
	CHErrorsTotal       *prometheus.CounterVec
	CHDroppedTotal      *prometheus.CounterVec
	CHFallbackTotal     *prometheus.CounterVec
}

// New создаёт новый экземпляр Metrics для указанного сервиса.
//
// В собственный реестр регистрируются Go-runtime и Process collectors,
// чтобы /metrics показывал стандартные `go_*` и `process_*` ряды.
func New(service string) *Metrics {
	reg := prometheus.NewRegistry()
	constLabels := prometheus.Labels{"service": service}

	m := &Metrics{
		registry: reg,
		service:  service,

		RequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name:        "databus_requests_total",
			Help:        "Total DataBus requests by method (request/requestAsync), node path and resulting HTTP status.",
			ConstLabels: constLabels,
		}, []string{"method", "node", "status"}),

		RequestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:        "databus_request_duration_seconds",
			Help:        "DataBus request duration in seconds (end-to-end for the given service).",
			ConstLabels: constLabels,
			Buckets:     prometheus.DefBuckets,
		}, []string{"method", "node"}),

		KafkaLag: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name:        "databus_kafka_lag",
			Help:        "Kafka consumer lag in messages per topic/partition for the configured consumer group.",
			ConstLabels: constLabels,
		}, []string{"topic", "partition", "group"}),

		CHBufferSize: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name:        "databus_clickhouse_buffer_size",
			Help:        "ClickHouse batch writer pending rows per table.",
			ConstLabels: constLabels,
		}, []string{"table"}),

		CHErrorsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name:        "databus_clickhouse_errors_total",
			Help:        "ClickHouse batch writer errors by table and operation (insert, prepare, fallback).",
			ConstLabels: constLabels,
		}, []string{"table", "op"}),

		CHDroppedTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name:        "databus_clickhouse_dropped_total",
			Help:        "ClickHouse log records dropped (e.g. due to full buffer or missing table).",
			ConstLabels: constLabels,
		}, []string{"table", "reason"}),

		CHFallbackTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name:        "databus_clickhouse_fallback_total",
			Help:        "ClickHouse file-fallback batch outcomes (saved, restored).",
			ConstLabels: constLabels,
		}, []string{"table", "op"}),
	}

	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.RequestsTotal,
		m.RequestDuration,
		m.KafkaLag,
		m.CHBufferSize,
		m.CHErrorsTotal,
		m.CHDroppedTotal,
		m.CHFallbackTotal,
	)
	return m
}

// Registry возвращает собственный prometheus.Registry — для тестов или
// дополнительных кастомных collectors.
func (m *Metrics) Registry() *prometheus.Registry { return m.registry }

// Handler — http.Handler для эндпоинта /metrics, изолированный от default registry.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{Registry: m.registry})
}
