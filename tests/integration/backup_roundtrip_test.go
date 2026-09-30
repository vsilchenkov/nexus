//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/bootstrap"
	chpf "nexus/internal/platform/clickhouse"
	"nexus/internal/platform/config"
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
		Snapshots: repo, Target: repo, Restorer: repo, Locker: repo,
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

// startWeb — стартовые гейты Web на этой базе и этом ClickHouse, те же, что в
// cmd/web: заявка идентичности ноды и гейт владения §70.5. На первом старте
// свежей базы гейт переименовывает базу Default в nexus_<id>_default ещё до
// того, как администратор откроет UI; после восстановления он обязан
// пропустить ноду — иначе восстановленный сервер не переживает рестарт.
func startWeb(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ch chdriver.Conn, id string) bootstrap.Identity {
	t.Helper()
	cfg := &config.Config{}
	cfg.Instance.ID = id
	identity := bootstrap.MustInstanceIdentity(ctx, pool, cfg, config.Flags{}, "web", logging.NewNoop())
	require.NoError(t, bootstrap.CheckCHOwnership(ctx, pool, ch, cfg, identity, logging.NewNoop()),
		"гейт владения пропускает Web")
	return identity
}

// firstStart — первый старт ноды с instance.id на свежей PostgreSQL.
func firstStart(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ch chdriver.Conn, id string) {
	t.Helper()
	identity := startWeb(t, ctx, pool, ch, id)
	require.True(t, identity.FreshPG, "первый старт — на свежей базе")
}

// restartAfterRestore — рестарт восстановленной ноды: база уже не свежая,
// имена баз команд — ровно те, что требует гейт для этого идентификатора.
func restartAfterRestore(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ch chdriver.Conn, id string) {
	t.Helper()
	identity := startWeb(t, ctx, pool, ch, id)
	assert.False(t, identity.FreshPG)
	assert.True(t, identity.CHClaimed)
	rows, err := pool.Query(ctx, `SELECT slug, ch_database FROM teams`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var slug, db string
		require.NoError(t, rows.Scan(&slug, &db))
		assert.Equal(t, domain.InstanceID(id).CHDatabase(slug), db, "база команды %s", slug)
	}
	require.NoError(t, rows.Err())
}

// createLegacyLogTable — таблица логов старой версии: есть type и IP (к ним
// привязаны AFTER доводки), нет колонок, добавленных позже (§39 http_method,
// §67 client_host, §42.10 размеры тел).
func createLegacyLogTable(t *testing.T, ctx context.Context, conn chdriver.Conn, table string) {
	t.Helper()
	require.NoError(t, conn.Exec(ctx, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
	ID String, type String, url String, method String, parameters String, request String,
	response String, status Int32, reason String, date_create Date, date_request DateTime,
	date_response DateTime, duration Int32, done UInt8, checksum_request FixedString(32),
	checksum_response FixedString(32), Host String, IP String, attempts Int32,
	attempts_details String, node_id String
) ENGINE = MergeTree PARTITION BY toYYYYMM(date_create) ORDER BY (date_create, ID)`, table)))
}

func columnExists(t *testing.T, ctx context.Context, ch chdriver.Conn, db, table, column string) bool {
	t.Helper()
	var n uint64
	require.NoError(t, ch.QueryRow(ctx, `SELECT count() FROM system.columns
WHERE database = ? AND table = ? AND name = ?`, db, table, column).Scan(&n))
	return n == 1
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
		firstStart(t, ctx, pool, ch, "nw")
		res := restoreAll(t, ctx, newRestoreUC(pool, tgtCipher, "nw", ch), src.file)
		assert.Equal(t, 2, res.TablesCreated)
		restartAfterRestore(t, ctx, pool, ch, "nw")

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
		createLegacyLogTable(t, ctx, ch, "nexus_kz_acme.orders")
		insertOldRow(t, ctx, ch, "nexus_kz_acme.orders", "old-log")

		pool, cleanup := startPostgres(t, ctx)
		defer cleanup()
		firstStart(t, ctx, pool, ch, "kz")
		res := restoreAll(t, ctx, newRestoreUC(pool, cipher, "kz", ch), src.file)
		assert.Equal(t, 1, res.TablesCreated)
		assert.Equal(t, 1, res.TablesUpdated, "уцелевшая таблица доводится до схемы, а не создаётся")
		assert.Equal(t, uint64(1), chRowCount(t, ctx, ch, "nexus_kz_acme.orders"), "старые логи на месте")
		for _, col := range []string{"http_method", "client_host", "request_size", "response_size"} {
			assert.True(t, columnExists(t, ctx, ch, "nexus_kz_acme", "orders", col), "колонка %s добавлена", col)
		}
		restartAfterRestore(t, ctx, pool, ch, "kz")
	})

	// Источник с тем же идентификатором, но база команды в файле не по нему
	// (источник стартовал без ClickHouse, сидированную базу не переименовали,
	// или имя правили вручную). Раньше имя бралось из файла как есть, и первый
	// же рестарт восстановленной ноды падал на гейте владения.
	t.Run("тот же instance.id, база в файле не по идентификатору", func(t *testing.T) {
		_, err := srcPool.Exec(ctx, `UPDATE teams SET ch_database = 'nexus_acme' WHERE slug = 'acme'`)
		require.NoError(t, err)
		_, err = srcPool.Exec(ctx, `UPDATE nodes SET clickhouse_table = 'nexus_acme.orders' WHERE id = $1::uuid`, src.acmeNode.ID)
		require.NoError(t, err)
		f, err := newRestoreUC(srcPool, cipher, "kz", nil).Export(ctx, usecase.Actor{UserLogin: "root"}, backupTestPassphrase)
		require.NoError(t, err)

		pool, cleanup := startPostgres(t, ctx)
		defer cleanup()
		firstStart(t, ctx, pool, ch, "kz")
		res := restoreAll(t, ctx, newRestoreUC(pool, cipher, "kz", ch), f.Data)
		assert.Contains(t, res.Preview.Warnings, usecase.BackupIssue{
			Code: "ch_database_renamed", Subject: "nexus_acme", Detail: "nexus_kz_acme",
		})
		got, err := pgrepo.NewNodeRepoPg(pool, cipher, logging.NewNoop()).Get(ctx, src.acmeNode.ID)
		require.NoError(t, err)
		assert.Equal(t, "nexus_kz_acme.orders", got.ClickHouseTable)
		restartAfterRestore(t, ctx, pool, ch, "kz")
	})
}

// TestBackupSchemaMaintainer_ReportsFailures — доводка схемы для
// восстановления возвращает ошибку ALTER, а не только пишет её в лог:
// восстановление, доложившее «таблицы дополнены», не должно оставить узел с
// падающим чтением логов. Остальные таблицы при этом доводятся.
func TestBackupSchemaMaintainer_ReportsFailures(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	ch, _, cleanup := startClickHouse(t, ctx)
	defer cleanup()
	require.NoError(t, ch.Exec(ctx, "CREATE DATABASE IF NOT EXISTS nexus_sm"))
	createLegacyLogTable(t, ctx, ch, "nexus_sm.legacy")
	createLogTable(t, ctx, ch, "nexus_sm.no_type") // без type и IP: AFTER-доводки невыполнимы

	m := webch.NewSchemaMaintainer(fixedConn{ch}, logging.NewNoop())
	require.NoError(t, m.EnsureColumns(ctx, []string{"nexus_sm.legacy"}))
	assert.True(t, columnExists(t, ctx, ch, "nexus_sm", "legacy", "client_host"))

	err := m.EnsureColumns(ctx, []string{"nexus_sm.no_type", "nexus_sm.missing", "nexus_sm.legacy"})
	require.Error(t, err)
	assert.ErrorContains(t, err, "nexus_sm.no_type")
	assert.ErrorContains(t, err, "nexus_sm.missing")
	assert.NotContains(t, err.Error(), "nexus_sm.legacy", "доведённая таблица в ошибке не значится")
	assert.True(t, columnExists(t, ctx, ch, "nexus_sm", "no_type", "request_size"),
		"доводки без AFTER выполнены и на таблице с ошибкой")
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
