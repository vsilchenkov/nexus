//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	chpf "nexus/internal/platform/clickhouse"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
	webch "nexus/internal/web/adapter/out/clickhouse"
	pgrepo "nexus/internal/web/adapter/out/postgres"
	"nexus/internal/web/usecase"
	"nexus/internal/web/usecase/port"
)

// newRestoreUC — usecase резервной копии цели с настоящим ClickHouse: гейт
// владения ноды id, провижинер с захватом баз, доводка схемы.
func newRestoreUC(pool *pgxpool.Pool, cipher *crypto.Cipher, id string, ch chdriver.Conn) *usecase.BackupUsecase {
	logger := logging.NewNoop()
	repo := pgrepo.NewBackupRepoPg(pool, cipher, logger)
	audit := usecase.NewAuditUsecase(pgrepo.NewAuditRepoPg(pool, logger), logger)
	settings := usecase.NewAppSettingsUsecase(pgrepo.NewAppSettingsRepoPg(pool, cipher, logger), audit,
		nil, false, 32<<20, 32<<20, logger)
	guard := newGuard(ch, id)
	prov := webch.NewTeamProvisioner(fixedConn{ch}, logger)
	prov.SetOwnership(guard)
	return usecase.NewBackupUsecase(usecase.BackupDeps{
		Snapshots: repo, Target: repo, Restorer: repo,
		CHInspector:   webch.NewBackupInspector(fixedConn{ch}, guard, logger),
		CHProvisioner: prov,
		CHSchema:      webch.NewSchemaMaintainer(fixedConn{ch}, logger),
		Settings:      settings,
		Audit:         audit,
		Source:        usecase.BackupSource{NexusVersion: "test", SchemaVersion: 43, InstanceID: domain.InstanceID(id), CHAvailable: true},
		Logger:        logger,
	}, usecase.WithBackupKDF(cheapKDF()))
}

// roundTripSource — источник с идентификатором kz: базы nexus_kz_*, узел в
// Default и узел в acme, пользователь с bcrypt-хешем, токен, своя маска,
// настройки с секретом и адресом ClickHouse.
type roundTripSource struct {
	defaultNode, acmeNode *domain.Node
	file                  []byte
}

func seedRoundTripSource(t *testing.T, ctx context.Context, pool *pgxpool.Pool, cipher *crypto.Cipher) roundTripSource {
	t.Helper()
	logger := logging.NewNoop()
	defaultTeam := resolveDefaultTeamID(t, ctx, pool)
	_, err := pool.Exec(ctx, `UPDATE teams SET ch_database = 'nexus_kz_default' WHERE id = $1::uuid`, defaultTeam)
	require.NoError(t, err)
	acme := &domain.Team{Slug: "acme", Name: "Acme", CHDatabase: "nexus_kz_acme"}
	require.NoError(t, pgrepo.NewTeamRepoPg(pool, logger).Create(ctx, acme))

	nodes := pgrepo.NewNodeRepoPg(pool, cipher, logger)
	src := roundTripSource{defaultNode: nonDefaultRequestNode(defaultTeam), acmeNode: nonDefaultRequestNode(acme.ID)}
	src.defaultNode.ClickHouseTable = "nexus_kz_default.guard_all_fields"
	src.acmeNode.Path, src.acmeNode.ClickHouseTable = "acme/orders", "nexus_kz_acme.orders"
	require.NoError(t, nodes.Create(ctx, src.defaultNode))
	require.NoError(t, nodes.Create(ctx, src.acmeNode))
	seedBackupRows(t, ctx, pool, defaultTeam, src.defaultNode.ID)

	_, err = pool.Exec(ctx, `INSERT INTO users (login, name, password_hash, role, active, default_team_id)
VALUES ('ivanov', 'Иванов', '$2a$10$abcdefghijklmnopqrstuuOaJmGmJm5ZqQeSxlJqWlM6C6wP3a1Ku', 'manager', true, $1::uuid)`, acme.ID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO log_mask_patterns (pattern, replacement, description)
VALUES ('secret=\w+', 'secret=***', 'custom')`)
	require.NoError(t, err)
	env, chHost, dsn := "prod", "ch.old", "https://key@sentry.example/1"
	require.NoError(t, pgrepo.NewAppSettingsRepoPg(pool, cipher, logger).Update(ctx, &domain.AppSettings{
		Sentry:     domain.SentrySettings{Environment: &env, DSN: &dsn},
		ClickHouse: domain.ClickHouseSettings{Host: &chHost},
	}))

	exporter := newRestoreUC(pool, cipher, "kz", nil)
	f, err := exporter.Export(ctx, usecase.Actor{UserLogin: "root"}, backupTestPassphrase)
	require.NoError(t, err)
	src.file = f.Data
	return src
}

// firstStart — эффект первого старта ноды с instance.id на свежей PostgreSQL:
// rebaseSeededTeamDB (bootstrap/ch_ownership.go) переименовывает базу Default
// в nexus_<id>_default ещё до того, как администратор откроет UI.
func firstStart(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id string) {
	t.Helper()
	_, err := pool.Exec(ctx, `UPDATE teams SET ch_database = $1 WHERE slug = 'default'`,
		domain.InstanceID(id).CHDatabase("default"))
	require.NoError(t, err)
}

func restoreAll(t *testing.T, ctx context.Context, uc *usecase.BackupUsecase, file []byte) *usecase.BackupImportResult {
	t.Helper()
	req := usecase.BackupImportRequest{File: file, Passphrase: backupTestPassphrase, SelfHost: "nexus-new:8000"}
	p, err := uc.PreviewImport(ctx, usecase.Actor{UserLogin: "admin"}, req)
	require.NoError(t, err)
	require.Empty(t, p.Blockers, "%+v", p.Blockers)
	res, err := uc.ApplyImport(ctx, usecase.Actor{UserLogin: "admin"}, usecase.BackupApplyRequest{
		BackupImportRequest: req, Digest: p.Digest, ConfirmHost: "nexus-new",
	})
	require.NoError(t, err)
	return res
}

// TestBackupRoundTrip — выгрузка → восстановление на чистую базу: узлы с
// теми же UUID и рабочими кредами, базы и таблицы ClickHouse, пользователи,
// токены, маски, настройки. Две цели на одном ClickHouse: с другим
// идентификатором (новые базы) и с тем же (уцелевшая база и её логи).
func TestBackupRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	srcPool, c1 := startPostgres(t, ctx)
	defer c1()
	ch, _, c2 := startClickHouse(t, ctx)
	defer c2()
	cipher, err := crypto.NewCipher(testEncryptionKey)
	require.NoError(t, err)
	src := seedRoundTripSource(t, ctx, srcPool, cipher)

	t.Run("другой instance.id — новые базы", func(t *testing.T) {
		pool, cleanup := startPostgres(t, ctx)
		defer cleanup()
		// Цель поднята с другим ENCRYPTION_KEY: креды в файле не зависят от ключа.
		tgtCipher, err := crypto.NewCipher("ZmVkY2JhOTg3NjU0MzIxMGZlZGNiYTk4NzY1NDMyMTA=")
		require.NoError(t, err)
		firstStart(t, ctx, pool, "nw")
		res := restoreAll(t, ctx, newRestoreUC(pool, tgtCipher, "nw", ch), src.file)
		assert.Equal(t, 2, res.TablesCreated)

		got, err := pgrepo.NewNodeRepoPg(pool, tgtCipher, logging.NewNoop()).Get(ctx, src.acmeNode.ID)
		require.NoError(t, err, "UUID узла сохранён")
		assert.Equal(t, "user:secret", got.AuthCredentials, "креды расшифровываются ключом цели")
		assert.Equal(t, "nexus_nw_acme.orders", got.ClickHouseTable)
		assertOwnedTable(t, ctx, ch, "nw", "nexus_nw_acme", "orders")
		assertOwnedTable(t, ctx, ch, "nw", "nexus_nw_default", "guard_all_fields")
		assertRestoredRows(t, ctx, pool, tgtCipher, srcPool)

		_, err = newRestoreUC(pool, tgtCipher, "nw", ch).PreviewImport(ctx, usecase.Actor{},
			usecase.BackupImportRequest{File: src.file, Passphrase: backupTestPassphrase})
		require.ErrorIs(t, err, port.ErrTargetNotClean, "повторный импорт на ту же ноду отклоняется")
	})

	t.Run("тот же instance.id — уцелевшая база и её логи", func(t *testing.T) {
		require.NoError(t, newGuard(ch, "kz").Claim(ctx, "nexus_kz_acme"))
		createLogTable(t, ctx, ch, "nexus_kz_acme.orders")
		insertOldRow(t, ctx, ch, "nexus_kz_acme.orders", "old-log")

		pool, cleanup := startPostgres(t, ctx)
		defer cleanup()
		firstStart(t, ctx, pool, "kz")
		res := restoreAll(t, ctx, newRestoreUC(pool, cipher, "kz", ch), src.file)
		assert.Equal(t, 1, res.TablesCreated)
		assert.Equal(t, 1, res.TablesUpdated, "уцелевшая таблица доводится до схемы, а не создаётся")
		assert.Equal(t, uint64(1), chRowCount(t, ctx, ch, "nexus_kz_acme.orders"), "старые логи на месте")

		var cols uint64
		require.NoError(t, ch.QueryRow(ctx, `SELECT count() FROM system.columns
WHERE database = 'nexus_kz_acme' AND table = 'orders' AND name = 'request_size'`).Scan(&cols))
		assert.Equal(t, uint64(1), cols, "недостающая колонка добавлена")
	})
}

func assertOwnedTable(t *testing.T, ctx context.Context, ch chdriver.Conn, id, db, table string) {
	t.Helper()
	v, _, err := newGuard(ch, id).Check(ctx, db)
	require.NoError(t, err)
	assert.Equal(t, chpf.VerdictOwned, v, "база %s занята маркером цели", db)
	var n uint64
	require.NoError(t, ch.QueryRow(ctx, `SELECT count() FROM system.tables WHERE database = ? AND name = ?`, db, table).Scan(&n))
	assert.Equal(t, uint64(1), n, "таблица %s.%s создана", db, table)
}

// assertRestoredRows — пользователи, токены, маски, настройки и аудит цели.
func assertRestoredRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, cipher *crypto.Cipher, srcPool *pgxpool.Pool) {
	t.Helper()
	var hash string
	require.NoError(t, pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE login = 'ivanov'`).Scan(&hash))
	assert.Equal(t, "$2a$10$abcdefghijklmnopqrstuuOaJmGmJm5ZqQeSxlJqWlM6C6wP3a1Ku", hash, "вход старым паролем")
	var tokens int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM api_tokens WHERE token_hash = 'active-hash'`).Scan(&tokens))
	assert.Equal(t, 1, tokens)

	masks := func(p *pgxpool.Pool) []string {
		rows, err := p.Query(ctx, `SELECT id::text FROM log_mask_patterns ORDER BY id`)
		require.NoError(t, err)
		defer rows.Close()
		var ids []string
		for rows.Next() {
			var id string
			require.NoError(t, rows.Scan(&id))
			ids = append(ids, id)
		}
		return ids
	}
	assert.Equal(t, masks(srcPool), masks(pool), "набор масок заменён целиком")

	s, err := pgrepo.NewAppSettingsRepoPg(pool, cipher, logging.NewNoop()).Get(ctx)
	require.NoError(t, err)
	require.NotNil(t, s.Sentry.DSN)
	assert.Equal(t, "https://key@sentry.example/1", *s.Sentry.DSN, "секрет настроек перешифрован ключом цели")
	assert.Nil(t, s.ClickHouse.Host, "адрес ClickHouse из файла не переносится")

	var audit int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM user_audit WHERE action = 'config.import'`).Scan(&audit))
	assert.Equal(t, 1, audit)
}
