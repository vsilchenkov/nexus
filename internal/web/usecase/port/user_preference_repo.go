package port

import (
	"context"

	"nexus/internal/domain"
)

// UserPreferenceRepo — generic-хранилище персональных предпочтений
// пользователя (§71): настройки UI, привязанные к пользователю и, опционально,
// к команде. Отдельный малый интерфейс, а не расширение UserRepo: потребитель
// (PreferenceUsecase) использует только эти два метода, а стабы UserRepo в
// существующих тестах не обязаны знать про префы (ISP) — как SearchHistoryRepo
// относительно UserRepo.
//
// Метода удаления намеренно нет: UI сбрасывает преф записью нового значения,
// а неиспользуемый метод в порту — мёртвый код (§71.1, «вне рамок»).
type UserPreferenceRepo interface {
	// ListPreferences — все префы пользователя: и глобальные (TeamID пустой),
	// и по всем его командам. Пустой список — не ошибка. Порядок стабильный:
	// по ключу, глобальный перед командными.
	ListPreferences(ctx context.Context, userID string) ([]*domain.UserPreference, error)
	// SetPreference — upsert префа по (user_id, team_id, key).
	//
	// maxPerUser — потолок числа записей на пользователя, проверяется атомарно
	// в том же запросе (приём §62, где SaveSearchQuery принимает keep).
	// Ограничение действует только на добавление НОВОГО ключа: обновление уже
	// существующего проходит и при достигнутом потолке. Превышение →
	// domain.ErrPreferencesLimit.
	//
	// Потолок не под блокировкой: два параллельных запроса могут добавить
	// запись сверх лимита. Это защита от раздувания, а не инвариант — строгой
	// гарантии контракт не даёт.
	//
	// TeamID вне членств пользователя (в том числе гонка с исключением из
	// команды) → domain.ErrUserNotTeamMember.
	SetPreference(ctx context.Context, p *domain.UserPreference, maxPerUser int) error
}
