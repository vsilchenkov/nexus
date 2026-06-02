package port

import (
	"context"
	"time"
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
}
