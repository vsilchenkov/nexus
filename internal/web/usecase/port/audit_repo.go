package port

import (
	"context"
	"time"

	"nexus/internal/domain"
)

// AuditFilter — параметры фильтрации в AuditRepo.List.
//
// TeamID — multi-tenancy v2 scope (Phase 10.F.1). Пустая строка = без
// фильтра по команде; admin-handler передаёт current_team_id из сессии
// при включённой scope-фильтрации.
type AuditFilter struct {
	UserID string
	TeamID string
	// TeamIDs — сквозной скоуп «все мои команды» (§86.7). Непустой список имеет
	// приоритет над TeamID: смешивать однокомандный и многокомандный скоуп в
	// одном запросе нельзя. Приём тот же, что у ListNodesFilter.TeamIDs (§62) —
	// расширяем фильтр, а не интерфейс порта, чтобы стабы в тестах не ломались.
	//
	// Записи с team_id IS NULL (глобальные действия, §18.1) по умолчанию в выдачу
	// не попадают — как и при фильтре по одной команде. Исключение — IncludeGlobal.
	TeamIDs    []string
	Actions    []string
	TargetType string
	TargetID   string
	From       *time.Time
	To         *time.Time
	Limit      int
	Offset     int

	// IncludeGlobal — §91.2: добавить к скоупу записи без команды (входы,
	// неудачные логины, восстановление пароля, действия над пользователями).
	// Ставится только для роли admin: остальным журнал остаётся строго
	// командным. Без этого флага такие записи не видны НИ в одном режиме, кроме
	// ручного ?team_id=* — из-за чего журнал и выглядел полупустым.
	IncludeGlobal bool

	// BeforeTS/BeforeID — keyset-курсор (§91.1): выдать записи строго старше
	// указанной. Работают только парой. Предпочтительнее Offset: журнал
	// пополняется во время просмотра, и на offset-пагинации записи съезжали бы
	// между страницами. Offset сохранён для CSV-выгрузки и совместимости API.
	BeforeTS *time.Time
	BeforeID string
}

// AuditRepo — append-only журнал (§7.13). Modify / Delete операций нет
// сознательно — журнал неизменяем по контракту.
type AuditRepo interface {
	Write(ctx context.Context, e *domain.AuditEntry) error
	List(ctx context.Context, f AuditFilter) ([]*domain.AuditEntry, error)
	// Count — число записей под теми же фильтрами, без limit/курсора (§91.1).
	// Нужен счётчику «показано N из M»: список отдаёт страницу и по нему нельзя
	// понять, обрезана выдача или нет.
	Count(ctx context.Context, f AuditFilter) (int, error)
	DeleteOlderThan(ctx context.Context, cutoff time.Time) (int, error) // только для housekeeping
}
