package port

import (
	"context"
	"time"

	"nexus/internal/domain"
)

// NodeKPI — сводные показатели одного узла за окно (§21, вкладки
// «Обзор»/«Метрики»). Считаются из Prometheus по per-node счётчикам Sender'а.
type NodeKPI struct {
	Total     uint64  // всего исходящих вызовов (nexus_requests_total)
	Delivered uint64  // успешно доставленных (status 2xx) = total - errors
	Errors    uint64  // «незавершённых» вызовов (non-2xx, nexus_request_incomplete_total)
	P95ms     float64 // 95-й перцентиль длительности, мс
	P99ms     float64 // 99-й перцентиль длительности, мс
}

// SeriesPoint — одна точка временного ряда для графика трафика узла.
// TsMs — начало бакета (UnixMilli).
type SeriesPoint struct {
	TsMs   int64
	Count  uint64
	Errors uint64
}

// GlobalTotals — кросс-сервисные счётчики за окно (из Prometheus).
// Incoming — принято Receiver'ом; Outgoing — доставлено Sender'ом во внешний
// URL; Errors — внешние ответы с ошибкой (status 0/4xx/5xx) у Sender'а.
type GlobalTotals struct {
	Incoming float64
	Outgoing float64
	Errors   float64
}

// NodeThroughput — per-node счётчики за окно (из Prometheus, sum by node).
type NodeThroughput struct {
	In     float64
	Out    float64
	Errors float64
	P95ms  float64 // 95-й перцентиль длительности исходящих (Sender), мс
}

// KafkaSummary — сводка Kafka-трафика за окно (since, until] для экрана
// Kafka-мониторинга (§4.1 spec). Источник — Prometheus, фильтр async-трафика
// method="requestAsync". InFlight и ProduceP95ms берутся из выделенных метрик
// nexus_kafka_in_flight / nexus_kafka_produce_duration_seconds (§31).
type KafkaSummary struct {
	Produced       float64 // принято Receiver'ом в Kafka (requestAsync)
	Consumed       float64 // обработано Sender-consumer'ом (requestAsync)
	FailedProduced float64 // отказы публикации на Receiver (status 0/5xx)
	FailedConsumed float64 // незавершённые на Sender (non-2xx, incomplete_total)
	CurrentLag     float64 // суммарный consumer lag сейчас (sum nexus_kafka_lag)
	InFlight       float64 // сообщения «в полёте» (sum nexus_kafka_in_flight)
	ProduceP95ms   float64 // p95 длительности публикации в Kafka, мс
}

// KafkaPoint — точка временного ряда Kafka-графика (§4.2 spec). TsMs — начало
// бакета (UnixMilli), V — значение (rate в сообщениях/сек для produced/consumed/
// errors; абсолютный lag для метрики lag).
type KafkaPoint struct {
	TsMs int64
	V    float64
}

// ChartQuery — как строить столбцы графика узла (§79.5).
type ChartQuery struct {
	// StepSec — ширина одного столбца в секундах. Задаётся вызывающим (usecase
	// согласует его с окном), адаптер ничего не домысливает.
	StepSec int64

	// ByRecord — единица и привязка столбца:
	//
	//   true  (§79.5, основной режим): столбец — ЗАПИСИ, попавшие в интервал по
	//         своему ПЕРВОМУ прогону, а «ошибка» определяется ИТОГОВЫМ статусом
	//         записи в окне. Поэтому после успешного авто-повтора красный
	//         сегмент исчезает из столбца прихода сам — как и запись из
	//         «Неудачных доставок» (§79.1). Требует свёртки строк в записи
	//         (GROUP BY ID) по всему окну.
	//
	//   false (деградация): столбец — записи, у которых прогон ПОПАЛ в этот
	//         интервал, статус — по прогонам интервала. Дешевле (одна стадия),
	//         но запись с прогонами в разных интервалах видна в каждом, и
	//         повтор не «лечит» прошлый столбец. Включается только когда записей
	//         в окне больше порога; наружу об этом сообщается явно.
	ByRecord bool

	// Approx — приблизительный счёт уникальных (HLL). Действует только при
	// ByRecord=false: точный режим уже свёл строки в записи, и считать там
	// нечего, кроме готовых групп.
	Approx bool
}

// NodeLogMetrics — ТОЧНЫЕ per-node KPI и временной ряд графика узла ИЗ
// ClickHouse-логов (§21, вкладки «Обзор»/«Метрики»). Реализуется LogReaderCH.
// В отличие от Prometheus increase(): точные счётчики по уникальным запросам, без
// rate-экстраполяции и без зависимости от доступности Prometheus (нет мерцания).
//
// §79.4: окно, таблица, узел и фильтры журнала приходят одним LogQuery — тем же
// типом, что у списка логов. Отдельный набор условий для метрик гарантированно
// разошёлся бы со списком (эту болезнь §67 уже лечил, сделав searchConds общим).
type NodeLogMetrics interface {
	// NodeKPI — KPI узла под фильтрами q. approx (§44-perf): false = точный счёт
	// уникальных (countDistinct/uniqExact), true = приблизительный HyperLogLog
	// (uniq/uniqIf, ~3× дешевле, ошибка ~0.3%). Управляется настройкой
	// app_settings.general.metrics_approx_counts.
	NodeKPI(ctx context.Context, q LogQuery, approx bool) (NodeKPI, error)

	// NodeChart — временной ряд под теми же фильтрами. Ряд плотный: интервалы без
	// данных отдаются нулями, чтобы график не «сжимался» по времени.
	NodeChart(ctx context.Context, q LogQuery, c ChartQuery) ([]SeriesPoint, error)
}

// PromMetrics — глобальные и кросс-сервисные метрики из Prometheus
// (query API). Реализуется адаптером adapter/out/prometheus. Опционально:
// при пустом prometheus.url в app.go провайдер не создаётся (nil), и
// MetricsUsecase отдаёт деградацию (prometheus_available=false), не падая.
type PromMetrics interface {
	// GlobalTotals — суммарные incoming/outgoing/errors за окно.
	GlobalTotals(ctx context.Context, window time.Duration) (GlobalTotals, error)

	// KafkaQueue — суммарный consumer lag всех партиций (мгновенное значение).
	KafkaQueue(ctx context.Context) (float64, error)

	// NodeThroughput — per-node in/out/errors за период (since, until], ключ —
	// path узла (метка node в nexus_requests_total). Произвольный период (§28
	// Пункт 4): окно = until-since, запрос вычисляется на момент until.
	NodeThroughput(ctx context.Context, since, until time.Time) (map[string]NodeThroughput, error)

	// NodeErrors — per-node число «незавершённых» вызовов (done=0) за окно
	// из nexus_request_incomplete_total. Ключ — path узла (метка node).
	// Используется планировщиком Telegram-алертов (§22) вместо ClickHouse.
	NodeErrors(ctx context.Context, window time.Duration) (map[string]float64, error)

	// NodeLastErrors — per-node исход ПОСЛЕДНЕГО исходящего вызова (instant
	// gauge nexus_node_last_request_error на момент at): 0/отсутствует = ok
	// (2xx), 1 = degraded (ответил не-2xx <500), 2 = down (транспортная ошибка
	// или 5xx) — §52; маппинг в usecase через domain.OutcomeFromGaugeValue.
	// Ключ — path узла. Fallback-источник бейджа узла в Overview (§41),
	// независим от окна.
	NodeLastErrors(ctx context.Context, at time.Time) (map[string]float64, error)

	// NodeSeries — спарклайн входящего трафика per-node: range-запрос за период
	// (since, until], разбитый на buckets точек (один запрос на весь список,
	// §22/§28). Ключ — path узла; длина слайса == buckets (недостающие — нули).
	NodeSeries(ctx context.Context, since, until time.Time, buckets int) (map[string][]float64, error)

	// NodeKPI — сводка одного узла (total/delivered/errors/p95/p99) за период
	// (since, until]. node — path узла (метка node счётчиков Sender'а).
	// Источник вкладки «Метрики» узла (§21) вместо ClickHouse: total/errors из
	// nexus_requests_total / nexus_request_incomplete_total, перцентили — из
	// histogram_quantile по nexus_request_duration_seconds_bucket.
	NodeKPI(ctx context.Context, node string, since, until time.Time) (NodeKPI, error)

	// NodeChart — временной ряд графика одного узла за период (since, until],
	// разбитый на buckets равных бакетов (ASC по времени, недостающие — нули,
	// длина результата == buckets). Count — все исходящие вызовы, Errors —
	// «незавершённые» (non-2xx). node — path узла.
	NodeChart(ctx context.Context, node string, since, until time.Time, buckets int) ([]SeriesPoint, error)

	// KafkaOverview — сводка Kafka-трафика за период (since, until] для §4.1
	// spec: produced/consumed/failed/lag/in-flight/p95. Источник — Prometheus
	// (async-трафик, method="requestAsync"); current_lag/in_flight — instant на
	// момент until. Для дельты к предыдущему периоду usecase вызывает метод
	// повторно на сдвинутом окне.
	KafkaOverview(ctx context.Context, since, until time.Time) (KafkaSummary, error)

	// KafkaTimeseries — временные ряды Kafka-графиков (§4.2 spec) за период
	// (since, until] с шагом step. metrics — подмножество
	// {"produced","consumed","errors","lag"}; возвращает по ряду на каждую
	// запрошенную метрику (ключ — имя метрики, ASC по времени). produced/
	// consumed/errors — rate (сообщений/сек), lag — абсолютное значение gauge.
	KafkaTimeseries(ctx context.Context, since, until time.Time, step time.Duration, metrics []string) (map[string][]KafkaPoint, error)

	// KafkaTopicSizes — размер топиков на дисках кластера в байтах (ключ — имя
	// топика, §75). Instant-агрегат `sum by(topic)(kafka_log_log_size)` — метрики
	// JMX-агента на брокере: админ-протокол Kafka размер не отдаёт (см.
	// TopicInfo.SizeBytes), поэтому это единственный источник.
	//
	// Значение суммирует ВСЕ реплики партиций топика, то есть отвечает на вопрос
	// «сколько занято на дисках кластера», а не «каков логический объём данных»:
	// при RF=3 оно втрое больше объёма сообщений. При RF=1 (наша инсталляция)
	// разницы нет.
	//
	// Отсутствие метрики (JMX-агент не настроен, внешняя чужая Kafka) — штатная
	// деградация, а не ошибка: пустая карта без error, UI показывает «—».
	KafkaTopicSizes(ctx context.Context) (map[string]int64, error)
}

// NodeStatusReader — персистентный исход последнего исходящего вызова узла из
// Redis (§46, §52). В отличие от Prometheus-гауджа nexus_node_last_request_error
// (in-memory, теряется при рестарте Sender'а), Redis-ключ переживает рестарт →
// runtime-бейдж узла на дашборде корректен после деплоя. Источник истины для
// бейджа; Prometheus остаётся fallback'ом. Реализуется adapter/out/redis.
// Может быть nil (нет Redis) → applyLastOutcomes деградирует на Prometheus.
type NodeStatusReader interface {
	// GetLastOutcomes возвращает исход последнего вызова (ok/degraded/down) для
	// указанных путей узлов. Узлы без записи в Redis либо с нераспознанным
	// значением в карту НЕ попадают (вызывающий добивает их из Prometheus).
	GetLastOutcomes(ctx context.Context, nodePaths []string) (map[string]domain.NodeOutcome, error)
}
