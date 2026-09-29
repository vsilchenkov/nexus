package usecase

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/web/usecase/port"
)

// previewThenApply — предпросмотр и применение того же файла с теми же
// опциями; mutate меняет запрос применения.
func previewThenApply(t *testing.T, f *importFixture, file []byte, mutate func(*BackupApplyRequest)) (*BackupImportResult, error) {
	t.Helper()
	p, err := f.uc.PreviewImport(context.Background(), Actor{UserLogin: "admin"}, previewRequest(file))
	require.NoError(t, err)
	req := BackupApplyRequest{BackupImportRequest: previewRequest(file), Digest: p.Digest, ConfirmHost: "Nexus-New.example.ru"}
	if mutate != nil {
		mutate(&req)
	}
	return f.uc.ApplyImport(context.Background(), Actor{UserID: "u-admin", UserLogin: "admin"}, req)
}

func TestBackupApply_ProvisionsThenWrites(t *testing.T) {
	t.Parallel()
	f := newImportFixture(t, targetSnapshot(), "kz")
	f.ch.dbs["nexus_kz_default"] = port.CHDatabaseState{Ownership: port.CHOwnershipOwned}

	res, err := previewThenApply(t, f, sealBackup(t, sourceSnapshot(t), "old", 43), nil)
	require.NoError(t, err)

	assert.Equal(t, []bool{true, false}, f.restorer.dryRun, "предпросмотр — dry-run, применение — запись")
	assert.ElementsMatch(t, []string{"nexus_kz_default", "nexus_kz_acme"}, f.prov.dbs,
		"своя база занимается повторно (идемпотентно), новая создаётся")
	assert.Equal(t, 1, res.DatabasesClaimed)
	assert.ElementsMatch(t, []string{"nexus_kz_default.orders", "nexus_kz_default.queue", "nexus_kz_acme.acme"}, f.prov.tables)
	assert.Equal(t, 3, res.TablesCreated)
	assert.Equal(t, []string{"sentry", "masking"}, f.published, "маски — отдельной секцией, без SectionAll")
	assert.Equal(t, "u-admin", f.restorer.last.AppSettings.UpdatedBy)
}

// TestBackupApply_ExistingTablesGetColumns — уцелевшая таблица не создаётся,
// а доводится до текущей схемы.
func TestBackupApply_ExistingTablesGetColumns(t *testing.T) {
	t.Parallel()
	f := newImportFixture(t, targetSnapshot(), "kz")
	f.ch.dbs["nexus_kz_acme"] = port.CHDatabaseState{Ownership: port.CHOwnershipOwned, Tables: 1}
	f.ch.tables["nexus_kz_acme.acme"] = true

	res, err := previewThenApply(t, f, sealBackup(t, sourceSnapshot(t), "old", 43), nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"nexus_kz_acme.acme"}, f.schema.ensured)
	assert.NotContains(t, f.prov.tables, "nexus_kz_acme.acme")
	assert.Equal(t, 1, res.TablesUpdated)
}

// TestBackupApply_Refusals — ни одна из проверок применения не доходит до
// ClickHouse и записи.
func TestBackupApply_Refusals(t *testing.T) {
	t.Parallel()
	file := sealBackup(t, sourceSnapshot(t), "old", 43)
	tests := []struct {
		name    string
		mutate  func(f *importFixture, req *BackupApplyRequest)
		wantErr error
	}{
		{"адрес не совпал", func(_ *importFixture, req *BackupApplyRequest) {
			req.ConfirmHost = "nexus-prod.example.ru"
		}, ErrBackupConfirmHost},
		{"пустой адрес", func(_ *importFixture, req *BackupApplyRequest) {
			req.ConfirmHost, req.SelfHost = "", ""
		}, ErrBackupConfirmHost},
		{"другие опции", func(_ *importFixture, req *BackupApplyRequest) {
			req.Options.StatusMode = BackupStatusAsFile
		}, ErrBackupDigestMismatch},
		{"другой файл", func(_ *importFixture, req *BackupApplyRequest) {
			req.File = sealBackup(t, sourceSnapshot(t), "old", 43)
		}, ErrBackupDigestMismatch},
		{"после предпросмотра появилась чужая база", func(f *importFixture, _ *BackupApplyRequest) {
			f.ch.dbs["nexus_kz_acme"] = port.CHDatabaseState{Ownership: port.CHOwnershipForeign, Owner: "msk"}
		}, ErrBackupBlocked},
		{"после предпросмотра появились узлы", func(f *importFixture, _ *BackupApplyRequest) {
			f.target.state = port.BackupTargetState{Nodes: 1, Teams: 1}
		}, port.ErrTargetNotClean},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newImportFixture(t, targetSnapshot(), "kz")
			_, err := previewThenApply(t, f, file, func(req *BackupApplyRequest) { tt.mutate(f, req) })
			require.ErrorIs(t, err, tt.wantErr)
			assert.Empty(t, f.prov.dbs, "ClickHouse не тронут")
			assert.Equal(t, []bool{true}, f.restorer.dryRun, "запись не выполнялась")
			assert.Empty(t, f.published)
		})
	}
}

// TestBackupApply_CHFailureLeavesPostgresUntouched — сбой создания таблицы
// прерывает восстановление до записи в PostgreSQL.
func TestBackupApply_CHFailureLeavesPostgresUntouched(t *testing.T) {
	t.Parallel()
	f := newImportFixture(t, targetSnapshot(), "kz")
	f.prov.failTable = "nexus_kz_acme.acme"
	_, err := previewThenApply(t, f, sealBackup(t, sourceSnapshot(t), "old", 43), nil)
	require.ErrorIs(t, err, ErrBackupCHProvision)
	assert.Equal(t, []bool{true}, f.restorer.dryRun)
}

// TestBackupApply_SurvivesClientDisconnect — закрытая вкладка не обрывает
// восстановление между ClickHouse и PostgreSQL.
func TestBackupApply_SurvivesClientDisconnect(t *testing.T) {
	t.Parallel()
	f := newImportFixture(t, targetSnapshot(), "kz")
	file := sealBackup(t, sourceSnapshot(t), "old", 43)
	p, err := f.uc.PreviewImport(context.Background(), Actor{}, previewRequest(file))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = f.uc.ApplyImport(ctx, Actor{}, BackupApplyRequest{
		BackupImportRequest: previewRequest(file), Digest: p.Digest, ConfirmHost: "nexus-new.example.ru",
	})
	require.NoError(t, err)
	assert.Equal(t, []bool{true, false}, f.restorer.dryRun)
}

func TestSameHostname(t *testing.T) {
	t.Parallel()
	tests := []struct {
		confirm, host string
		want          bool
	}{
		{"nexus.example.ru", "nexus.example.ru", true},
		{" Nexus.Example.RU ", "nexus.example.ru:8443", true},
		{"nexus.example.ru:8443", "nexus.example.ru", true},
		{"[::1]:8000", "[::1]", true},
		{"nexus.example.ru", "nexus-prod.example.ru", false},
		{"", "", false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, sameHostname(tt.confirm, tt.host), "%q vs %q", tt.confirm, tt.host)
	}
}
