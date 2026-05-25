// Package clickhouse — Web-сторонний адаптер к ClickHouse для чтения логов.
// Реализует port.LogReader (replay + live-tail).
package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"bus/internal/domain"
	"bus/internal/platform/logging"
	"bus/internal/web/usecase/port"
)

// ConnProvider — узкий read-only доступ к ClickHouse-соединению.
// Определён на стороне consumer'а (CLAUDE.md §3): LogReaderCH не должен
// зависеть от конкретного владельца conn'а — clickhouse.Manager
// автоматически реализует этот интерфейс структурным совпадением.
type ConnProvider interface {
	Conn() chdriver.Conn
}

// LogReaderCH принимает ConnProvider, а не raw driver.Conn: при hot-reload
// (Phase 6.3.2.5) clickhouse.Manager swap'ает внутренний conn, и каждый
// новый запрос автоматически идёт в свежий клиент.
type LogReaderCH struct {
	conn   ConnProvider
	logger logging.Logger
}

var _ port.LogReader = (*LogReaderCH)(nil)

func NewLogReader(conn ConnProvider, logger logging.Logger) *LogReaderCH {
	return &LogReaderCH{conn: conn, logger: logger}
}

// liveConn возвращает текущее соединение из ConnProvider или ошибку, если
// Manager уже закрыт.
func (r *LogReaderCH) liveConn() (chdriver.Conn, error) {
	c := r.conn.Conn()
	if c == nil {
		return nil, fmt.Errorf("clickhouse conn is nil")
	}
	return c, nil
}

const selectCols = `ID, type, url, method, parameters, request, response,
	status, reason, date_create, date_request, date_response,
	duration, done, checksum_request, checksum_response,
	Host, IP, attempts, attempts_details`

// GetByID — одна запись по ID (UUID v4) из указанной таблицы.
//
// table — формат "db.table". Sanitized: проверяем, что состоит только из
// допустимых символов; иначе ошибка (защита от SQL-инъекции).
func (r *LogReaderCH) GetByID(ctx context.Context, table, id string) (*domain.LogRecord, error) {
	if !isSafeTableName(table) {
		return nil, fmt.Errorf("invalid table name: %q", table)
	}
	conn, err := r.liveConn()
	if err != nil {
		return nil, err
	}
	rows, err := conn.Query(ctx,
		fmt.Sprintf(`SELECT %s FROM %s WHERE ID = ? LIMIT 1`, selectCols, table), id)
	if err != nil {
		return nil, fmt.Errorf("clickhouse select log: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, domain.ErrNotFound
	}
	rec, err := scanLogRow(rows)
	if err != nil {
		return nil, err
	}
	return rec, nil
}

// ListSince — записи с date_request_unix_ms > cursor; ASC, LIMIT.
func (r *LogReaderCH) ListSince(ctx context.Context, table string, cursor int64, limit int) ([]*domain.LogRecord, error) {
	if !isSafeTableName(table) {
		return nil, fmt.Errorf("invalid table name: %q", table)
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	conn, err := r.liveConn()
	if err != nil {
		return nil, err
	}
	rows, err := conn.Query(ctx, fmt.Sprintf(
		`SELECT %s FROM %s WHERE toUnixTimestamp64Milli(toDateTime64(date_request, 3)) > ?
		 ORDER BY date_request ASC LIMIT ?`, selectCols, table), cursor, limit)
	if err != nil {
		return nil, fmt.Errorf("clickhouse list since: %w", err)
	}
	defer rows.Close()

	var out []*domain.LogRecord
	for rows.Next() {
		rec, err := scanLogRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}

func scanLogRow(rows chdriver.Rows) (*domain.LogRecord, error) {
	var (
		r          domain.LogRecord
		typ        string
		dateCreate time.Time
		dateReq    time.Time
		dateResp   time.Time
	)
	if err := rows.Scan(
		&r.ID, &typ, &r.URL, &r.Method, &r.Parameters, &r.Request, &r.Response,
		&r.Status, &r.Reason, &dateCreate, &dateReq, &dateResp,
		&r.Duration, &r.Done, &r.ChecksumRequest, &r.ChecksumResponse,
		&r.Host, &r.IP, &r.Attempts, &r.AttemptsDetails,
	); err != nil {
		return nil, fmt.Errorf("scan log row: %w", err)
	}
	r.Type = domain.RootMethod(typ)
	r.DateCreate = dateCreate
	r.DateRequest = dateReq
	r.DateResponse = dateResp
	return &r, nil
}

// isSafeTableName — db.table из A-Za-z0-9_; обе части обязательны.
func isSafeTableName(name string) bool {
	if name == "" {
		return false
	}
	dot := -1
	for i, c := range name {
		switch {
		case c == '.':
			if dot >= 0 {
				return false
			}
			dot = i
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '_':
		default:
			return false
		}
	}
	return dot > 0 && dot < len(name)-1
}

// Unused — silence unused-import warning if package compiled standalone.
var _ = errors.New
