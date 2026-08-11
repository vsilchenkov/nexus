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
	// Записи с team_id IS NULL (глобальные действия admin'а, §18.1) в выдачу не
	// попадают — как и при фильтре по одной команде.
	TeamIDs    []string
	Actions    []string
	TargetType string
	TargetID   string
	From       *time.Time
	To         *time.Time
	Limit      int
	Offset     int
}

// AuditRepo — append-only журнал (§7.13). Modify / Delete операций нет
// сознательно — журнал неизменяем по контракту.
type AuditRepo interface {
	Write(ctx context.Context, e *domain.AuditEntry) error
	List(ctx context.Context, f AuditFilter) ([]*domain.AuditEntry, error)
	DeleteOlderThan(ctx context.Context, cutoff time.Time) (int, error) // только для housekeeping
}
