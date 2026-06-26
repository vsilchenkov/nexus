package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

type UserRepoPg struct {
	pool   *pgxpool.Pool
	logger logging.Logger
}

var _ port.UserRepo = (*UserRepoPg)(nil)

func NewUserRepoPg(pool *pgxpool.Pool, logger logging.Logger) *UserRepoPg {
	return &UserRepoPg{pool: pool, logger: logger}
}

// userCols квалифицированы алиасом u — в List есть JOIN с user_teams,
// где тоже есть created_at (иначе ambiguous column).
const userCols = `u.id, u.login, COALESCE(u.email,''), COALESCE(u.password_hash,''),
	u.role, u.active, u.must_change_password, u.lang, u.default_team_id, u.created_at, u.last_login_at`

func (r *UserRepoPg) scanRow(row pgx.Row) (*domain.User, error) {
	var u domain.User
	var role, lang string
	var lastLogin *time.Time
	if err := row.Scan(
		&u.ID, &u.Login, &u.Email, &u.PasswordHash,
		&role, &u.Active, &u.MustChangePassword, &lang, &u.DefaultTeamID,
		&u.CreatedAt, &lastLogin,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) || isInvalidUUID(err) {
			return nil, domain.ErrUserNotFound
		}
		return nil, fmt.Errorf("scan user: %w", err)
	}
	u.Role = domain.UserRole(role)
	u.Lang = domain.UserLang(lang)
	u.LastLoginAt = lastLogin
	return &u, nil
}

func (r *UserRepoPg) Get(ctx context.Context, id string) (*domain.User, error) {
	return r.scanRow(r.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users u WHERE u.id = $1::uuid`, id))
}

func (r *UserRepoPg) GetByLogin(ctx context.Context, login string) (*domain.User, error) {
	return r.scanRow(r.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users u WHERE u.login = $1`, login))
}

func (r *UserRepoPg) List(ctx context.Context, f port.ListUsersFilter) ([]*domain.User, error) {
	// Phase 11.A: scope по членству в команде через JOIN user_teams.
	// userCols квалифицированы алиасом u, чтобы created_at не был
	// ambiguous с user_teams.created_at.
	q := `SELECT ` + userCols + ` FROM users u`
	args := []any{}
	if f.TeamID != "" {
		q += fmt.Sprintf(" JOIN user_teams ut ON ut.user_id = u.id AND ut.team_id = $%d::uuid", len(args)+1)
		args = append(args, f.TeamID)
	}
	q += " WHERE 1=1"
	if f.Search != "" {
		q += fmt.Sprintf(" AND (u.login ILIKE $%d OR u.email ILIKE $%d)", len(args)+1, len(args)+2)
		like := "%" + f.Search + "%"
		args = append(args, like, like)
	}
	q += " ORDER BY u.login"
	if f.Limit > 0 {
		q += fmt.Sprintf(" LIMIT $%d", len(args)+1)
		args = append(args, f.Limit)
	}
	if f.Offset > 0 {
		q += fmt.Sprintf(" OFFSET $%d", len(args)+1)
		args = append(args, f.Offset)
	}
	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	var out []*domain.User
	for rows.Next() {
		u, err := r.scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (r *UserRepoPg) CountActiveAdmins(ctx context.Context) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM users WHERE role='admin' AND active=true`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count admins: %w", err)
	}
	return n, nil
}

func (r *UserRepoPg) Create(ctx context.Context, u *domain.User) error {
	// default_team_id: если caller не передал — берём UUID 'default'-team
	// из сидинга миграции 0008. NULLIF превращает пустую строку в NULL,
	// COALESCE подставляет lookup.
	const q = `
INSERT INTO users (login, email, password_hash, role, active, must_change_password, lang, default_team_id)
VALUES ($1, NULLIF($2,''), NULLIF($3,''), $4, $5, $6, $7,
	COALESCE(NULLIF($8,'')::uuid, (SELECT id FROM teams WHERE slug = '` + domain.DefaultTeamSlug + `')))
RETURNING id, created_at`
	err := r.pool.QueryRow(ctx, q,
		u.Login, u.Email, u.PasswordHash, string(u.Role), u.Active,
		u.MustChangePassword, string(u.Lang), u.DefaultTeamID,
	).Scan(&u.ID, &u.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return domain.ErrUserAlreadyExists
		}
		return fmt.Errorf("create user: %w", err)
	}
	return nil
}

func (r *UserRepoPg) Update(ctx context.Context, u *domain.User) error {
	const q = `
UPDATE users SET
	email = NULLIF($2,''),
	role = $3,
	active = $4,
	lang = $5,
	must_change_password = $6
WHERE id = $1::uuid`
	tag, err := r.pool.Exec(ctx, q,
		u.ID, u.Email, string(u.Role), u.Active, string(u.Lang), u.MustChangePassword,
	)
	if err != nil {
		return fmt.Errorf("update user: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrUserNotFound
	}
	return nil
}

// UpdateDefaultTeam меняет default_team_id пользователя (§45). Невалидный UUID в
// id/teamID → 22P02, маппится в ErrUserNotFound (§44.I, isInvalidUUID). Членство
// в команде проверяет usecase до вызова.
func (r *UserRepoPg) UpdateDefaultTeam(ctx context.Context, userID, teamID string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE users SET default_team_id = $2::uuid WHERE id = $1::uuid`, userID, teamID)
	if err != nil {
		if isInvalidUUID(err) {
			return domain.ErrUserNotFound
		}
		return fmt.Errorf("update user default team: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrUserNotFound
	}
	return nil
}

func (r *UserRepoPg) UpdatePassword(ctx context.Context, id, passwordHash string, mustChange bool) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE users SET password_hash=$2, must_change_password=$3 WHERE id=$1::uuid`,
		id, passwordHash, mustChange)
	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrUserNotFound
	}
	return nil
}

func (r *UserRepoPg) UpdateLastLogin(ctx context.Context, id string, at time.Time) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE users SET last_login_at=$2 WHERE id=$1::uuid`, id, at)
	return err
}

func (r *UserRepoPg) Delete(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM users WHERE id=$1::uuid`, id)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrUserNotFound
	}
	return nil
}
