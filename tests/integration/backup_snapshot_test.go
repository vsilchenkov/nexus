//go:build integration

package integration

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/domain/backup"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
	pgrepo "nexus/internal/web/adapter/out/postgres"
)

// TestBackupSnapshot_CoversEveryColumn — сторож полноты резервной копии
// (§110.2). Колонка, добавленная миграцией, но не попавшая ни в снимок, ни в
// список сознательно исключённых, валит тест, а не молча выпадает из файла.
// То же для таблиц: новая таблица обязана попасть в Tables или ExcludedTables.
func TestBackupSnapshot_CoversEveryColumn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	schema := publicColumns(t, ctx, pool)
	known := slices.Clone(backup.ExcludedTables())
	for _, tb := range backup.Tables() {
		known = append(known, tb.Name)
		cols, ok := schema[tb.Name]
		if !assert.True(t, ok, "таблицы %s нет в схеме — уберите её из backup.Tables", tb.Name) {
			continue
		}
		want := append(slices.Clone(tb.Exported()), tb.Excluded...)
		slices.Sort(want)
		assert.Equal(t, cols, want,
			"колонки %s: снимок + исключения должны совпадать со схемой (новую колонку — в DTO или в Excluded)", tb.Name)
	}
	for table := range schema {
		assert.Contains(t, known, table,
			"таблица %s не переносится и не исключена — решите, нужна ли она резервной копии (§110.2)", table)
	}
}

// publicColumns — таблица → отсортированные колонки схемы public.
func publicColumns(t *testing.T, ctx context.Context, pool *pgxpool.Pool) map[string][]string {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT c.table_name, c.column_name
FROM information_schema.columns c
JOIN information_schema.tables t ON t.table_schema = c.table_schema AND t.table_name = c.table_name
WHERE c.table_schema = 'public' AND t.table_type = 'BASE TABLE'
ORDER BY c.table_name, c.column_name`)
	require.NoError(t, err)
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var table, col string
		require.NoError(t, rows.Scan(&table, &col))
		out[table] = append(out[table], col)
	}
	require.NoError(t, rows.Err())
	return out
}

// TestBackupSnapshot_ReadsConfiguration — снимок несёт расшифрованные креды и
// секреты настроек, сохраняет NULL у пользователя, не берёт отозванные токены,
// и проходит собственную проверку восстановления.
func TestBackupSnapshot_ReadsConfiguration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()
	cipher, err := crypto.NewCipher(testEncryptionKey)
	require.NoError(t, err)
	logger := logging.NewNoop()

	teamID := resolveDefaultTeamID(t, ctx, pool)
	node := nonDefaultRequestNode(teamID)
	require.NoError(t, pgrepo.NewNodeRepoPg(pool, cipher, logger).Create(ctx, node))
	seedBackupRows(t, ctx, pool, teamID, node.ID)
	mailPass := "smtp-secret"
	require.NoError(t, pgrepo.NewAppSettingsRepoPg(pool, cipher, logger).Update(ctx,
		&domain.AppSettings{Mail: domain.MailSettings{Password: &mailPass}}))

	repo := pgrepo.NewBackupRepoPg(pool, cipher, logger)
	s, err := repo.ReadSnapshot(ctx)
	require.NoError(t, err)
	require.NoError(t, s.Validate(), "снимок своей базы обязан проходить проверку восстановления")

	require.Len(t, s.Nodes, 1)
	assert.Equal(t, node.ID, s.Nodes[0].ID)
	assert.Equal(t, "user:secret", s.Nodes[0].AuthCredentials, "креды расшифрованы")
	assert.Equal(t, "incoming-token", s.Nodes[0].IncomingAuthCredentials)
	spec, err := s.Nodes[0].AckSpec()
	require.NoError(t, err)
	require.NotNil(t, spec)
	assert.JSONEq(t, `{"ok": true}`, spec.Body)

	backupUser := findUser(t, s, "backup-viewer")
	assert.Nil(t, backupUser.Email, "NULL email не превращается в пустую строку")
	assert.Nil(t, backupUser.PasswordHash, "NULL-хеш = пароль не задан")

	require.Len(t, s.APITokens, 1, "отозванный токен не переносится")
	assert.Equal(t, "active-hash", s.APITokens[0].TokenHash)
	require.Len(t, s.NodeHosts, 1)
	assert.Equal(t, node.ID, s.NodeHosts[0].NodeID)
	require.Len(t, s.PeerInstances, 1)
	require.NotNil(t, s.AppSettings.Mail.Password)
	assert.Equal(t, "smtp-secret", *s.AppSettings.Mail.Password, "секрет настроек расшифрован")

	st, err := repo.TargetState(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, st.Nodes)
	assert.Equal(t, 1, st.APITokens)
	assert.False(t, st.Fresh, "экземпляр с узлом — не чистый")
}

// seedBackupRows — строки таблиц, которые снимок читает сырым SQL.
func seedBackupRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, teamID, nodeID string) {
	t.Helper()
	var userID, hostID string
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO users (login, name, role, active, default_team_id)
VALUES ('backup-viewer', 'Viewer', 'viewer', true, $1::uuid) RETURNING id::text`, teamID).Scan(&userID))
	_, err := pool.Exec(ctx, `INSERT INTO user_teams (user_id, team_id, role) VALUES ($1::uuid, $2::uuid, 'member')`,
		userID, teamID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO api_tokens (user_id, team_id, name, token_hash, prefix, revoked_at)
VALUES ($1::uuid, $2::uuid, 'active', 'active-hash', 'db_active', NULL),
       ($1::uuid, $2::uuid, 'revoked', 'revoked-hash', 'db_revoke', now())`, userID, teamID)
	require.NoError(t, err)
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO host_allowlist (pattern, kind)
VALUES ('upstream.example', 'exact') RETURNING id::text`).Scan(&hostID))
	_, err = pool.Exec(ctx, `INSERT INTO node_allowed_hosts (node_id, host_id) VALUES ($1::uuid, $2::uuid)`,
		nodeID, hostID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO peer_instances (title, base_url, last_status)
VALUES ('KZ', 'https://nexus-kz.example.ru', 'active')`)
	require.NoError(t, err)
}

func findUser(t *testing.T, s *backup.Snapshot, login string) backup.User {
	t.Helper()
	for _, u := range s.Users {
		if u.Login == login {
			return u
		}
	}
	t.Fatalf("пользователь %s не попал в снимок", login)
	return backup.User{}
}
