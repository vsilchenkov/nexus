package port

import "context"

// SearchHistoryRepo — персональная история поисковых запросов пользователя
// (§62): последние сохранённые строки поиска узлов, общие для глобального
// поиска в шапке и поля «Поиск» на странице узлов. Отдельный малый интерфейс,
// а не расширение UserRepo: потребитель (AuthUsecase) использует только эти
// три метода, а стабы UserRepo в существующих тестах не обязаны знать про
// историю (ISP) — как FavoriteTeamRepo относительно TeamRepo.
type SearchHistoryRepo interface {
	// ListSearchHistory — сохранённые запросы пользователя от свежих к старым
	// (searched_at DESC), не более limit. Пустой список — не ошибка.
	ListSearchHistory(ctx context.Context, userID string, limit int) ([]string, error)
	// SaveSearchQuery — upsert запроса (повтор поднимается наверх по searched_at)
	// с обрезкой истории до keep самых свежих записей в одной транзакции.
	SaveSearchQuery(ctx context.Context, userID, query string, keep int) error
	// ClearSearchHistory — удалить всю историю пользователя.
	ClearSearchHistory(ctx context.Context, userID string) error
}
