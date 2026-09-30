//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/backupfile"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
	pgrepo "nexus/internal/web/adapter/out/postgres"
	"nexus/internal/web/usecase"
	"nexus/internal/web/usecase/port"
)

const backupTestPassphrase = "correct horse battery"

// noCHDatabases — ClickHouse, в котором нет ни одной базы: все базы и таблицы
// будут созданы. Интеграция с настоящим ClickHouse — в сквозном тесте §110.5.
type noCHDatabases struct{}

func (noCHDatabases) InspectDatabase(context.Context, string) (port.CHDatabaseState, error) {
	return port.CHDatabaseState{Ownership: port.CHOwnershipNone}, nil
}

func (noCHDatabases) TableExists(context.Context, string) (bool, error) { return false, nil }

// Предпросмотр ClickHouse не меняет: создание и доводка схемы здесь не
// вызываются, но обязательны для восстановимости цели.
func (noCHDatabases) CreateDatabase(context.Context, string) error      { return nil }
func (noCHDatabases) CreateTable(context.Context, string, string) error { return nil }
func (noCHDatabases) EnsureColumns(context.Context, []string) error     { return nil }

func cheapKDF() backupfile.KDFParams {
	k := backupfile.DefaultKDF()
	k.Time, k.MemoryKiB, k.Threads = 1, 8*1024, 1
	return k
}

// newBackupUC — usecase резервной копии поверх настоящего PostgreSQL.
func newBackupUC(pool *pgxpool.Pool, cipher *crypto.Cipher, id domain.InstanceID) (*usecase.BackupUsecase, *pgrepo.BackupRepoPg) {
	logger := logging.NewNoop()
	repo := pgrepo.NewBackupRepoPg(pool, cipher, logger)
	audit := usecase.NewAuditUsecase(pgrepo.NewAuditRepoPg(pool, logger), logger)
	settings := usecase.NewAppSettingsUsecase(pgrepo.NewAppSettingsRepoPg(pool, cipher, logger), audit,
		nil, false, 32<<20, 32<<20, logger)
	uc := usecase.NewBackupUsecase(usecase.BackupDeps{
		Snapshots: repo, Target: repo, Restorer: repo, Locker: repo, CHInspector: noCHDatabases{}, Settings: settings,
		CHProvisioner: noCHDatabases{}, CHSchema: noCHDatabases{},
		Audit:  audit,
		Source: usecase.BackupSource{NexusVersion: "test", SchemaVersion: 43, InstanceID: id, CHAvailable: true},
		Logger: logger,
	}, usecase.WithBackupKDF(cheapKDF()))
	return uc, repo
}

// seedBackupSource — источник: вторая команда, узел с кредами, строки
// каталогов и доступа (seedBackupRows).
func seedBackupSource(t *testing.T, ctx context.Context, pool *pgxpool.Pool, cipher *crypto.Cipher) *domain.Node {
	t.Helper()
	logger := logging.NewNoop()
	team := &domain.Team{Slug: "acme", Name: "Acme", CHDatabase: domain.CHDatabaseForSlug("acme")}
	require.NoError(t, pgrepo.NewTeamRepoPg(pool, logger).Create(ctx, team))
	node := nonDefaultRequestNode(resolveDefaultTeamID(t, ctx, pool))
	node.ClickHouseTable = domain.CHDatabaseForSlug(domain.DefaultTeamSlug) + ".guard_all_fields"
	require.NoError(t, pgrepo.NewNodeRepoPg(pool, cipher, logger).Create(ctx, node))
	seedBackupRows(t, ctx, pool, resolveDefaultTeamID(t, ctx, pool), node.ID)
	return node
}

// TestBackupRestore_PreviewDryRunOnCleanInstance — выгрузка из заполненной
// базы, предпросмотр на чистой: вся запись прошла ограничения и внешние
// ключи настоящей схемы и откатилась. Затем — отказы гейта.
func TestBackupRestore_PreviewDryRunOnCleanInstance(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	srcPool, srcCleanup := startPostgres(t, ctx)
	defer srcCleanup()
	tgtPool, tgtCleanup := startPostgres(t, ctx)
	defer tgtCleanup()
	cipher, err := crypto.NewCipher(testEncryptionKey)
	require.NoError(t, err)

	seedBackupSource(t, ctx, srcPool, cipher)
	srcUC, _ := newBackupUC(srcPool, cipher, "")
	file, err := srcUC.Export(ctx, usecase.Actor{UserLogin: "root"}, backupTestPassphrase)
	require.NoError(t, err)

	tgtUC, tgtRepo := newBackupUC(tgtPool, cipher, "kz")
	p, err := tgtUC.PreviewImport(ctx, usecase.Actor{UserLogin: "admin"}, usecase.BackupImportRequest{
		File: file.Data, Passphrase: backupTestPassphrase,
	})
	require.NoError(t, err)
	require.Empty(t, p.Blockers, "dry-run на настоящей схеме: %+v", p.Blockers)
	assert.Equal(t, 1, p.Entities["nodes"].Create)
	assert.Equal(t, entityPlan(1, 1), p.Entities["teams"], "Default совпала, acme новая")

	st, err := tgtRepo.TargetState(ctx)
	require.NoError(t, err)
	assert.True(t, st.Fresh, "dry-run откатился — цель по-прежнему чистая")
	assert.Zero(t, st.Nodes)

	t.Run("импорт уже идёт", func(t *testing.T) {
		conn, err := tgtPool.Acquire(ctx)
		require.NoError(t, err)
		defer conn.Release()
		// Тот же ключ, что backupRestoreLockKey в адаптере.
		_, err = conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, int64(0x6e657875_0110))
		require.NoError(t, err)
		defer func() { _, _ = conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, int64(0x6e657875_0110)) }()
		_, err = tgtUC.PreviewImport(ctx, usecase.Actor{}, usecase.BackupImportRequest{
			File: file.Data, Passphrase: backupTestPassphrase,
		})
		require.ErrorIs(t, err, port.ErrRestoreInProgress)
	})

	t.Run("нечистая цель", func(t *testing.T) {
		_, err := srcUC.PreviewImport(ctx, usecase.Actor{}, usecase.BackupImportRequest{
			File: file.Data, Passphrase: backupTestPassphrase,
		})
		require.ErrorIs(t, err, port.ErrTargetNotClean, "источник с узлами импорт не принимает")
	})

	// Быстрая проверка usecase'а (checkRestorable) отсекает нечистую цель до
	// транзакции; здесь — последний рубеж: пересчёт под LOCK TABLE в самой
	// транзакции записи, на случай узла, созданного между проверкой и записью.
	t.Run("нечистая цель — проверка в транзакции записи", func(t *testing.T) {
		_, srcRepo := newBackupUC(srcPool, cipher, "")
		for _, dryRun := range []bool{true, false} {
			require.ErrorIs(t, srcRepo.Restore(ctx, &port.RestoreSet{}, dryRun), port.ErrTargetNotClean)
		}
	})

	// Замок операции держится на выделенном соединении всё время
	// предпросмотра или применения: вторая реплика Web на той же базе сразу
	// получает «импорт уже идёт», а транзакция Restore самой операции идёт
	// под другим ключом и замком не блокируется.
	t.Run("замок операции — один на все реплики", func(t *testing.T) {
		unlock, err := tgtRepo.LockRestore(ctx)
		require.NoError(t, err)
		_, err = tgtRepo.LockRestore(ctx)
		require.ErrorIs(t, err, port.ErrRestoreInProgress)

		replica, _ := newBackupUC(tgtPool, cipher, "kz")
		req := usecase.BackupImportRequest{File: file.Data, Passphrase: backupTestPassphrase}
		_, err = replica.PreviewImport(ctx, usecase.Actor{}, req)
		require.ErrorIs(t, err, port.ErrRestoreInProgress)

		unlock()
		p, err := replica.PreviewImport(ctx, usecase.Actor{}, req)
		require.NoError(t, err, "замок снят, соединение вернулось в пул")
		require.Empty(t, p.Blockers, "dry-run под замком операции прошёл")
	})
}

// entityPlan — ожидаемая строка плана (создать, уже есть).
func entityPlan(create, existing int) usecase.BackupEntityPlan {
	return usecase.BackupEntityPlan{Create: create, Existing: existing}
}
