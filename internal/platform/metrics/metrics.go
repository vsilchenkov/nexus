// Package metrics — Prometheus-метрики Nexus (§6 ТЗ).
//
// Метрики не лежат в глобальной registry: каждый сервис создаёт свой
// экземпляр *Metrics в bootstrap'е и передаёт в middleware/usecase
// через DI. Это исключает кросс-сервисное «протекание» и упрощает unit-тесты.
//
// Минимальный набор (§6):
//   - nexus_requests_total{service, method, node, status}
//   - nexus_request_duration_seconds{service, method, node}
//   - nexus_kafka_lag{topic, partition, group}
//   - nexus_clickhouse_buffer_size{table}
//   - nexus_clickhouse_errors_total{table, op}
//
// Дополнительно (для observability сборщика логов):
//   - nexus_clickhouse_dropped_total{table, reason}
//   - nexus_clickhouse_fallback_total{table, op}
//
// Receiver L2-кеш узлов (§9.2):
//   - nexus_l2_cache_hits_total{kind}     # kind=fresh|stale
//   - nexus_l2_cache_misses_total
//   - nexus_l2_cache_evictions_total
//   - nexus_l2_cache_size
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

	RequestsTotal           *prometheus.CounterVec
	RequestsIncompleteTotal *prometheus.CounterVec
	LoopDetectedTotal       *prometheus.CounterVec // §32: запросы, отклонённые по hop-лимиту
	RequestDuration         *prometheus.HistogramVec
	KafkaLag                *prometheus.GaugeVec
	KafkaInFlight           *prometheus.GaugeVec     // §31: сообщения «в полёте» (fetched, не committed)
	KafkaProduceDuration    *prometheus.HistogramVec // §31: длительность публикации в Kafka по топику
	CHBufferSize            *prometheus.GaugeVec
	CHErrorsTotal           *prometheus.CounterVec
	CHDroppedTotal          *prometheus.CounterVec
	CHFallbackTotal         *prometheus.CounterVec

	L2CacheHits      *prometheus.CounterVec
	L2CacheMisses    prometheus.Counter
	L2CacheEvictions prometheus.Counter
	L2CacheSize      prometheus.Gauge

	// §27: узел RabbitMQAsync (Puller-воркер в Receiver).
	RMQMessagesPulledTotal *prometheus.CounterVec // {node, status=ok|nack|reject}
	RMQPullDuration        *prometheus.HistogramVec
	RMQConnectionState     *prometheus.GaugeVec // {node}: 0=down,1=connecting,2=up
	RMQQueueDepth          *prometheus.GaugeVec // {node}
	RMQConsumerCount       *prometheus.GaugeVec // {node}
	NodeDegraded           *prometheus.GaugeVec // {node, reason}: 1 при degraded
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
			Name:        "nexus_requests_total",
			Help:        "Total Nexus requests by method (request/requestAsync), node path and resulting HTTP status.",
			ConstLabels: constLabels,
		}, []string{"method", "node", "status"}),

		// §22: «незавершённые» исходящие вызовы Sender (done=0): статус не 2xx —
		// сетевой сбой (0), 3xx/4xx/5xx, circuit-breaker. Единый сигнал для
		// Telegram-алертов, эквивалентный ClickHouse-условию CountErrors.
		RequestsIncompleteTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name:        "nexus_request_incomplete_total",
			Help:        "Sender outbound calls that did not complete successfully (non-2xx) by method and node path.",
			ConstLabels: constLabels,
		}, []string{"method", "node"}),

		// §32: запросы, отклонённые защитой от зацикливания (превышен hop-лимит
		// X-Nexus-Hops). mode=sync|async — путь, на котором сработала защита.
		LoopDetectedTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name:        "nexus_loop_detected_total",
			Help:        "Requests rejected by the loop-protection hop limit (X-Nexus-Hops), by mode (sync, async).",
			ConstLabels: constLabels,
		}, []string{"mode"}),

		RequestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:        "nexus_request_duration_seconds",
			Help:        "Nexus request duration in seconds (end-to-end for the given service).",
			ConstLabels: constLabels,
			// DefBuckets упираются в 10с, а timeout_ms узла — до 300с. Расширяем
			// верх диапазона, чтобы histogram_quantile (p95/p99 per-node, §21)
			// не «прилипал» к +Inf на медленных узлах.
			Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 120, 300},
		}, []string{"method", "node"}),

		KafkaLag: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name:        "nexus_kafka_lag",
			Help:        "Kafka consumer lag in messages per topic/partition for the configured consumer group.",
			ConstLabels: constLabels,
		}, []string{"topic", "partition", "group"}),

		// §31: сообщения, прочитанные consumer'ом, но ещё не закоммиченные
		// (в обработке между produce и consume). Метка component=sender.
		KafkaInFlight: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name:        "nexus_kafka_in_flight",
			Help:        "Kafka messages fetched but not yet committed (in-flight between produce and consume).",
			ConstLabels: constLabels,
		}, []string{"component"}),

		// §31: время публикации одного сообщения в Kafka (WriteMessages) по топику.
		KafkaProduceDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:        "nexus_kafka_produce_duration_seconds",
			Help:        "Kafka produce duration in seconds per topic (Receiver/Sender producer).",
			ConstLabels: constLabels,
			Buckets:     []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5},
		}, []string{"topic"}),

		CHBufferSize: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name:        "nexus_clickhouse_buffer_size",
			Help:        "ClickHouse batch writer pending rows per table.",
			ConstLabels: constLabels,
		}, []string{"table"}),

		CHErrorsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name:        "nexus_clickhouse_errors_total",
			Help:        "ClickHouse batch writer errors by table and operation (insert, prepare, fallback).",
			ConstLabels: constLabels,
		}, []string{"table", "op"}),

		CHDroppedTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name:        "nexus_clickhouse_dropped_total",
			Help:        "ClickHouse log records dropped (e.g. due to full buffer or missing table).",
			ConstLabels: constLabels,
		}, []string{"table", "reason"}),

		CHFallbackTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name:        "nexus_clickhouse_fallback_total",
			Help:        "ClickHouse file-fallback batch outcomes (saved, restored).",
			ConstLabels: constLabels,
		}, []string{"table", "op"}),

		L2CacheHits: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name:        "nexus_l2_cache_hits_total",
			Help:        "Receiver L2 in-memory node cache hits, split by kind (fresh, stale).",
			ConstLabels: constLabels,
		}, []string{"kind"}),

		L2CacheMisses: prometheus.NewCounter(prometheus.CounterOpts{
			Name:        "nexus_l2_cache_misses_total",
			Help:        "Receiver L2 in-memory node cache misses (delegated to Redis/Postgres).",
			ConstLabels: constLabels,
		}),

		L2CacheEvictions: prometheus.NewCounter(prometheus.CounterOpts{
			Name:        "nexus_l2_cache_evictions_total",
			Help:        "Receiver L2 in-memory node cache evictions due to capacity overflow.",
			ConstLabels: constLabels,
		}),

		L2CacheSize: prometheus.NewGauge(prometheus.GaugeOpts{
			Name:        "nexus_l2_cache_size",
			Help:        "Receiver L2 in-memory node cache current entry count.",
			ConstLabels: constLabels,
		}),

		RMQMessagesPulledTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name:        "nexus_rmq_messages_pulled_total",
			Help:        "RabbitMQAsync messages pulled by node and outcome (ok, nack, reject).",
			ConstLabels: constLabels,
		}, []string{"node", "status"}),

		RMQPullDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:        "nexus_rmq_pull_duration_seconds",
			Help:        "RabbitMQAsync batch pull+publish duration in seconds by node.",
			ConstLabels: constLabels,
			Buckets:     prometheus.DefBuckets,
		}, []string{"node"}),

		RMQConnectionState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name:        "nexus_rmq_connection_state",
			Help:        "RabbitMQAsync connection state by node: 0=down, 1=connecting, 2=up.",
			ConstLabels: constLabels,
		}, []string{"node"}),

		RMQQueueDepth: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name:        "nexus_rmq_queue_depth",
			Help:        "RabbitMQAsync source queue depth (messages) by node, sampled each tick.",
			ConstLabels: constLabels,
		}, []string{"node"}),

		RMQConsumerCount: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name:        "nexus_rmq_consumer_count",
			Help:        "RabbitMQAsync source queue consumer count by node.",
			ConstLabels: constLabels,
		}, []string{"node"}),

		NodeDegraded: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name:        "nexus_node_degraded",
			Help:        "Pull node (RabbitMQAsync) degraded state: 1 when degraded, 0 otherwise.",
			ConstLabels: constLabels,
		}, []string{"node", "reason"}),
	}

	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.RequestsTotal,
		m.RequestsIncompleteTotal,
		m.LoopDetectedTotal,
		m.RequestDuration,
		m.KafkaLag,
		m.KafkaInFlight,
		m.KafkaProduceDuration,
		m.CHBufferSize,
		m.CHErrorsTotal,
		m.CHDroppedTotal,
		m.CHFallbackTotal,
		m.L2CacheHits,
		m.L2CacheMisses,
		m.L2CacheEvictions,
		m.L2CacheSize,
		m.RMQMessagesPulledTotal,
		m.RMQPullDuration,
		m.RMQConnectionState,
		m.RMQQueueDepth,
		m.RMQConsumerCount,
		m.NodeDegraded,
	)
	return m
}

// IncL2Hit реализует nodecache.L2Metrics: счётчик попаданий в L2-кеш.
// kind — "fresh" (валидная запись) либо "stale" (после fallback'а при ошибке downstream).
func (m *Metrics) IncL2Hit(kind string) {
	m.L2CacheHits.WithLabelValues(kind).Inc()
}

// IncL2Miss инкрементит счётчик промахов L2-кеша.
func (m *Metrics) IncL2Miss() { m.L2CacheMisses.Inc() }

// IncL2Eviction инкрементит счётчик вытеснений из L2-кеша.
func (m *Metrics) IncL2Eviction() { m.L2CacheEvictions.Inc() }

// SetL2Size обновляет текущий размер L2-кеша.
func (m *Metrics) SetL2Size(n int) { m.L2CacheSize.Set(float64(n)) }

// IncLoopDetected инкрементит счётчик запросов, отклонённых защитой от
// зацикливания (§32). mode — "sync" либо "async".
func (m *Metrics) IncLoopDetected(mode string) { m.LoopDetectedTotal.WithLabelValues(mode).Inc() }

// Registry возвращает собственный prometheus.Registry — для тестов или
// дополнительных кастомных collectors.
func (m *Metrics) Registry() *prometheus.Registry { return m.registry }

// Handler — http.Handler для эндпоинта /metrics, изолированный от default registry.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{Registry: m.registry})
}
