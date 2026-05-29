package port

import (
	"context"
	"time"
)

// NodeKPI — сводные показатели одного узла за окно, считаются из таблицы
// логов узла в ClickHouse (§21, вкладки «Обзор»/«Метрики»).
type NodeKPI struct {
	Total     uint64  // всего записей (обработанных запросов)
	Delivered uint64  // успешно доставленных (status 2xx)
	Errors    uint64  // ошибок (status>=400 OR status=0 OR done=0)
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

// CHMetrics — per-node агрегаты из ClickHouse-таблицы логов узла.
// Реализуется адаптером adapter/out/clickhouse. Доступно только когда у узла
// настроен ClickHouseTable; иначе usecase отдаёт нулевые значения, не ошибку.
type CHMetrics interface {
	// NodeKPI — сводка за окно (sinceMs, untilMs] по date_request.
	NodeKPI(ctx context.Context, table string, sinceMs, untilMs int64) (NodeKPI, error)

	// NodeSeries — временной ряд за окно, разбитый на buckets равных бакетов
	// (ASC по времени). Пустые бакеты заполняются нулями вызывающей стороной
	// либо адаптером — контракт: длина результата == buckets.
	NodeSeries(ctx context.Context, table string, sinceMs, untilMs int64, buckets int) ([]SeriesPoint, error)
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

	// NodeThroughput — per-node in/out/errors за окно, ключ — path узла
	// (метка node в nexus_requests_total).
	NodeThroughput(ctx context.Context, window time.Duration) (map[string]NodeThroughput, error)

	// NodeErrors — per-node число «незавершённых» вызовов (done=0) за окно
	// из nexus_request_incomplete_total. Ключ — path узла (метка node).
	// Используется планировщиком Telegram-алертов (§22) вместо ClickHouse.
	NodeErrors(ctx context.Context, window time.Duration) (map[string]float64, error)

	// NodeSeries — спарклайн входящего трафика per-node: range-запрос за окно,
	// разбитый на buckets точек (один запрос на весь список, §22). Ключ — path
	// узла; длина слайса == buckets (недостающие точки — нули).
	NodeSeries(ctx context.Context, window time.Duration, buckets int) (map[string][]float64, error)
}
