package port

import (
	"context"

	"bus/internal/domain"
)

// LogReader — read-only доступ к ClickHouse-логам узлов (§7.4 ТЗ).
// Используется replay (§7.4.1) и live-tail (§7.4).
type LogReader interface {
	// GetByID — найти одну запись в указанной таблице.
	GetByID(ctx context.Context, table, id string) (*domain.LogRecord, error)

	// ListSince — все записи после cursor (date_request > cursor) по таблице,
	// упорядоченные по date_request ASC. Используется для SSE live-tail.
	ListSince(ctx context.Context, table string, cursor int64, limit int) ([]*domain.LogRecord, error)
}
