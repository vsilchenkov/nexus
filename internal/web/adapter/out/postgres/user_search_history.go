package postgres

import (
	"context"
	"fmt"

	"nexus/internal/web/usecase/port"
)

// SearchHistoryRepo (§62) лёг на UserRepoPg — зеркало §49, где FavoriteTeamRepo
// лёг на TeamRepoPg: история привязана к пользователю, а не к команде.
var _ port.SearchHistoryRepo = (*UserRepoPg)(nil)

// ListSearchHistory — сохранённые запросы пользователя от свежих к старым (§62).
func (r *UserRepoPg) ListSearchHistory(ctx context.Context, userID string, limit int) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
SELECT query
FROM user_search_history
WHERE user_id = $1::uuid
ORDER BY searched_at DESC
LIMIT $2`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("list search history: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var q string
		if err := rows.Scan(&q); err != nil {
			return nil, fmt.Errorf("scan search history: %w", err)
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// SaveSearchQuery — upsert запроса с обрезкой истории до keep свежих записей в
// одной транзакции (§62). ON CONFLICT поднимает повтор наверх по searched_at;
// DELETE вычищает всё, что не попало в keep самых свежих. Нормализацию строки
// (trim, лимит длины) делает usecase — сюда приходит уже валидная непустая
// строка.
func (r *UserRepoPg) SaveSearchQuery(ctx context.Context, userID, query string, keep int) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("save search query: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op после успешного Commit

	if _, err := tx.Exec(ctx, `
INSERT INTO user_search_history (user_id, query, searched_at)
VALUES ($1::uuid, $2, now())
ON CONFLICT (user_id, query) DO UPDATE SET searched_at = now()`, userID, query); err != nil {
		return fmt.Errorf("save search query: upsert: %w", err)
	}
	if _, err := tx.Exec(ctx, `
DELETE FROM user_search_history
WHERE user_id = $1::uuid AND query NOT IN (
	SELECT query FROM user_search_history
	WHERE user_id = $1::uuid
	ORDER BY searched_at DESC
	LIMIT $2)`, userID, keep); err != nil {
		return fmt.Errorf("save search query: trim: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("save search query: commit: %w", err)
	}
	return nil
}

// ClearSearchHistory — удалить всю историю поиска пользователя (§62).
func (r *UserRepoPg) ClearSearchHistory(ctx context.Context, userID string) error {
	if _, err := r.pool.Exec(ctx,
		`DELETE FROM user_search_history WHERE user_id = $1::uuid`, userID); err != nil {
		return fmt.Errorf("clear search history: %w", err)
	}
	return nil
}
