package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// TeamRepoPg — PG-реализация port.TeamRepo (multi-tenancy v2).
//
// CRUD по таблице teams + membership в user_teams. Запросы простые,
// JOIN'ов нет до ListMembers / ListUserTeams.
type TeamRepoPg struct {
	pool   *pgxpool.Pool
	logger logging.Logger
}

var _ port.TeamRepo = (*TeamRepoPg)(nil)

func NewTeamRepoPg(pool *pgxpool.Pool, logger logging.Logger) *TeamRepoPg {
	return &TeamRepoPg{pool: pool, logger: logger}
}

const teamCols = `id, slug, name, ch_database, created_at, updated_at`

func (r *TeamRepoPg) scanRow(row pgx.Row) (*domain.Team, error) {
	var t domain.Team
	if err := row.Scan(&t.ID, &t.Slug, &t.Name, &t.CHDatabase, &t.CreatedAt, &t.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) || isInvalidUUID(err) {
			return nil, domain.ErrTeamNotFound
		}
		return nil, fmt.Errorf("scan team: %w", err)
	}
	return &t, nil
}

func (r *TeamRepoPg) GetByID(ctx context.Context, id string) (*domain.Team, error) {
	return r.scanRow(r.pool.QueryRow(ctx,
		`SELECT `+teamCols+` FROM teams WHERE id = $1::uuid`, id))
}

func (r *TeamRepoPg) GetBySlug(ctx context.Context, slug string) (*domain.Team, error) {
	return r.scanRow(r.pool.QueryRow(ctx,
		`SELECT `+teamCols+` FROM teams WHERE slug = $1`, slug))
}

func (r *TeamRepoPg) List(ctx context.Context) ([]*domain.Team, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+teamCols+` FROM teams ORDER BY slug`)
	if err != nil {
		return nil, fmt.Errorf("list teams: %w", err)
	}
	defer rows.Close()
	var out []*domain.Team
	for rows.Next() {
		t, err := r.scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *TeamRepoPg) Create(ctx context.Context, t *domain.Team) error {
	err := r.pool.QueryRow(ctx, `
INSERT INTO teams (slug, name, ch_database)
VALUES ($1, $2, $3)
RETURNING id, created_at, updated_at`,
		t.Slug, t.Name, t.CHDatabase,
	).Scan(&t.ID, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return domain.ErrTeamAlreadyExists
		}
		return fmt.Errorf("create team: %w", err)
	}
	return nil
}

func (r *TeamRepoPg) Update(ctx context.Context, t *domain.Team) error {
	tag, err := r.pool.Exec(ctx, `
UPDATE teams SET name = $2, ch_database = $3, updated_at = now()
WHERE id = $1::uuid`,
		t.ID, t.Name, t.CHDatabase,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return domain.ErrTeamAlreadyExists
		}
		return fmt.Errorf("update team: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrTeamNotFound
	}
	return nil
}

func (r *TeamRepoPg) Delete(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM teams WHERE id = $1::uuid`, id)
	if err != nil {
		// FK-нарушение (23503): на команду ссылаются узлы (ON DELETE RESTRICT).
		// Раньше отдавали generic error → handler возвращал 500 (QA-2026-02 / П17).
		// Теперь — доменная ошибка → 409 Conflict с понятным сообщением.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return domain.ErrTeamHasNodes
		}
		return fmt.Errorf("delete team: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrTeamNotFound
	}
	return nil
}

func (r *TeamRepoPg) AddMember(ctx context.Context, userID, teamID string, role domain.TeamRole) error {
	if !role.Valid() {
		return domain.ErrTeamInvalidRole
	}
	_, err := r.pool.Exec(ctx, `
INSERT INTO user_teams (user_id, team_id, role)
VALUES ($1::uuid, $2::uuid, $3)
ON CONFLICT (user_id, team_id) DO UPDATE SET role = EXCLUDED.role`,
		userID, teamID, string(role),
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return domain.ErrNotFound
		}
		return fmt.Errorf("add team member: %w", err)
	}
	return nil
}

func (r *TeamRepoPg) RemoveMember(ctx context.Context, userID, teamID string) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM user_teams WHERE user_id = $1::uuid AND team_id = $2::uuid`,
		userID, teamID)
	if err != nil {
		return fmt.Errorf("remove team member: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrTeamMemberNotFound
	}
	// §43.H: держим users.default_team_id согласованным с членствами. Если
	// удалили команду, бывшую дефолтом пользователя, и у него остались другие
	// команды — переводим default_team_id на первую из оставшихся (ORDER BY
	// team_id, детерминированно). Если других нет — оставляем как есть (логин
	// деградирует на DefaultTeamID, рассинхрон виден в списке пользователей).
	// Подзапрос пуст ⇒ FROM не даёт строк ⇒ UPDATE не выполняется.
	if _, err := r.pool.Exec(ctx, `
UPDATE users u
SET default_team_id = sub.team_id
FROM (
	SELECT team_id FROM user_teams WHERE user_id = $1::uuid ORDER BY team_id LIMIT 1
) sub
WHERE u.id = $1::uuid AND u.default_team_id = $2::uuid`, userID, teamID); err != nil {
		return fmt.Errorf("reassign default team after member removal: %w", err)
	}
	return nil
}

func (r *TeamRepoPg) UpdateMemberRole(ctx context.Context, userID, teamID string, role domain.TeamRole) error {
	if !role.Valid() {
		return domain.ErrTeamInvalidRole
	}
	tag, err := r.pool.Exec(ctx,
		`UPDATE user_teams SET role = $3 WHERE user_id = $1::uuid AND team_id = $2::uuid`,
		userID, teamID, string(role))
	if err != nil {
		return fmt.Errorf("update team member role: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrTeamMemberNotFound
	}
	return nil
}

// ListMembers — участники команды с обогащением login/email из users
// (JOIN). Раньше отдавался только user_id, и UI показывал сырой UUID для
// пользователей, которых нет в текущей (team-scoped) выборке /api/users.
// INNER JOIN: висячих membership без users быть не может (FK на user_teams).
func (r *TeamRepoPg) ListMembers(ctx context.Context, teamID string) ([]*domain.TeamMember, error) {
	rows, err := r.pool.Query(ctx, `
SELECT ut.user_id, ut.team_id, u.login, COALESCE(u.email, ''), ut.role, ut.created_at
FROM user_teams ut
JOIN users u ON u.id = ut.user_id
WHERE ut.team_id = $1::uuid
ORDER BY ut.created_at`, teamID)
	if err != nil {
		return nil, fmt.Errorf("list team members: %w", err)
	}
	defer rows.Close()
	var out []*domain.TeamMember
	for rows.Next() {
		var m domain.TeamMember
		var role string
		if err := rows.Scan(&m.UserID, &m.TeamID, &m.Login, &m.Email, &role, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan team member: %w", err)
		}
		m.Role = domain.TeamRole(role)
		out = append(out, &m)
	}
	return out, rows.Err()
}

func (r *TeamRepoPg) ListUserTeams(ctx context.Context, userID string) ([]*domain.UserTeam, error) {
	rows, err := r.pool.Query(ctx, `
SELECT t.id, t.slug, t.name, t.ch_database, t.created_at, t.updated_at, ut.role
FROM user_teams ut
JOIN teams t ON t.id = ut.team_id
WHERE ut.user_id = $1::uuid
ORDER BY t.slug`, userID)
	if err != nil {
		return nil, fmt.Errorf("list user teams: %w", err)
	}
	defer rows.Close()
	var out []*domain.UserTeam
	for rows.Next() {
		var ut domain.UserTeam
		var role string
		if err := rows.Scan(
			&ut.Team.ID, &ut.Team.Slug, &ut.Team.Name, &ut.Team.CHDatabase,
			&ut.Team.CreatedAt, &ut.Team.UpdatedAt, &role,
		); err != nil {
			return nil, fmt.Errorf("scan user team: %w", err)
		}
		ut.Role = domain.TeamRole(role)
		out = append(out, &ut)
	}
	return out, rows.Err()
}
