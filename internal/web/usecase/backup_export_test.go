package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/domain/backup"
	"nexus/internal/platform/backupfile"
	"nexus/internal/platform/clock"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

const testPassphrase = "correct horse battery"

// cheapBackupKDF — минимальные допустимые параметры: тестам не нужен
// настоящий Argon2id на 64 МиБ.
func cheapBackupKDF() backupfile.KDFParams {
	k := backupfile.DefaultKDF()
	k.Time, k.MemoryKiB, k.Threads = 1, 8*1024, 1
	return k
}

type stubSnapshotReader struct {
	snap  *backup.Snapshot
	err   error
	calls int
}

func (r *stubSnapshotReader) ReadSnapshot(context.Context) (*backup.Snapshot, error) {
	r.calls++
	return r.snap, r.err
}

type stubTargetInspector struct {
	state port.BackupTargetState
	err   error
}

func (s stubTargetInspector) TargetState(context.Context) (port.BackupTargetState, error) {
	return s.state, s.err
}

var exportTime = time.Date(2026, 9, 29, 17, 30, 5, 0, time.UTC)

func newBackupUC(reader port.BackupSnapshotReader, target port.BackupTargetInspector, id domain.InstanceID) (*BackupUsecase, *stubAuditRepo) {
	auditRepo := &stubAuditRepo{}
	uc := NewBackupUsecase(BackupDeps{
		Snapshots: reader,
		Target:    target,
		Audit:     NewAuditUsecase(auditRepo, logging.NewNoop()),
		Source:    BackupSource{NexusVersion: "v1.39.0", SchemaVersion: 43, InstanceID: id, CHAvailable: true},
		Logger:    logging.NewNoop(),
	}, WithBackupKDF(cheapBackupKDF()), WithBackupClock(clock.NewFake(exportTime)))
	return uc, auditRepo
}

func exportableSnapshot() *backup.Snapshot {
	base, chHost, chPass := "https://nexus.example.ru", "ch.old", "ch-pa55"
	return &backup.Snapshot{
		Teams: []backup.Team{{ID: "t1", Slug: "default"}},
		Users: []backup.User{{ID: "u1", Login: "admin", Role: "admin", DefaultTeamID: "t1"}},
		Nodes: []backup.Node{{ID: "n1", Path: "a", TeamID: "t1", AuthType: "basic", AuthCredentials: "api:s3cret"}},
		AppSettings: &domain.AppSettings{
			General:    domain.GeneralSettings{PublicBaseURL: &base},
			ClickHouse: domain.ClickHouseSettings{Host: &chHost, Password: &chPass},
		},
	}
}

func TestBackupExport_SealsSnapshotAndAudits(t *testing.T) {
	t.Parallel()
	reader := &stubSnapshotReader{snap: exportableSnapshot()}
	uc, audit := newBackupUC(reader, stubTargetInspector{}, "kz")

	f, err := uc.Export(context.Background(), Actor{UserLogin: "root"}, testPassphrase)
	require.NoError(t, err)

	assert.Equal(t, "nexus-backup-kz-20260929-173005.json", f.Name)
	assert.NotContains(t, string(f.Data), "s3cret", "креды не лежат в файле открытым текстом")

	h, payload, err := backupfile.Open(f.Data, testPassphrase)
	require.NoError(t, err)
	assert.Equal(t, "v1.39.0", h.NexusVersion)
	assert.Equal(t, uint(43), h.SchemaVersion)
	assert.Equal(t, "kz", h.SourceInstanceID)
	assert.True(t, h.ExportedAt.Equal(exportTime))
	assert.Equal(t, 1, h.Counts["nodes"])

	var back backup.Snapshot
	require.NoError(t, json.Unmarshal(payload, &back))
	assert.Equal(t, "root", back.ExportedBy)
	assert.Equal(t, "https://nexus.example.ru", back.SourcePublicURL)
	assert.Equal(t, "api:s3cret", back.Nodes[0].AuthCredentials)
	// Подключение к ClickHouse не импортируется: адрес — подсказка для
	// предпросмотра, пароль в файл не попадает вовсе.
	require.NotNil(t, back.AppSettings.ClickHouse.Host)
	assert.Equal(t, "ch.old", *back.AppSettings.ClickHouse.Host)
	assert.Nil(t, back.AppSettings.ClickHouse.Password)
	assert.NotContains(t, string(payload), "ch-pa55")

	require.Len(t, audit.entries, 1)
	e := audit.entries[0]
	assert.Equal(t, domain.ActionConfigExport, e.Action)
	assert.Equal(t, f.Name, e.Details["file"])
	raw, err := json.Marshal(e.Details)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), testPassphrase, "пароль файла не попадает в аудит")
	assert.NotContains(t, string(raw), "s3cret")
}

func TestBackupExport_Rejects(t *testing.T) {
	t.Parallel()
	readErr := errors.New("pg down")
	broken := exportableSnapshot()
	broken.Nodes[0].TeamID = "t-missing"
	tests := []struct {
		name       string
		reader     *stubSnapshotReader
		passphrase string
		wantIs     error
		wantRead   bool
	}{
		{"короткий пароль — снимок не читается", &stubSnapshotReader{snap: exportableSnapshot()}, "short", backupfile.ErrPassphraseTooShort, false},
		{"ошибка чтения", &stubSnapshotReader{err: readErr}, testPassphrase, readErr, true},
		{"снимок не восстановится", &stubSnapshotReader{snap: broken}, testPassphrase, backup.ErrInvalidSnapshot, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			uc, audit := newBackupUC(tt.reader, stubTargetInspector{}, "kz")
			_, err := uc.Export(context.Background(), Actor{UserLogin: "root"}, tt.passphrase)
			require.ErrorIs(t, err, tt.wantIs)
			assert.Equal(t, tt.wantRead, tt.reader.calls > 0)
			assert.Empty(t, audit.entries, "неудачная выгрузка не пишется в журнал")
		})
	}
}

// TestBackupExport_WaitsForRunningOperation — вторая выгрузка ждёт первую, а
// отменённый запрос не занимает слот навсегда.
func TestBackupExport_WaitsForRunningOperation(t *testing.T) {
	t.Parallel()
	uc, _ := newBackupUC(&stubSnapshotReader{snap: exportableSnapshot()}, stubTargetInspector{}, "kz")
	release, err := uc.acquire(context.Background())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = uc.Export(ctx, Actor{}, testPassphrase)
	require.ErrorIs(t, err, context.Canceled)

	release()
	_, err = uc.Export(context.Background(), Actor{}, testPassphrase)
	require.NoError(t, err)
}

func TestBackupStatus(t *testing.T) {
	t.Parallel()
	state := port.BackupTargetState{Nodes: 3, Teams: 2, Users: 4, APITokens: 1}
	uc, _ := newBackupUC(&stubSnapshotReader{}, stubTargetInspector{state: state}, "kz")
	st, err := uc.Status(context.Background())
	require.NoError(t, err)
	assert.Equal(t, state, st.BackupTargetState)
	assert.True(t, st.CHAvailable)
	assert.Equal(t, "kz", st.InstanceID)
	assert.Equal(t, uint(43), st.SchemaVersion)

	uc, _ = newBackupUC(&stubSnapshotReader{}, stubTargetInspector{err: errors.New("boom")}, "kz")
	_, err = uc.Status(context.Background())
	require.Error(t, err)
}

func TestBackupFileName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		id   domain.InstanceID
		url  string
		want string
	}{
		{"по коду ноды", "kz", "https://nexus.example.ru", "nexus-backup-kz-20260929-173005.json"},
		{"без кода — по хосту", "", "https://Nexus.Example.ru:8443", "nexus-backup-nexus.example.ru-20260929-173005.json"},
		{"без кода и адреса", "", "", "nexus-backup-20260929-173005.json"},
		{"лишние символы в хосте отбрасываются", "", "https://a_b.ru", "nexus-backup-ab.ru-20260929-173005.json"},
		{"неразбираемый адрес", "", "https://a%22b.ru", "nexus-backup-20260929-173005.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := backupFileName(tt.id, tt.url, exportTime)
			assert.Equal(t, tt.want, got)
			assert.False(t, strings.ContainsAny(got, `"\/;`), "имя уходит в Content-Disposition")
		})
	}
}
