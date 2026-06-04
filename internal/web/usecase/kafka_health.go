package usecase

import "time"

// KafkaThresholds — пороги индикации экрана Kafka-мониторинга (§6 spec).
// Собственный тип usecase (не config) — usecase зависит от абстракции, маппинг
// из config.KafkaAlertsSection выполняется в app.go.
type KafkaThresholds struct {
	LagWarning              int64
	LagCritical             int64
	LagGrowthCriticalPerSec float64
	ErrorRateWarning        float64
	ErrorRateCritical       float64
	ProduceP95WarningMs     float64
	ProduceP95CriticalMs    float64
}

// Severity состояния кластера для health-banner (§5.2 spec).
const (
	SeverityOK   = "ok"
	SeverityWarn = "warn"
	SeverityErr  = "err"
)

// Reason-коды health-banner. Frontend локализует их в конкретный текст
// (с подстановкой чисел из summary/broker_health) — backend i18n-независим.
const (
	ReasonHealthy           = "healthy"
	ReasonOfflinePartitions = "offline_partitions"
	ReasonBrokersDown       = "brokers_down"
	ReasonErrorRateHigh     = "error_rate_high"
	ReasonLagHigh           = "lag_high"
	ReasonLagGrowing        = "lag_growing"
	ReasonUnderReplicated   = "under_replicated"
	ReasonProduceLatency    = "produce_latency_high"
)

// HealthBanner — итог оценки состояния (§5.2 spec): severity + машинный reason.
// Числовой контекст (lag, error rate, offline и т.д.) фронтенд берёт из других
// полей overview-ответа и подставляет в локализованный текст.
type HealthBanner struct {
	Severity string
	Reason   string
}

// healthInput — агрегированные сигналы для оценки severity.
type healthInput struct {
	currentLag       int64
	lagGrowthPerSec  float64
	errorRate        float64
	produceP95ms     float64
	brokersTotal     int
	brokersOnline    int
	underReplicated  int
	offlinePartition int
}

// evaluateHealth определяет severity и причину по порогам (§6 spec).
// Проверки от самого критичного к менее: красные раньше жёлтых, первая
// сработавшая определяет reason (то, что админу делать в первую очередь).
func evaluateHealth(in healthInput, th KafkaThresholds) HealthBanner {
	brokersOffline := in.brokersTotal - in.brokersOnline

	switch {
	case in.offlinePartition >= 1:
		return HealthBanner{SeverityErr, ReasonOfflinePartitions}
	case in.brokersTotal > 0 && brokersOffline*2 > in.brokersTotal:
		return HealthBanner{SeverityErr, ReasonBrokersDown}
	case th.ErrorRateCritical > 0 && in.errorRate > th.ErrorRateCritical:
		return HealthBanner{SeverityErr, ReasonErrorRateHigh}
	case th.LagCritical > 0 && in.currentLag > th.LagCritical:
		return HealthBanner{SeverityErr, ReasonLagHigh}
	case th.LagGrowthCriticalPerSec > 0 && in.lagGrowthPerSec > th.LagGrowthCriticalPerSec:
		return HealthBanner{SeverityErr, ReasonLagGrowing}
	case th.ProduceP95CriticalMs > 0 && in.produceP95ms > th.ProduceP95CriticalMs:
		return HealthBanner{SeverityErr, ReasonProduceLatency}
	}

	switch {
	case in.underReplicated >= 1:
		return HealthBanner{SeverityWarn, ReasonUnderReplicated}
	case brokersOffline >= 1:
		return HealthBanner{SeverityWarn, ReasonBrokersDown}
	case th.LagWarning > 0 && in.currentLag > th.LagWarning:
		return HealthBanner{SeverityWarn, ReasonLagHigh}
	case th.ErrorRateWarning > 0 && in.errorRate > th.ErrorRateWarning:
		return HealthBanner{SeverityWarn, ReasonErrorRateHigh}
	case th.ProduceP95WarningMs > 0 && in.produceP95ms > th.ProduceP95WarningMs:
		return HealthBanner{SeverityWarn, ReasonProduceLatency}
	}

	return HealthBanner{SeverityOK, ReasonHealthy}
}

// autoStep — шаг временного ряда по длительности окна (§4.2 spec): 1ч→30с,
// 24ч→5м, 30д→1ч (между — промежуточные значения).
func autoStep(window time.Duration) time.Duration {
	switch {
	case window <= time.Hour:
		return 30 * time.Second
	case window <= 3*time.Hour:
		return time.Minute
	case window <= 24*time.Hour:
		return 5 * time.Minute
	case window <= 7*24*time.Hour:
		return 30 * time.Minute
	default:
		return time.Hour
	}
}

// pctDelta — относительное изменение (cur-prev)/prev. ok=false, если prev<=0
// (дельта неопределена).
func pctDelta(cur, prev float64) (ratio float64, ok bool) {
	if prev <= 0 {
		return 0, false
	}
	return (cur - prev) / prev, true
}
