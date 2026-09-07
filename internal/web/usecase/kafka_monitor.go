package usecase

import (
	"context"
	"time"

	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// KafkaMonitorUsecase — экран мониторинга Kafka (§4 spec). Источники
// опциональны и деградируют независимо: Prometheus (prom) даёт throughput/
// ошибки/lag/KPI и top-узлы; Kafka Admin (admin) — топики/брокеры/ping;
// cache — Redis-кеш ответов admin (TTL 30с). При nil-источнике
// соответствующие блоки помечаются недоступными, экран не падает.
type KafkaMonitorUsecase struct {
	prom  port.PromMetrics // может быть nil
	admin port.KafkaAdmin  // может быть nil
	cache port.KafkaCache  // может быть nil
	// §98.2: путь узла → id, чтобы строки top-узлов вели на журнал узла.
	// Может быть nil — тогда строки остаются текстом, как до §98.
	nodes  port.NodePathResolver
	th     KafkaThresholds
	logger logging.Logger
}

func NewKafkaMonitorUsecase(prom port.PromMetrics, admin port.KafkaAdmin, cache port.KafkaCache, nodes port.NodePathResolver, th KafkaThresholds, logger logging.Logger) *KafkaMonitorUsecase {
	return &KafkaMonitorUsecase{prom: prom, admin: admin, cache: cache, nodes: nodes, th: th, logger: logger}
}

// KafkaSummaryView — числовая сводка за период (§4.1 spec, поле summary).
type KafkaSummaryView struct {
	ProducedTotal  uint64
	ConsumedTotal  uint64
	FailedProduced uint64
	FailedConsumed uint64
	CurrentLag     uint64
	InFlightNow    uint64
}

// KafkaOverviewResult — ответ §4.1: сводка, дельты, broker_health, health-banner.
type KafkaOverviewResult struct {
	From, To            time.Time
	Summary             KafkaSummaryView
	DeltaProduced       float64
	DeltaConsumed       float64
	HasDelta            bool
	ErrorRate           float64
	BrokerHealth        port.BrokerHealth
	Health              HealthBanner
	PrometheusAvailable bool
	KafkaAvailable      bool
}

// Overview — сводка экрана за период (since, until]. Деградирует поблочно.
func (u *KafkaMonitorUsecase) Overview(ctx context.Context, since, until time.Time) KafkaOverviewResult {
	res := KafkaOverviewResult{From: since, To: until}
	if u.prom != nil {
		u.fillPromOverview(ctx, since, until, &res)
	}
	if u.admin != nil {
		if bh, err := u.brokerHealth(ctx); err != nil {
			u.logger.Warn("kafka broker health failed", u.logger.Err(err))
		} else {
			res.BrokerHealth = bh
			res.KafkaAvailable = true
		}
	}
	res.Health = evaluateHealth(healthInput{
		currentLag:       int64(res.Summary.CurrentLag),
		lagGrowthPerSec:  u.lagGrowthPerSec(ctx, until),
		errorRate:        res.ErrorRate,
		brokersTotal:     res.BrokerHealth.BrokersTotal,
		brokersOnline:    res.BrokerHealth.BrokersOnline,
		underReplicated:  res.BrokerHealth.UnderReplicatedPartitions,
		offlinePartition: res.BrokerHealth.OfflinePartitions,
	}, u.th)
	return res
}

// fillPromOverview заполняет summary/дельты/error-rate из Prometheus за окно
// (since, until] и предыдущее такое же окно (для дельт).
func (u *KafkaMonitorUsecase) fillPromOverview(ctx context.Context, since, until time.Time, res *KafkaOverviewResult) {
	cur, err := u.prom.KafkaOverview(ctx, since, until)
	if err != nil {
		u.logger.Warn("kafka prometheus overview failed", u.logger.Err(err))
		return
	}
	res.PrometheusAvailable = true
	res.Summary = KafkaSummaryView{
		ProducedTotal:  f2u(cur.Produced),
		ConsumedTotal:  f2u(cur.Consumed),
		FailedProduced: f2u(cur.FailedProduced),
		FailedConsumed: f2u(cur.FailedConsumed),
		CurrentLag:     f2u(cur.CurrentLag),
		InFlightNow:    f2u(cur.InFlight),
	}
	total := cur.Produced + cur.Consumed
	failed := cur.FailedProduced + cur.FailedConsumed
	if total > 0 {
		res.ErrorRate = failed / total
	}

	d := until.Sub(since)
	prev, err := u.prom.KafkaOverview(ctx, since.Add(-d), since)
	if err != nil {
		return
	}
	dp, okP := pctDelta(cur.Produced, prev.Produced)
	dc, okC := pctDelta(cur.Consumed, prev.Consumed)
	res.DeltaProduced, res.DeltaConsumed = dp, dc
	res.HasDelta = okP || okC
}

// lagGrowthPerSec оценивает прирост lag/сек за последние 5 минут (для триггера
// «lag растёт», §6). Без Prometheus или при недостатке точек → 0.
func (u *KafkaMonitorUsecase) lagGrowthPerSec(ctx context.Context, until time.Time) float64 {
	if u.prom == nil {
		return 0
	}
	const window = 5 * time.Minute
	series, err := u.prom.KafkaTimeseries(ctx, until.Add(-window), until, 30*time.Second, []string{"lag"})
	if err != nil {
		return 0
	}
	pts := series["lag"]
	if len(pts) < 2 {
		return 0
	}
	first, last := pts[0], pts[len(pts)-1]
	secs := float64(last.TsMs-first.TsMs) / 1000
	if secs <= 0 {
		return 0
	}
	return (last.V - first.V) / secs
}

// KafkaTimeseriesResult — ответ §4.2: ряды + выбранный шаг.
type KafkaTimeseriesResult struct {
	StepSeconds         int
	Series              map[string][]port.KafkaPoint
	PrometheusAvailable bool
}

// Timeseries — ряды produced/consumed/errors/lag за период с шагом step
// (step<=0 → авто по длительности окна, §4.2 spec).
func (u *KafkaMonitorUsecase) Timeseries(ctx context.Context, since, until time.Time, step time.Duration, metrics []string) KafkaTimeseriesResult {
	if step <= 0 {
		step = autoStep(until.Sub(since))
	}
	res := KafkaTimeseriesResult{StepSeconds: int(step.Seconds()), Series: map[string][]port.KafkaPoint{}}
	if u.prom == nil {
		return res
	}
	series, err := u.prom.KafkaTimeseries(ctx, since, until, step, metrics)
	if err != nil {
		u.logger.Warn("kafka timeseries failed", u.logger.Err(err))
		return res
	}
	res.Series = series
	res.PrometheusAvailable = true
	return res
}

// KafkaTopicsResult — ответ §4.3: топики + флаг доступности Kafka.
//
// SizesAvailable (§75) отвечает на вопрос «источник размеров вообще жив»:
// Prometheus вернул хотя бы одну серию kafka_log_log_size. Он нужен, чтобы UI
// не выдавал пустой топик за сломанный мониторинг — нулевой размер при
// SizesAvailable=true означает «топик пуст», а не «JMX-агент не настроен».
type KafkaTopicsResult struct {
	Topics         []port.TopicInfo
	KafkaAvailable bool
	SizesAvailable bool
}

// Topics — список топиков (§4.3). Кешируется в Redis (TTL адаптера) для
// снижения нагрузки на брокеры при автообновлении экрана.
func (u *KafkaMonitorUsecase) Topics(ctx context.Context) KafkaTopicsResult {
	res := KafkaTopicsResult{Topics: []port.TopicInfo{}}
	if u.admin == nil {
		return res
	}
	var topics []port.TopicInfo
	if u.cache != nil {
		var cached []port.TopicInfo
		if ok, _ := u.cache.Get(ctx, "topics", &cached); ok {
			topics = cached
		}
	}
	if topics == nil {
		fresh, err := u.admin.Topics(ctx)
		if err != nil {
			u.logger.Warn("kafka topics failed", u.logger.Err(err))
			return res
		}
		topics = fresh
		if u.cache != nil {
			// В кеш кладём метаданные БЕЗ размеров (§75): размер живёт в другом
			// источнике и подмешивается ниже на каждый запрос. Иначе при пропаже
			// метрики UI до конца TTL показывал бы размеры из кеша, противореча
			// флагу SizesAvailable=false.
			_ = u.cache.Set(ctx, "topics", topics)
		}
	}
	return KafkaTopicsResult{
		Topics:         topics,
		KafkaAvailable: true,
		SizesAvailable: u.fillTopicSizes(ctx, topics),
	}
}

// fillTopicSizes проставляет топикам размер на диске из Prometheus (§75):
// админ-протокол Kafka его не отдаёт, единственный источник — JMX-агент
// брокера. Возвращает признак «источник ответил хотя бы одной серией» — он
// уезжает в KafkaTopicsResult.SizesAvailable и позволяет UI отличить пустой
// топик (размер 0, источник жив) от ненастроенного экспортёра.
//
// Запрос идёт на каждый вызов, а не раз в TTL кеша метаданных: instant-запрос
// дешевле, чем разбирательство «почему размеры отстали», а экран и без того
// делает несколько запросов в Prometheus на каждое обновление.
//
// Best-effort: без Prometheus, при его ошибке или при ненастроенном JMX
// размеры остаются нулями, а флаг — false; UI показывает «—» с подсказкой, как
// до §75. Ни на KafkaAvailable, ни на остальные поля топиков это не влияет.
func (u *KafkaMonitorUsecase) fillTopicSizes(ctx context.Context, topics []port.TopicInfo) bool {
	if u.prom == nil || len(topics) == 0 {
		u.logger.Debug("kafka topic sizes skipped",
			u.logger.Str("op", "web.kafkaMonitor.topicSizes"),
			u.logger.Str("reason", "no prometheus or no topics"),
			u.logger.Int("topics", len(topics)))
		return false
	}
	sizes, err := u.prom.KafkaTopicSizes(ctx)
	if err != nil {
		u.logger.Warn("kafka topic sizes failed", u.logger.Err(err))
		return false
	}
	var filled int
	for i := range topics {
		if v, ok := sizes[topics[i].Name]; ok {
			topics[i].SizeBytes = v
			filled++
		}
	}
	// Расхождение «топики есть, размеров нет» — самый вероятный симптом
	// незаведённого scrape-job'а kafka-jmx; без этой строки он выглядит как
	// «UI почему-то рисует прочерк».
	u.logger.Debug("kafka topic sizes merged",
		u.logger.Str("op", "web.kafkaMonitor.topicSizes"),
		u.logger.Int("topics", len(topics)),
		u.logger.Int("with_size", filled),
		u.logger.Int("series", len(sizes)))
	return len(sizes) > 0
}

// brokerHealth — состояние брокеров с кешированием (Redis TTL адаптера).
func (u *KafkaMonitorUsecase) brokerHealth(ctx context.Context) (port.BrokerHealth, error) {
	if u.cache != nil {
		var cached port.BrokerHealth
		if ok, _ := u.cache.Get(ctx, "brokers", &cached); ok {
			return cached, nil
		}
	}
	bh, err := u.admin.BrokerHealth(ctx)
	if err != nil {
		return port.BrokerHealth{}, err
	}
	if u.cache != nil {
		_ = u.cache.Set(ctx, "brokers", bh)
	}
	return bh, nil
}
