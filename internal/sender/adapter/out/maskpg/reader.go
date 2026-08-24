// Package maskpg — read-only чтение справочника маскирования логов (§95) для
// Sender'а поверх PostgreSQL. Управление справочником живёт в Web; Sender лишь
// читает активные шаблоны на старте и по hot-reload секции masking.
package maskpg

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
)

// Reader читает активные шаблоны маскирования в порядке применения.
type Reader struct {
	pg     *pgxpool.Pool
	logger logging.Logger
}

func New(pg *pgxpool.Pool, logger logging.Logger) *Reader {
	return &Reader{pg: pg, logger: logger}
}

// Load возвращает включённые шаблоны (enabled) в порядке применения
// (sort_order, затем created_at). Заполняет только Pattern и Replacement —
// провайдеру больше ничего не нужно.
func (r *Reader) Load(ctx context.Context) ([]domain.LogMaskPattern, error) {
	rows, err := r.pg.Query(ctx, `
SELECT pattern, replacement
FROM log_mask_patterns
WHERE enabled
ORDER BY sort_order, created_at`)
	if err != nil {
		return nil, fmt.Errorf("load log_mask_patterns: %w", err)
	}
	defer rows.Close()
	var out []domain.LogMaskPattern
	for rows.Next() {
		var e domain.LogMaskPattern
		if err := rows.Scan(&e.Pattern, &e.Replacement); err != nil {
			return nil, fmt.Errorf("scan log_mask_patterns: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
