package clickhouse

import (
	"context"
	"fmt"
	"math"
	"strings"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// MetricsReaderCH — per-node агрегаты из ClickHouse-таблицы логов узла
// (§21: KPI и график на вкладках «Обзор»/«Метрики»). Реализует port.CHMetrics.
//
// Как и LogReaderCH, принимает ConnProvider, чтобы при hot-reload (Phase
// 6.3.2.5) запросы шли через свежий conn.
type MetricsReaderCH struct {
	conn   ConnProvider
	logger logging.Logger
}

var _ port.CHMetrics = (*MetricsReaderCH)(nil)

func NewMetricsReader(conn ConnProvider, logger logging.Logger) *MetricsReaderCH {
	return &MetricsReaderCH{conn: conn, logger: logger}
}

// liveConn возвращает текущее соединение из ConnProvider или ошибку, если
// Manager уже закрыт.
func (r *MetricsReaderCH) liveConn() (chdriver.Conn, error) {
	c := r.conn.Conn()
	if c == nil {
		return nil, fmt.Errorf("clickhouse conn is nil")
	}
	return c, nil
}

// windowConds строит условия WHERE по окну (sinceMs, untilMs] на date_request.
func windowConds(sinceMs, untilMs int64) ([]string, []any) {
	var conds []string
	var args []any
	if sinceMs > 0 {
		conds = append(conds, "toUnixTimestamp64Milli(toDateTime64(date_request, 3)) > ?")
		args = append(args, sinceMs)
	}
	if untilMs > 0 {
		conds = append(conds, "toUnixTimestamp64Milli(toDateTime64(date_request, 3)) <= ?")
		args = append(args, untilMs)
	}
	return conds, args
}

// NodeKPI — count/delivered/errors/p95/p99 за окно. Перцентили считаются
// точно через quantile() по колонке duration (мс). На пустом окне quantile
// возвращает NaN — нормализуем в 0.
func (r *MetricsReaderCH) NodeKPI(ctx context.Context, table string, sinceMs, untilMs int64) (port.NodeKPI, error) {
	var kpi port.NodeKPI
	if !isSafeTableName(table) {
		return kpi, fmt.Errorf("invalid table name: %q", table)
	}
	conds, args := windowConds(sinceMs, untilMs)
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	conn, err := r.liveConn()
	if err != nil {
		return kpi, err
	}
	q := fmt.Sprintf(`SELECT
		count() AS total,
		countIf(status BETWEEN 200 AND 299) AS delivered,
		countIf(status >= 400 OR status = 0 OR done = 0) AS errors,
		quantile(0.95)(duration) AS p95,
		quantile(0.99)(duration) AS p99
	FROM %s%s`, table, where)
	if err := conn.QueryRow(ctx, q, args...).Scan(
		&kpi.Total, &kpi.Delivered, &kpi.Errors, &kpi.P95ms, &kpi.P99ms,
	); err != nil {
		return port.NodeKPI{}, fmt.Errorf("clickhouse node kpi: %w", err)
	}
	if math.IsNaN(kpi.P95ms) {
		kpi.P95ms = 0
	}
	if math.IsNaN(kpi.P99ms) {
		kpi.P99ms = 0
	}
	return kpi, nil
}

// NodeSeries — временной ряд за окно, разбитый на buckets равных бакетов.
// Возвращает ровно buckets точек (пустые бакеты — нулевые), ASC по времени.
func (r *MetricsReaderCH) NodeSeries(ctx context.Context, table string, sinceMs, untilMs int64, buckets int) ([]port.SeriesPoint, error) {
	if !isSafeTableName(table) {
		return nil, fmt.Errorf("invalid table name: %q", table)
	}
	if buckets <= 0 {
		buckets = 1
	}
	if untilMs <= sinceMs {
		return nil, fmt.Errorf("invalid window: until <= since")
	}
	bucketMs := (untilMs - sinceMs) / int64(buckets)
	if bucketMs <= 0 {
		bucketMs = 1
	}

	// Предзаполняем все бакеты нулями — UI ждёт сплошной ряд.
	out := make([]port.SeriesPoint, buckets)
	for i := range out {
		out[i] = port.SeriesPoint{TsMs: sinceMs + int64(i)*bucketMs}
	}

	conn, err := r.liveConn()
	if err != nil {
		return nil, err
	}
	q := fmt.Sprintf(`SELECT
		intDiv(toUnixTimestamp64Milli(toDateTime64(date_request, 3)) - ?, ?) AS bucket,
		count() AS cnt,
		countIf(status >= 400 OR status = 0 OR done = 0) AS errs
	FROM %s
	WHERE toUnixTimestamp64Milli(toDateTime64(date_request, 3)) > ?
	  AND toUnixTimestamp64Milli(toDateTime64(date_request, 3)) <= ?
	GROUP BY bucket ORDER BY bucket`, table)
	rows, err := conn.Query(ctx, q, sinceMs, bucketMs, sinceMs, untilMs)
	if err != nil {
		return nil, fmt.Errorf("clickhouse node series: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			bucket int64
			cnt    uint64
			errs   uint64
		)
		if err := rows.Scan(&bucket, &cnt, &errs); err != nil {
			return nil, fmt.Errorf("scan series row: %w", err)
		}
		// ts == untilMs точно попадает в bucket == buckets — относим к последнему.
		if bucket >= int64(buckets) {
			bucket = int64(buckets) - 1
		}
		if bucket < 0 {
			continue
		}
		out[bucket].Count += cnt
		out[bucket].Errors += errs
	}
	return out, nil
}
