package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"nexus/internal/domain/backup"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
	pgpf "nexus/internal/platform/pg"
	"nexus/internal/web/usecase/port"
)

// BackupRepoPg — чтение и запись конфигурации целиком для резервной копии
// (§110). Держит пул, а не DBTX: снимок и восстановление открывают СВОЮ
// транзакцию с особыми режимами (REPEATABLE READ READ ONLY и LOCK TABLE).
// Креды узлов и секреты настроек шифруются/расшифровываются теми же кодами
// NodeRepoPg и AppSettingsRepoPg — шифр не покидает этот пакет (§4.1).
type BackupRepoPg struct {
	pool   *pgxpool.Pool
	cipher *crypto.Cipher
	logger logging.Logger
}

var (
	_ port.BackupSnapshotReader  = (*BackupRepoPg)(nil)
	_ port.BackupTargetInspector = (*BackupRepoPg)(nil)
)

func NewBackupRepoPg(pool *pgxpool.Pool, cipher *crypto.Cipher, logger logging.Logger) *BackupRepoPg {
	return &BackupRepoPg{pool: pool, cipher: cipher, logger: logger}
}

// TargetState — счётчики конфигурации и признак чистого экземпляра.
func (r *BackupRepoPg) TargetState(ctx context.Context) (port.BackupTargetState, error) {
	var st port.BackupTargetState
	err := r.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM nodes), (SELECT count(*) FROM teams),
	(SELECT count(*) FROM users), (SELECT count(*) FROM api_tokens WHERE revoked_at IS NULL), (`+
		pgpf.FreshInstanceSQL+`)`).Scan(&st.Nodes, &st.Teams, &st.Users, &st.APITokens, &st.Fresh)
	if err != nil {
		return st, fmt.Errorf("backup target state: %w", err)
	}
	return st, nil
}

// ReadSnapshot читает всю переносимую конфигурацию одной транзакцией
// REPEATABLE READ READ ONLY: узел, созданный посреди выгрузки, попадёт в
// снимок целиком со связями либо не попадёт вовсе.
func (r *BackupRepoPg) ReadSnapshot(ctx context.Context) (*backup.Snapshot, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("backup snapshot begin: %w", err)
	}
	// Транзакция только читает: откат — штатное завершение, его ошибка не
	// меняет результат (соединение вернётся в пул в любом случае).
	defer func() { _ = tx.Rollback(ctx) }()

	s := &backup.Snapshot{}
	steps := []func(context.Context, pgx.Tx, *backup.Snapshot) error{
		readIdentity, readAccess, readCatalogs, r.readNodes, r.readAppSettings,
	}
	for _, step := range steps {
		if err := step(ctx, tx, s); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// readIdentity — команды, пользователи, членство.
func readIdentity(ctx context.Context, tx pgx.Tx, s *backup.Snapshot) error {
	var err error
	if s.Teams, err = queryRows(ctx, tx, `SELECT id, slug, name, ch_database, external_url, created_at, updated_at
FROM teams ORDER BY created_at, id`, func(row pgx.Rows, t *backup.Team) error {
		return row.Scan(&t.ID, &t.Slug, &t.Name, &t.CHDatabase, &t.ExternalURL, &t.CreatedAt, &t.UpdatedAt)
	}); err != nil {
		return fmt.Errorf("backup snapshot teams: %w", err)
	}
	// Сырой SQL, а не UserRepoPg: тот делает COALESCE и теряет NULL у email и
	// password_hash (NULL-хеш = «пароль не задан», пустая строка — нет).
	if s.Users, err = queryRows(ctx, tx, `SELECT id, login, name, email, password_hash, role, active,
	must_change_password, lang, default_team_id, created_at
FROM users ORDER BY created_at, id`, func(row pgx.Rows, u *backup.User) error {
		return row.Scan(&u.ID, &u.Login, &u.Name, &u.Email, &u.PasswordHash, &u.Role, &u.Active,
			&u.MustChangePassword, &u.Lang, &u.DefaultTeamID, &u.CreatedAt)
	}); err != nil {
		return fmt.Errorf("backup snapshot users: %w", err)
	}
	if s.UserTeams, err = queryRows(ctx, tx, `SELECT user_id, team_id, role, created_at
FROM user_teams ORDER BY user_id, team_id`, func(row pgx.Rows, m *backup.UserTeam) error {
		return row.Scan(&m.UserID, &m.TeamID, &m.Role, &m.CreatedAt)
	}); err != nil {
		return fmt.Errorf("backup snapshot user_teams: %w", err)
	}
	return nil
}

// readAccess — избранное, личные настройки, API-токены (только не отозванные).
func readAccess(ctx context.Context, tx pgx.Tx, s *backup.Snapshot) error {
	var err error
	if s.Favorites, err = queryRows(ctx, tx, `SELECT user_id, team_id, position, created_at
FROM user_team_favorites ORDER BY user_id, position`, func(row pgx.Rows, f *backup.Favorite) error {
		return row.Scan(&f.UserID, &f.TeamID, &f.Position, &f.CreatedAt)
	}); err != nil {
		return fmt.Errorf("backup snapshot favorites: %w", err)
	}
	if s.Preferences, err = queryRows(ctx, tx, `SELECT user_id, team_id, key, value, updated_at
FROM user_preferences ORDER BY user_id, key`, func(row pgx.Rows, p *backup.Preference) error {
		return row.Scan(&p.UserID, &p.TeamID, &p.Key, &p.Value, &p.UpdatedAt)
	}); err != nil {
		return fmt.Errorf("backup snapshot preferences: %w", err)
	}
	if s.APITokens, err = queryRows(ctx, tx, `SELECT id, user_id, team_id, name, token_hash, prefix, scopes,
	created_at, expires_at
FROM api_tokens WHERE revoked_at IS NULL ORDER BY created_at, id`, func(row pgx.Rows, t *backup.APIToken) error {
		return row.Scan(&t.ID, &t.UserID, &t.TeamID, &t.Name, &t.TokenHash, &t.Prefix, &t.Scopes,
			&t.CreatedAt, &t.ExpiresAt)
	}); err != nil {
		return fmt.Errorf("backup snapshot api_tokens: %w", err)
	}
	return nil
}

// readNodes — узлы (креды расшифровывает scan NodeRepoPg) и связи с хостами.
func (r *BackupRepoPg) readNodes(ctx context.Context, tx pgx.Tx, s *backup.Snapshot) error {
	repo := NewNodeRepoPg(tx, r.cipher, r.logger)
	var err error
	if s.Nodes, err = queryRows(ctx, tx, `SELECT `+nodeColumns+` FROM nodes ORDER BY team_id, path`,
		func(row pgx.Rows, b *backup.Node) error {
			n, err := repo.scan(row)
			if err != nil {
				return err
			}
			*b, err = backup.NodeFromDomain(n)
			return err
		}); err != nil {
		return fmt.Errorf("backup snapshot nodes: %w", err)
	}
	if s.NodeHosts, err = queryRows(ctx, tx, `SELECT node_id, host_id, created_at
FROM node_allowed_hosts ORDER BY node_id, host_id`, func(row pgx.Rows, l *backup.NodeHost) error {
		return row.Scan(&l.NodeID, &l.HostID, &l.CreatedAt)
	}); err != nil {
		return fmt.Errorf("backup snapshot node_allowed_hosts: %w", err)
	}
	return nil
}

// readAppSettings — глобальные настройки с расшифрованными секретами (§90.1).
func (r *BackupRepoPg) readAppSettings(ctx context.Context, tx pgx.Tx, s *backup.Snapshot) error {
	settings, err := NewAppSettingsRepoPg(tx, r.cipher, r.logger).Get(ctx)
	if err != nil {
		return fmt.Errorf("backup snapshot app_settings: %w", err)
	}
	s.AppSettings = settings
	return nil
}

// queryRows выполняет запрос и собирает строки через scan.
func queryRows[T any](ctx context.Context, db DBTX, q string, scan func(pgx.Rows, *T) error) ([]T, error) {
	rows, err := db.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		var v T
		if err := scan(rows, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
