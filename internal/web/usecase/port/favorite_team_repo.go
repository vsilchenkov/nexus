package port

import "context"

// FavoriteTeamRepo — персональный упорядоченный список избранных команд
// пользователя (§49). Отдельный малый интерфейс, а не расширение TeamRepo:
// потребитель (AuthUsecase) использует только эти два метода, а стабы
// TeamRepo в существующих тестах не обязаны знать про избранное (ISP).
type FavoriteTeamRepo interface {
	// ListFavoriteTeamIDs — id избранных команд пользователя в сохранённом
	// порядке (position ASC). Пустой список — не ошибка.
	ListFavoriteTeamIDs(ctx context.Context, userID string) ([]string, error)
	// ReplaceFavoriteTeams — атомарная полная замена списка избранного:
	// позиция = индекс в teamIDs. Пустой teamIDs очищает избранное.
	// id вне членств пользователя → domain.ErrUserNotTeamMember (FK).
	ReplaceFavoriteTeams(ctx context.Context, userID string, teamIDs []string) error
}
