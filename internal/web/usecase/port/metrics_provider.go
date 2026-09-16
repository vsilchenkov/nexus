package port

import (
	"context"
	"time"

	"nexus/internal/domain"
)

// NodeKPI — сводные показатели одного узла за окно (§21, вкладки
// «Обзор»/«Метрики»). Считаются из ClickHouse-логов узла (NodeLogMetrics).
type NodeKPI struct {
	Total     uint64  // всего исходящих вызовов (nexus_requests_total)
	Delivered uint64  // успешно доставленных (status 2xx) = total - errors
	Errors    uint64  // «незавершённых» вызовов (non-2xx, nexus_request_incomplete_total)
	P95ms     float64 // 95-й перцентиль длительности, мс
	P99ms     float64 // 99-й перцентиль длительности, мс

	// LastSeenMs — время последнего запроса В ОКНЕ И ПОД ТЕКУЩИМИ ФИЛЬТРАМИ
	// (§84.6), UnixMilli; 0 = в окне запросов не было.
	//
	// Считается той же агрегацией, что и остальные поля, то есть бесплатно.
	// Полный max(date_request) без окна — скан колонки на всей таблице (боевая
	// внешняя §64 — 10,2 млн записей), и вкладка поллится каждые ~12 с: такому
	// запросу здесь не место. «За всё время» живёт отдельным действием.
	LastSeenMs int64
}

// SeriesPoint — одна точка временного ряда для графика трафика узла.
// TsMs — начало бакета (UnixMilli).
type SeriesPoint struct {
	TsMs   int64
	Count  uint64
	Errors uint64
}

// LatencyPoint — одна точка графика латентности узла (§84.5).
//
// Attempts здесь не украшение и не дубль Count из SeriesPoint: это признак
// «в интервале были данные». Пустой интервал обязан рисоваться РАЗРЫВОМ линии,
// а не нулём — «узел отвечал за 0 мс» прямая ложь. Ноль попыток и есть тот
// признак, по которому клиент рвёт линию.
//
// Единица — ПОПЫТКА (строка таблицы), а не запись: длительность описывает
// внешний вызов, а не запрос (§79.5). Поэтому Attempts и Count в одном
// интервале законно расходятся, и складывать их нельзя.
type LatencyPoint struct {
	TsMs     int64
	P50ms    float64
	P95ms    float64
	Attempts uint64
}

// MethodStat — одна строка рейтинга «Запросы по методам» (§102).
//
// Method — значение колонки `method`: у passthrough-узла (§39) это хвост пути
// запроса, у обычного узла оно пусто. Пустое значение — законная группа, а не
// «нет данных»: выброси её — и Σ по методам перестанет сходиться с
// NodeKPI.Total, на чём держится расчёт строки «Остальные» (§102.3).
//
// Единица — ЗАПИСЬ, как у NodeKPI (§79.5): те же выражения уникальности,
// разложенные по группам. Поэтому Total/Delivered складываются с полями KPI, а
// с LatencyPoint.Attempts (попытки) — нет.
type MethodStat struct {
	Method    string
	Total     uint64
	Delivered uint64
}

// GlobalTotals — кросс-сервисные счётчики за окно (из Prometheus).
// Incoming — принято Receiver'ом; Outgoing — доставлено Sender'ом во внешний
// URL; Errors — внешние ответы с ошибкой (status 0/4xx/5xx) у Sender'а.
type GlobalTotals struct {
	Incoming float64
	Outgoing float64
	Errors   float64
}

// NodeKey — ключ per-node ряда Prometheus (§101.9): слог команды и путь узла.
//
// Путь уникален лишь внутри команды, поэтому одного пути мало: одноимённые
// узлы разных команд складывались бы в один ряд. Team пуст у рядов, собранных
// до §101 (метки team не было), и у запросов, не дошедших до узла: Path ==
// metrics.NodeUnresolved — обращение к несуществующему узлу (§94.8).
type NodeKey struct {
	Team string
	Path string
}

// NodeThroughput — per-node счётчики за окно (из Prometheus, sum by team, node).
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

	// NodeLatencyChart — перцентили длительности по тем же интервалам (§84.5).
	//
	// Отдельный метод, а не поле SeriesPoint: у трафика единица — ЗАПИСИ, у
	// латентности — ПОПЫТКИ (строки), ровно как у p95/p99 в NodeKPI (§79.5).
	// Слить с NodeChart нельзя технически: в основном режиме тот сворачивает
	// строки в записи (GROUP BY ID), и перцентили по попыткам оттуда уже не
	// достать.
	//
	// stepSec обязан совпадать с ChartQuery.StepSec: два графика стоят друг под
	// другом и сопоставляются глазом только в одинаковых столбцах.
	NodeLatencyChart(ctx context.Context, q LogQuery, stepSec int64) ([]LatencyPoint, error)

	// NodeMethodStats — рейтинг значений колонки `method` под теми же фильтрами
	// (§102). Не более limit строк, по убыванию Total, при равенстве — по Method
	// (детерминизм: без второго ключа порядок одинаковых счётчиков плавает от
	// запроса к запросу, и строки на экране переставляются на каждом поллинге).
	//
	// more сообщает, что методов БОЛЬШЕ, чем вернулось строк. Флаг считает
	// адаптер (он запрашивает на строку больше и лишнюю отбрасывает), а не
	// вызывающий по «пришло ровно limit»: при РОВНО limit методах такой признак
	// объявил бы несуществующий хвост (§102.4).
	//
	// approx — та же настройка точности, что у NodeKPI (§44-perf). Вызывающий
	// обязан передать сюда ТО ЖЕ значение, что и в NodeKPI одного запроса:
	// разные режимы дают разные оценки, и Σ по методам разъедется с KPI.
	NodeMethodStats(ctx context.Context, q LogQuery, approx bool, limit int) (stats []MethodStat, more bool, err error)
}

// PromMetrics — глобальные и кросс-сервисные метрики из Prometheus
// (query API). Реализуется адаптером adapter/out/prometheus. Опционально:
// при пустом prometheus.url в app.go провайдер не создаётся (nil), и
// MetricsUsecase отдаёт деградацию (prometheus_available=false), не падая.
type PromMetrics interface {
	// GlobalTotals — суммарные incoming/outgoing/errors за окно. team — слог
	// команды (§101.9): непустой сужает сумму до её рядов, пустой — весь инстанс.
	GlobalTotals(ctx context.Context, window time.Duration, team string) (GlobalTotals, error)

	// KafkaQueue — суммарный consumer lag всех партиций (мгновенное значение).
	KafkaQueue(ctx context.Context) (float64, error)

	// NodeThroughput — per-node in/out/errors за период (since, until], ключ —
	// пара меток (team, node) в nexus_requests_total (§101.9). Произвольный
	// период (§28 Пункт 4): окно = until-since, запрос вычисляется на момент until.
	NodeThroughput(ctx context.Context, since, until time.Time) (map[NodeKey]NodeThroughput, error)

	// NodeErrors — per-node число «незавершённых» вызовов (done=0) за окно
	// из nexus_request_incomplete_total. Ключ — пара (team, node).
	// Используется планировщиком Telegram-алертов (§22) вместо ClickHouse.
	NodeErrors(ctx context.Context, window time.Duration) (map[NodeKey]float64, error)

	// NodeLastErrors — per-node исход ПОСЛЕДНЕГО исходящего вызова (instant
	// gauge nexus_node_last_request_error на момент at): 0/отсутствует = ok
	// (2xx), 1 = degraded (ответил не-2xx <500), 2 = down (транспортная ошибка
	// или 5xx) — §52; маппинг в usecase через domain.OutcomeFromGaugeValue.
	// Ключ — пара (team, node). Fallback-источник бейджа узла в Overview (§41),
	// независим от окна.
	NodeLastErrors(ctx context.Context, at time.Time) (map[NodeKey]float64, error)

	// NodeSeries — спарклайн входящего трафика per-node: range-запрос за период
	// (since, until], разбитый на buckets точек (один запрос на весь список,
	// §22/§28). Ключ — пара (team, node); точки — те, что вернул Prometheus.
	NodeSeries(ctx context.Context, since, until time.Time, buckets int) (map[NodeKey][]float64, error)

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
	// GetLastOutcomes возвращает исход последнего вызова (ok/degraded/down) по
	// id узлов (§101.6), карта — тоже по id. Узлы без записи в Redis либо с
	// нераспознанным значением в карту НЕ попадают (вызывающий добивает их из
	// Prometheus).
	GetLastOutcomes(ctx context.Context, nodeIDs []string) (map[string]domain.NodeOutcome, error)
}

// NodeStatusMigrator — перенос исходов узлов со старых ключей Redis по пути на
// ключи по id (§101.6). Реализуется adapter/out/redis поверх platform/nodestatus.
type NodeStatusMigrator interface {
	// LegacyMigrated — перенос уже выполнен.
	LegacyMigrated(ctx context.Context) (bool, error)
	// LegacyOutcomePaths — пути узлов, у которых остался ключ исхода по пути.
	LegacyOutcomePaths(ctx context.Context) ([]string, error)
	// MigrateLegacyOutcomes переносит исходы для пар «путь → id», не трогая уже
	// записанные ключи по id, отмечает перенос выполненным и возвращает число
	// перенесённых ключей.
	MigrateLegacyOutcomes(ctx context.Context, idsByPath map[string]string) (int, error)
}
