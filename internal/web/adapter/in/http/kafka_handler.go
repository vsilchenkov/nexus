package http

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
)

// rateLimiter — узкий интерфейс лимитера на стороне потребителя (ISP):
// удовлетворяется *ratelimit.Limiter. Позволяет подменять лимитер в тестах
// без Redis.
type rateLimiter interface {
	Allow(ctx context.Context, key string, limitPerMin int) (bool, error)
}

// maxKafkaCustomWindow — лимит произвольного периода (§4.1 spec): защита от
// слишком тяжёлых запросов к Prometheus.
const maxKafkaCustomWindow = 90 * 24 * time.Hour

// KafkaHandler — экран мониторинга Kafka (§4 spec), admin-only. Все эндпоинты
// деградируют (флаги *_available), если Prometheus/Kafka недоступны.
type KafkaHandler struct {
	uc     *usecase.KafkaMonitorUsecase
	logger logging.Logger
}

func NewKafkaHandler(uc *usecase.KafkaMonitorUsecase, logger logging.Logger) *KafkaHandler {
	return &KafkaHandler{uc: uc, logger: logger}
}

// KafkaRateLimitMiddleware ограничивает /api/kafka/* на пользователя (§9 spec):
// автообновление экрана раз в 10с не должно превращаться в dashboard-флуд.
// Fail-open при недоступности Redis (как остальные лимиты).
func KafkaRateLimitMiddleware(limiter rateLimiter, limitPerMin int) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := "kafka:anon"
		if s, ok := sessionFromCtx(c); ok {
			key = "kafka:" + s.UserID
		}
		if ok, _ := limiter.Allow(c.Request.Context(), key, limitPerMin); !ok {
			localizedError(c, http.StatusTooManyRequests, "kafka.rate_limited")
			c.Abort()
			return
		}
		c.Next()
	}
}

// resolveKafkaWindow — окно периода с проверкой лимита 90 дней (§4.1 spec).
func resolveKafkaWindow(c *gin.Context) (since, until time.Time, ok bool) {
	since, until, _ = resolveWindow(c)
	if until.Sub(since) > maxKafkaCustomWindow {
		localizedError(c, http.StatusBadRequest, "kafka.period_too_long")
		return since, until, false
	}
	return since, until, true
}

type kafkaPeriodDTO struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

type kafkaSummaryDTO struct {
	ProducedTotal  uint64 `json:"produced_total"`
	ConsumedTotal  uint64 `json:"consumed_total"`
	FailedProduced uint64 `json:"failed_produced"`
	FailedConsumed uint64 `json:"failed_consumed"`
	CurrentLag     uint64 `json:"current_lag"`
	InFlightNow    uint64 `json:"in_flight_now"`
}

type kafkaDeltaDTO struct {
	Produced float64 `json:"produced"`
	Consumed float64 `json:"consumed"`
	HasDelta bool    `json:"has_delta"`
}

type kafkaBrokerHealthDTO struct {
	BrokersTotal              int `json:"brokers_total"`
	BrokersOnline             int `json:"brokers_online"`
	UnderReplicatedPartitions int `json:"under_replicated_partitions"`
	OfflinePartitions         int `json:"offline_partitions"`
}

type kafkaHealthDTO struct {
	Severity string `json:"severity"`
	Reason   string `json:"reason"`
}

type kafkaOverviewDTO struct {
	Period              kafkaPeriodDTO       `json:"period"`
	Summary             kafkaSummaryDTO      `json:"summary"`
	DeltaVsPrevious     kafkaDeltaDTO        `json:"delta_vs_previous_period"`
	ErrorRate           float64              `json:"error_rate"`
	BrokerHealth        kafkaBrokerHealthDTO `json:"broker_health"`
	Health              kafkaHealthDTO       `json:"health"`
	PrometheusAvailable bool                 `json:"prometheus_available"`
	KafkaAvailable      bool                 `json:"kafka_available"`
}

// Overview godoc
// @Summary  Сводка экрана мониторинга Kafka за период (§4.1 spec).
// @Description  KPI produced/consumed/failed/lag/in-flight, дельта к предыдущему периоду, broker_health и severity health-banner. Источники деградируют (флаги *_available). Admin-only, rate-limit 60/мин.
// @Tags     kafka
// @Produce  json
// @Param    range  query  string  false  "1h | 3h | 24h | 7d | 14d | 30d (default 1h)"
// @Param    from   query  string  false  "период с (RFC3339 или UnixMilli); вместе с to — произвольный период (≤90д)"
// @Param    to     query  string  false  "период по (RFC3339 или UnixMilli)"
// @Success  200  {object}  kafkaOverviewDTO
// @Failure  400  {object}  map[string]string  "период длиннее 90 дней"
// @Failure  403  {object}  map[string]string
// @Security CookieAuth
// @Router   /api/kafka/overview [get]
func (h *KafkaHandler) Overview(c *gin.Context) {
	since, until, ok := resolveKafkaWindow(c)
	if !ok {
		return
	}
	r := h.uc.Overview(c.Request.Context(), since, until)
	c.JSON(http.StatusOK, kafkaOverviewDTO{
		Period:  kafkaPeriodDTO{From: r.From, To: r.To},
		Summary: kafkaSummaryDTO(r.Summary),
		DeltaVsPrevious: kafkaDeltaDTO{
			Produced: r.DeltaProduced, Consumed: r.DeltaConsumed, HasDelta: r.HasDelta,
		},
		ErrorRate:           r.ErrorRate,
		BrokerHealth:        kafkaBrokerHealthDTO(r.BrokerHealth),
		Health:              kafkaHealthDTO(r.Health),
		PrometheusAvailable: r.PrometheusAvailable,
		KafkaAvailable:      r.KafkaAvailable,
	})
}

type kafkaPointDTO struct {
	T int64   `json:"t"`
	V float64 `json:"v"`
}

type kafkaTimeseriesDTO struct {
	StepSeconds         int                        `json:"step_seconds"`
	Series              map[string][]kafkaPointDTO `json:"series"`
	PrometheusAvailable bool                       `json:"prometheus_available"`
}

// parseStep — шаг ряда из query: "auto"/пусто → 0 (авто в usecase); иначе
// Go-длительность (30s/1m/5m/1h).
func parseStep(s string) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" || s == "auto" {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

// parseMetrics — список метрик ряда; пусто → все четыре (§4.2 spec).
func parseMetrics(s string) []string {
	if strings.TrimSpace(s) == "" {
		return []string{"produced", "consumed", "errors", "lag"}
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Timeseries godoc
// @Summary  Временные ряды Kafka-графиков за период (§4.2 spec).
// @Description  produced/consumed/errors (rate, сообщений/сек) и lag (абсолют) через Prometheus query_range. Шаг авто по периоду или явный (step). Admin-only.
// @Tags     kafka
// @Produce  json
// @Param    range    query  string  false  "1h | 3h | 24h | 7d | 14d | 30d (default 1h)"
// @Param    from     query  string  false  "период с (RFC3339 или UnixMilli)"
// @Param    to       query  string  false  "период по"
// @Param    step     query  string  false  "auto | 10s | 30s | 1m | 5m | 1h (default auto)"
// @Param    metrics  query  string  false  "CSV из produced,consumed,errors,lag (default все)"
// @Success  200  {object}  kafkaTimeseriesDTO
// @Failure  400  {object}  map[string]string
// @Security CookieAuth
// @Router   /api/kafka/timeseries [get]
func (h *KafkaHandler) Timeseries(c *gin.Context) {
	since, until, ok := resolveKafkaWindow(c)
	if !ok {
		return
	}
	r := h.uc.Timeseries(c.Request.Context(), since, until, parseStep(c.Query("step")), parseMetrics(c.Query("metrics")))
	series := make(map[string][]kafkaPointDTO, len(r.Series))
	for name, pts := range r.Series {
		dto := make([]kafkaPointDTO, 0, len(pts))
		for _, p := range pts {
			dto = append(dto, kafkaPointDTO{T: p.TsMs, V: p.V})
		}
		series[name] = dto
	}
	c.JSON(http.StatusOK, kafkaTimeseriesDTO{
		StepSeconds: r.StepSeconds, Series: series, PrometheusAvailable: r.PrometheusAvailable,
	})
}

type kafkaTopicGroupDTO struct {
	Group    string `json:"group"`
	LagTotal int64  `json:"lag_total"`
	Members  int    `json:"members"`
}

type kafkaTopicDTO struct {
	Name              string               `json:"name"`
	Partitions        int                  `json:"partitions"`
	ReplicationFactor int                  `json:"replication_factor"`
	SizeBytes         int64                `json:"size_bytes"`
	MessagesEstimate  int64                `json:"messages_estimate"`
	RetentionMs       int64                `json:"retention_ms"`
	ConsumerGroups    []kafkaTopicGroupDTO `json:"consumer_groups"`
	UnderReplicated   int                  `json:"under_replicated"`
	OfflinePartitions int                  `json:"offline_partitions"`
}

// Topics godoc
// @Summary  Список топиков кластера (§4.3 spec).
// @Description  Партиции, RF, оценка числа сообщений, consumer-группы с lag, состояние реплик. Размер на диске недоступен (best-effort, 0). Кеш Redis 30с. Admin-only.
// @Tags     kafka
// @Produce  json
// @Success  200  {object}  map[string]interface{}
// @Security CookieAuth
// @Router   /api/kafka/topics [get]
func (h *KafkaHandler) Topics(c *gin.Context) {
	r := h.uc.Topics(c.Request.Context())
	topics := make([]kafkaTopicDTO, 0, len(r.Topics))
	for _, t := range r.Topics {
		groups := make([]kafkaTopicGroupDTO, 0, len(t.ConsumerGroups))
		for _, g := range t.ConsumerGroups {
			groups = append(groups, kafkaTopicGroupDTO(g))
		}
		topics = append(topics, kafkaTopicDTO{
			Name: t.Name, Partitions: t.Partitions, ReplicationFactor: t.ReplicationFactor,
			SizeBytes: t.SizeBytes, MessagesEstimate: t.MessagesEstimate, RetentionMs: t.RetentionMs,
			ConsumerGroups: groups, UnderReplicated: t.UnderReplicated, OfflinePartitions: t.OfflinePartitions,
		})
	}
	c.JSON(http.StatusOK, gin.H{"topics": topics, "kafka_available": r.KafkaAvailable})
}

type kafkaProducerDTO struct {
	NodePath string  `json:"node_path"`
	Produced uint64  `json:"produced"`
	Share    float64 `json:"share"`
}

type kafkaFailureDTO struct {
	NodePath string  `json:"node_path"`
	Failed   uint64  `json:"failed"`
	Rate     float64 `json:"rate"`
}

// ByNode godoc
// @Summary  Топ-узлы по нагрузке и ошибкам за период (§4.4 spec).
// @Description  top_producers (по числу async-сообщений) и top_failures (по ошибкам) из Prometheus (метка node). Admin-only.
// @Tags     kafka
// @Produce  json
// @Param    range  query  string  false  "1h | 3h | 24h | 7d | 14d | 30d (default 1h)"
// @Param    from   query  string  false  "период с"
// @Param    to     query  string  false  "период по"
// @Success  200  {object}  map[string]interface{}
// @Failure  400  {object}  map[string]string
// @Security CookieAuth
// @Router   /api/kafka/by-node [get]
func (h *KafkaHandler) ByNode(c *gin.Context) {
	since, until, ok := resolveKafkaWindow(c)
	if !ok {
		return
	}
	r := h.uc.ByNode(c.Request.Context(), since, until)
	producers := make([]kafkaProducerDTO, 0, len(r.TopProducers))
	for _, p := range r.TopProducers {
		producers = append(producers, kafkaProducerDTO(p))
	}
	failures := make([]kafkaFailureDTO, 0, len(r.TopFailures))
	for _, f := range r.TopFailures {
		failures = append(failures, kafkaFailureDTO(f))
	}
	c.JSON(http.StatusOK, gin.H{
		"top_producers": producers, "top_failures": failures,
		"prometheus_available": r.PrometheusAvailable,
	})
}

type kafkaBrokerPingDTO struct {
	Addr      string `json:"addr"`
	ElapsedMs int64  `json:"elapsed_ms"`
	OK        bool   `json:"ok"`
	Warn      string `json:"warn,omitempty"`
}

// Test godoc
// @Summary  Ping брокеров кластера (§4.5 spec).
// @Description  Подключение + Metadata к каждому брокеру, время отклика и предупреждения. Admin-only.
// @Tags     kafka
// @Produce  json
// @Success  200  {object}  map[string]interface{}
// @Security CookieAuth
// @Router   /api/kafka/test [post]
func (h *KafkaHandler) Test(c *gin.Context) {
	r := h.uc.Test(c.Request.Context())
	brokers := make([]kafkaBrokerPingDTO, 0, len(r.Brokers))
	for _, b := range r.Brokers {
		brokers = append(brokers, kafkaBrokerPingDTO(b))
	}
	c.JSON(http.StatusOK, gin.H{"ok": r.OK, "brokers": brokers, "kafka_available": r.KafkaAvailable})
}
