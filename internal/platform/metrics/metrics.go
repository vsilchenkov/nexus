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

	"nexus/internal/domain"
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
	// §41 («Down»): исход ПОСЛЕДНЕГО исходящего вызова Sender по узлу — 1 если
	// последний вызов завершился ошибкой (status 0/4xx/5xx), 0 если 2xx. Overview
	// определяет «Down» по этой метрике (снимок «сейчас»), а не по доле ошибок за
	// период. Caveat мульти-реплик: каждая держит своё значение, Web берёт
	// max by(node).
	NodeLastRequestError *prometheus.GaugeVec
	LoopDetectedTotal    *prometheus.CounterVec // §32: запросы, отклонённые по hop-лимиту
	// AsyncIngressRejectedTotal — §82.3: запросы во внешний /requestAsync к узлу,
	// который настроен не как requestAsync. Клиент видит только 404, поэтому без
	// счётчика рассинхронизация настроек остаётся невидимой.
	//
	// Метка node — путь ИЗ АДРЕСА, как у RequestsTotal (та же конвенция). Счётчик
	// растёт только после успешного резолва узла, поэтому кардинальность
	// ограничена числом узлов — кроме узлов с path_passthrough, где в путь входит
	// произвольный хвост запроса. Экспозиция та же, что у RequestsTotal{node}, и
	// новым классом риска не является; точный узел ищи в warn'е receiver.async —
	// там резолвнутый path и node_id.
	AsyncIngressRejectedTotal *prometheus.CounterVec
	// Phase AUD.8: сбои проверки rate-limit'а (fail-open, §9.4) по scope ключа.
	RateLimitCheckErrorsTotal *prometheus.CounterVec
	RequestDuration           *prometheus.HistogramVec
	KafkaLag                  *prometheus.GaugeVec
	KafkaInFlight             *prometheus.GaugeVec     // §31: сообщения «в полёте» (fetched, не committed)
	KafkaProduceDuration      *prometheus.HistogramVec // §31: длительность публикации в Kafka по топику
	CHBufferSize              *prometheus.GaugeVec
	CHErrorsTotal             *prometheus.CounterVec
	CHDroppedTotal            *prometheus.CounterVec
	CHFallbackTotal           *prometheus.CounterVec

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

	// §36: авто-репроцессор DLQ (повторная доставка неудачных async-сообщений).
	DLQReprocessTotal    *prometheus.CounterVec // {node, result=succeeded|failed|ttl_dropped|skipped|dropped}
	DLQReprocessDuration prometheus.Histogram   // длительность одного прохода sweeper'а (секунды)

	// §74.3: состояние схемы PostgreSQL относительно кода сервиса.
	PGSchemaVersion prometheus.Gauge // версия схемы в БД (0 = миграций не было)
	PGSchemaAhead   prometheus.Gauge // 1 = схема новее каталога миграций бинаря
}

// Option — функциональная опция конструктора New.
type Option func(prometheus.Labels)

// WithInstance добавляет постоянную метку идентификатора ноды (§70.7).
//
// Имя метки — `nexus_instance`, а НЕ `instance`: последнюю проставляет сам
// Prometheus при скрейпе (адрес цели), и одноимённая метка приложения была бы
// переименована в `exported_instance` — то есть фильтры по ней молча не
// работали бы.
//
// Пустой идентификатор метку не добавляет: она входит в идентичность ряда, и её
// появление на действующей ноде разорвало бы историю всех графиков.
func WithInstance(id string) Option {
	return func(l prometheus.Labels) {
		if id != "" {
			l["nexus_instance"] = id
		}
	}
}

// New создаёт новый экземпляр Metrics для указанного сервиса.
//
// В собственный реестр регистрируются Go-runtime и Process collectors,
// чтобы /metrics показывал стандартные `go_*` и `process_*` ряды.
//
// Длина функции осознанна: это объявление всех рядов подряд, без логики.
// Дробление по подсистемам развело бы объявление метрики и её регистрацию в
// reg.MustRegister по разным функциям — а именно рассинхрон этих двух списков
// и есть типичная ошибка здесь (ряд объявлен, но не зарегистрирован).
func New(service string, opts ...Option) *Metrics {
	reg := prometheus.NewRegistry()
	constLabels := prometheus.Labels{"service": service}
	for _, o := range opts {
		o(constLabels)
	}

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

		// §41 («Down»): см. комментарий у поля NodeLastRequestError.
		NodeLastRequestError: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name:        "nexus_node_last_request_error",
			Help:        "Last Sender outbound call outcome per node: 0 ok (2xx), 1 degraded (responded non-2xx <500), 2 down (transport error or 5xx). Drives Overview badge (§41, §52).",
			ConstLabels: constLabels,
		}, []string{"node"}),

		// §32: запросы, отклонённые защитой от зацикливания (превышен hop-лимит
		// X-Nexus-Hops). mode=sync|async — путь, на котором сработала защита.
		LoopDetectedTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name:        "nexus_loop_detected_total",
			Help:        "Requests rejected by the loop-protection hop limit (X-Nexus-Hops), by mode (sync, async).",
			ConstLabels: constLabels,
		}, []string{"mode"}),

		// §82.3: обращения во внешний /api/v1/requestAsync/ к узлу, чей
		// root_method не requestAsync. Наружу такой отказ неотличим от 404
		// «узла нет» (существование узла не раскрываем), поэтому счётчик —
		// единственный способ увидеть, что интеграция настроена не туда.
		AsyncIngressRejectedTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name:        "nexus_async_ingress_rejected_total",
			Help:        "Requests to /api/v1/requestAsync rejected because the node is not configured as requestAsync (§82.3).",
			ConstLabels: constLabels,
		}, []string{"node"}),

		// Phase AUD.8 (D.4): сбои проверки rate-limit'а (Redis недоступен и
		// т.п.). Лимиты по §9.4 fail-open — этот счётчик единственный сигнал,
		// что лимиты фактически не действуют; алертить при росте.
		// scope — префикс ключа до первого ':' (login, replay, kafka, ...).
		RateLimitCheckErrorsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name:        "nexus_ratelimit_check_errors_total",
			Help:        "Rate limit checks that failed (e.g. Redis down) and were allowed fail-open, by key scope.",
			ConstLabels: constLabels,
		}, []string{"scope"}),

		RequestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:        "nexus_request_duration_seconds",
			Help:        "Nexus request duration in seconds (end-to-end for the given service).",
			ConstLabels: constLabels,
			// DefBuckets упираются в 10с, а timeout_ms узла — до 600с. Расширяем
			// верх диапазона, чтобы histogram_quantile (p95/p99 per-node, §21)
			// не «прилипал» к +Inf на медленных узлах.
			Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600},
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

		// §36: исход обработки одного DLQ-сообщения авто-репроцессором.
		// result: succeeded (доставлено 2xx), failed (не-2xx → republish),
		// ttl_dropped (истёк TTL), skipped (paused/breaker → republish без попытки),
		// dropped (узел удалён/disabled/отменён оператором).
		DLQReprocessTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name:        "nexus_dlq_reprocess_total",
			Help:        "DLQ reprocessor outcomes per node and result (succeeded, failed, ttl_dropped, skipped, dropped).",
			ConstLabels: constLabels,
		}, []string{"node", "result"}),

		// §36: длительность одного прохода sweeper'а над DLQ.
		DLQReprocessDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:        "nexus_dlq_reprocess_duration_seconds",
			Help:        "DLQ reprocessor single sweep pass duration in seconds.",
			ConstLabels: constLabels,
			Buckets:     []float64{.01, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60},
		}),

		// §74.3: версия схемы PostgreSQL, на которой работает сервис.
		PGSchemaVersion: prometheus.NewGauge(prometheus.GaugeOpts{
			Name:        "nexus_pg_schema_version",
			Help:        "Applied PostgreSQL schema version (0 = no migrations applied).",
			ConstLabels: constLabels,
		}),

		// §74.3: признак незавершённого отката — схема новее кода сервиса.
		PGSchemaAhead: prometheus.NewGauge(prometheus.GaugeOpts{
			Name:        "nexus_pg_schema_ahead",
			Help:        "1 when the database schema is newer than this build's migrations directory (rollback in progress).",
			ConstLabels: constLabels,
		}),
	}

	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.RequestsTotal,
		m.RequestsIncompleteTotal,
		m.NodeLastRequestError,
		m.LoopDetectedTotal,
		m.AsyncIngressRejectedTotal,
		m.RateLimitCheckErrorsTotal,
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
		m.DLQReprocessTotal,
		m.DLQReprocessDuration,
		m.PGSchemaVersion,
		m.PGSchemaAhead,
	)
	return m
}

// SetSchemaState публикует состояние схемы PostgreSQL (§74.3): версию, на
// которой сервис работает, и признак «схема новее моего кода» — то есть откат
// кода выполнен, а схема осталась от новой версии. Признак держится до конца
// жизни процесса: он снимается только следующим стартом на согласованной схеме.
//
// Вызывается сервисами, которые накатывают миграции (Web и Receiver). Sender их
// не накатывает и метрику не публикует — у него нет источника значения.
func (m *Metrics) SetSchemaState(version uint, ahead bool) {
	m.PGSchemaVersion.Set(float64(version))
	if ahead {
		m.PGSchemaAhead.Set(1)
		return
	}
	m.PGSchemaAhead.Set(0)
}

// IncRateLimitCheckError реализует ratelimit.ErrorSink: учёт fail-open
// пропусков при недоступном Redis (Phase AUD.8).
func (m *Metrics) IncRateLimitCheckError(scope string) {
	m.RateLimitCheckErrorsTotal.WithLabelValues(scope).Inc()
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

// IncAsyncIngressRejected — §82.3: обращение во внешний /requestAsync к узлу,
// который не настроен асинхронным.
func (m *Metrics) IncAsyncIngressRejected(node string) {
	m.AsyncIngressRejectedTotal.WithLabelValues(node).Inc()
}

// SetNodeLastRequestOutcome фиксирует исход последнего исходящего вызова узла
// (§41/§52): 0=ok (2xx), 1=degraded (ответил не-2xx <500), 2=down (транспортная
// ошибка или 5xx). Порядок значений важен: PromQL `max by (node)` между
// репликами Sender выбирает худшее состояние. Управляет бейджем узла в Overview.
func (m *Metrics) SetNodeLastRequestOutcome(node string, outcome domain.NodeOutcome) {
	m.NodeLastRequestError.WithLabelValues(node).Set(outcome.GaugeValue())
}

// Registry возвращает собственный prometheus.Registry — для тестов или
// дополнительных кастомных collectors.
func (m *Metrics) Registry() *prometheus.Registry { return m.registry }

// Handler — http.Handler для эндпоинта /metrics, изолированный от default registry.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{Registry: m.registry})
}
