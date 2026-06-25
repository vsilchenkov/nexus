package port

import (
	"context"

	"nexus/internal/domain"
)

// TeamRepo — CRUD команд и членства пользователей в командах
// (multi-tenancy v2, §16 ТЗ).
//
// CRUD команд возвращает / принимает *domain.Team. Membership-операции
// работают парой (userID, teamID); ListMembers и ListUserTeams — обратные
// проекции для UI «Команды»/«Пользователи».
type TeamRepo interface {
	// Команды.
	GetByID(ctx context.Context, id string) (*domain.Team, error)
	GetBySlug(ctx context.Context, slug string) (*domain.Team, error)
	List(ctx context.Context) ([]*domain.Team, error)
	Create(ctx context.Context, t *domain.Team) error
	Update(ctx context.Context, t *domain.Team) error
	Delete(ctx context.Context, id string) error

	// Membership.
	AddMember(ctx context.Context, userID, teamID string, role domain.TeamRole) error
	RemoveMember(ctx context.Context, userID, teamID string) error
	UpdateMemberRole(ctx context.Context, userID, teamID string, role domain.TeamRole) error
	ListMembers(ctx context.Context, teamID string) ([]*domain.TeamMember, error)
	ListUserTeams(ctx context.Context, userID string) ([]*domain.UserTeam, error)
	// ListTeamsByUsers — членства для набора пользователей одним запросом (§43.G,
	// без N+1): ключ карты — user_id. Пустой список → пустая карта.
	ListTeamsByUsers(ctx context.Context, userIDs []string) (map[string][]*domain.UserTeam, error)
}
