package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/domain/backup"
	"nexus/internal/platform/backupfile"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// id строк файла (источник) и цели. Validate узла требует UUID у команды,
// шаблона и группы.
const (
	srcDefaultTeam = "11111111-1111-4111-8111-111111111111"
	srcAcmeTeam    = "22222222-2222-4222-8222-222222222222"
	srcTplStd      = "33333333-3333-4333-8333-333333333333"
	srcTplCustom   = "44444444-4444-4444-8444-444444444444"
	srcGroup       = "55555555-5555-4555-8555-555555555555"
	srcHost        = "66666666-6666-4666-8666-666666666666"
	srcAdmin       = "77777777-7777-4777-8777-777777777777"
	srcIvanov      = "88888888-8888-4888-8888-888888888888"
	nodeOrders     = "99999999-9999-4999-8999-999999999999"
	nodePull       = "9a9a9a9a-9a9a-4a9a-8a9a-9a9a9a9a9a9a"
	nodeAcme       = "9b9b9b9b-9b9b-4b9b-8b9b-9b9b9b9b9b9b"

	tgtDefaultTeam = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	tgtTplStd      = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	tgtAdmin       = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
)

// fakeRestorer — запись конфигурации: запоминает вызовы.
type fakeRestorer struct {
	err    error
	calls  int
	dryRun []bool
	last   *port.RestoreSet
}

func (r *fakeRestorer) Restore(_ context.Context, set *port.RestoreSet, dryRun bool) error {
	r.calls++
	r.dryRun = append(r.dryRun, dryRun)
	r.last = set
	return r.err
}

// fakeCHInspector — базы по имени; таблицы — множество существующих.
type fakeCHInspector struct {
	dbs    map[string]port.CHDatabaseState
	dbErr  error
	tables map[string]bool
}

func (f *fakeCHInspector) InspectDatabase(_ context.Context, db string) (port.CHDatabaseState, error) {
	if f.dbErr != nil {
		return port.CHDatabaseState{}, f.dbErr
	}
	if st, ok := f.dbs[db]; ok {
		return st, nil
	}
	return port.CHDatabaseState{Ownership: port.CHOwnershipNone}, nil
}

func (f *fakeCHInspector) TableExists(_ context.Context, table string) (bool, error) {
	return f.tables[table], nil
}

// passSettings — PrepareImport без проверок: настройки файла как есть.
type passSettings struct{}

func (passSettings) PrepareImport(_ context.Context, p *domain.AppSettings) (*domain.AppSettings, []string, []SettingsFieldSkip, error) {
	p.ClickHouse = domain.ClickHouseSettings{}
	if p.Sentry.Environment == nil {
		return p, nil, nil, nil
	}
	return p, []string{"sentry"}, nil, nil
}

type importFixture struct {
	uc       *BackupUsecase
	restorer *fakeRestorer
	ch       *fakeCHInspector
	target   *stubTargetInspector
}

func newImportFixture(t *testing.T, tgt *backup.Snapshot, id domain.InstanceID) *importFixture {
	t.Helper()
	f := &importFixture{
		restorer: &fakeRestorer{},
		ch:       &fakeCHInspector{dbs: map[string]port.CHDatabaseState{}, tables: map[string]bool{}},
		target:   &stubTargetInspector{state: port.BackupTargetState{Teams: 1, Fresh: true}},
	}
	f.uc = NewBackupUsecase(BackupDeps{
		Snapshots:        &stubSnapshotReader{snap: tgt},
		Target:           f.target,
		Restorer:         f.restorer,
		CHInspector:      f.ch,
		Settings:         passSettings{},
		Audit:            NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()),
		Source:           BackupSource{NexusVersion: "v1.40.0", SchemaVersion: 43, InstanceID: id, CHAvailable: true},
		SelfIngressHosts: []string{"nexus-new.example.ru"},
		Logger:           logging.NewNoop(),
	}, WithBackupKDF(cheapBackupKDF()))
	return f
}

// targetSnapshot — чистая цель: сиды миграций (Default, admin, «Standard
// logs», две маски) с id, отличными от id источника.
func targetSnapshot() *backup.Snapshot {
	spec, _ := json.Marshal(domain.DefaultCHTemplateSpec())
	return &backup.Snapshot{
		Teams:       []backup.Team{{ID: tgtDefaultTeam, Slug: "default", Name: "Default", CHDatabase: "nexus_kz_default"}},
		Users:       []backup.User{{ID: tgtAdmin, Login: "admin", Role: "admin", DefaultTeamID: tgtDefaultTeam}},
		UserTeams:   []backup.UserTeam{{UserID: tgtAdmin, TeamID: tgtDefaultTeam, Role: "owner"}},
		CHTemplates: []backup.CHTemplate{{ID: tgtTplStd, Name: "Standard logs", Spec: spec, IsDefault: true}},
		LogMasks:    []backup.LogMask{{ID: "m1"}, {ID: "m2"}},
		AppSettings: &domain.AppSettings{},
	}
}

func backupSourceNode(t *testing.T, id, teamID, path, table string) backup.Node {
	t.Helper()
	n := &domain.Node{
		ID: id, Path: path, TeamID: teamID, RootMethod: domain.RootMethodRequest,
		URLMode: domain.URLModeStatic, TargetURL: "https://api.example.com/" + path,
		AuthType: domain.AuthTypeBasic, AuthCredentials: "api:s3cret",
		IncomingAuthType: domain.IncomingAuthTypeNone, Status: domain.NodeStatusEnabled,
		ClickHouseTable: table, ClickHouseTemplateID: srcTplCustom, LoggingEnabled: true,
		GroupID: srcGroup, CreatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	n.SetDefaults()
	require.NoError(t, n.Validate(), "фикстура узла обязана быть валидной")
	b, err := backup.NodeFromDomain(n)
	require.NoError(t, err)
	return b
}

// sourceSnapshot — источник с идентификатором "old": Default и acme, admin
// и ivanov, стандартный и свой шаблон, группа, хост, три узла (один —
// RabbitMQ), токен, два соседа (один из них — адрес цели).
func sourceSnapshot(t *testing.T) *backup.Snapshot {
	t.Helper()
	spec, _ := json.Marshal(domain.DefaultCHTemplateSpec())
	pull := backupSourceNode(t, nodePull, srcDefaultTeam, "queue", "nexus_old_default.queue")
	pull.RootMethod = string(domain.RootMethodRabbitMQAsync)
	pull.RMQHost, pull.RMQQueue = "rmq.example.com", "orders"
	pull.PullIntervalSec, pull.PullBatchSize, pull.PullPrefetch = 15, 10, 10
	return &backup.Snapshot{
		ExportedBy: "root",
		Teams: []backup.Team{
			{ID: srcDefaultTeam, Slug: "default", Name: "Главная", CHDatabase: "nexus_old_default"},
			{ID: srcAcmeTeam, Slug: "acme", Name: "Acme", CHDatabase: "nexus_old_acme"},
		},
		Users: []backup.User{
			{ID: srcAdmin, Login: "admin", Role: "admin", DefaultTeamID: srcDefaultTeam},
			{ID: srcIvanov, Login: "ivanov", Role: "viewer", DefaultTeamID: srcAcmeTeam},
		},
		UserTeams: []backup.UserTeam{
			{UserID: srcAdmin, TeamID: srcDefaultTeam, Role: "owner"},
			{UserID: srcIvanov, TeamID: srcAcmeTeam, Role: "member"},
		},
		Favorites: []backup.Favorite{
			{UserID: srcIvanov, TeamID: srcAcmeTeam},
			{UserID: srcIvanov, TeamID: srcDefaultTeam}, // без членства — пропускается
		},
		APITokens: []backup.APIToken{{ID: "t1", UserID: srcIvanov, TeamID: srcAcmeTeam, Name: "ci", TokenHash: "h1"}},
		CHTemplates: []backup.CHTemplate{
			{ID: srcTplStd, Name: "Standard logs", Spec: spec},
			{ID: srcTplCustom, Name: "Compact", Spec: spec, IsDefault: true},
		},
		NodeGroups: []backup.NodeGroup{{ID: srcGroup, Name: "ERP"}},
		Hosts:      []backup.Host{{ID: srcHost, Pattern: "API.example.com", Kind: "exact"}},
		LogMasks:   []backup.LogMask{{ID: "m-src"}},
		PeerInstances: []backup.PeerInstance{
			{ID: "p1", BaseURL: "https://nexus-msk.example.ru"},
			{ID: "p2", BaseURL: "https://nexus-new.example.ru"},
		},
		Nodes: []backup.Node{
			backupSourceNode(t, nodeOrders, srcDefaultTeam, "orders", "nexus_old_default.orders"),
			pull,
			backupSourceNode(t, nodeAcme, srcAcmeTeam, "acme", "nexus_old_acme.acme"),
		},
		NodeHosts:   []backup.NodeHost{{NodeID: nodeOrders, HostID: srcHost}},
		AppSettings: &domain.AppSettings{Sentry: domain.SentrySettings{Environment: new("prod")}},
	}
}

func sealBackup(t *testing.T, s *backup.Snapshot, sourceID string, schema uint) []byte {
	t.Helper()
	payload, err := json.Marshal(s)
	require.NoError(t, err)
	file, err := backupfile.Seal(backupfile.Header{
		ExportedAt: time.Date(2026, 9, 29, 17, 30, 0, 0, time.UTC), NexusVersion: "v1.39.0",
		SchemaVersion: schema, SourceInstanceID: sourceID, Counts: s.Counts(),
	}, payload, testPassphrase, cheapBackupKDF())
	require.NoError(t, err)
	return file
}

func previewRequest(file []byte) BackupImportRequest {
	return BackupImportRequest{File: file, Passphrase: testPassphrase, SelfHost: "nexus-new.example.ru"}
}

func nodeByID(set *port.RestoreSet, id string) *domain.Node {
	for _, n := range set.Nodes {
		if n.ID == id {
			return n
		}
	}
	return nil
}

// TestBackupPreview_ReconcilesWithSeeds — сид-строки чистой цели остаются со
// своими id, ссылки файла перенаправляются, имена баз вычисляются по
// идентификатору цели, dry-run получает весь набор.
func TestBackupPreview_ReconcilesWithSeeds(t *testing.T) {
	t.Parallel()
	f := newImportFixture(t, targetSnapshot(), "kz")
	p, err := f.uc.PreviewImport(context.Background(), Actor{UserLogin: "root"}, previewRequest(
		sealBackup(t, sourceSnapshot(t), "old", 43)))
	require.NoError(t, err)
	require.Empty(t, p.Blockers)
	require.Equal(t, []bool{true}, f.restorer.dryRun, "предпросмотр — только dry-run")
	set := f.restorer.last

	require.Len(t, set.TeamUpdates, 1)
	assert.Equal(t, tgtDefaultTeam, set.TeamUpdates[0].ID, "Default остаётся строкой цели")
	assert.Equal(t, "Главная", set.TeamUpdates[0].Name)
	assert.Equal(t, "nexus_kz_default", set.TeamUpdates[0].CHDatabase)
	require.Len(t, set.Teams, 1)
	assert.Equal(t, "nexus_kz_acme", set.Teams[0].CHDatabase, "база — по идентификатору цели")

	require.Len(t, set.Users, 1, "admin цели не перезаписывается")
	assert.Equal(t, srcAcmeTeam, set.Users[0].DefaultTeamID)
	assert.Contains(t, p.Skipped, BackupIssue{Code: "user_exists", Subject: "admin"})
	assert.Len(t, set.Favorites, 1, "избранное без членства не переносится")

	assert.Equal(t, tgtTplStd, set.CHTemplates[0].ID, "шаблон совпал по имени")
	assert.Equal(t, srcTplCustom, set.DefaultTemplateID)
	assert.Equal(t, BackupEntityPlan{Create: 1, Replace: 2}, p.Entities["log_masks"])

	orders := nodeByID(set, nodeOrders)
	require.NotNil(t, orders, "UUID узла сохраняется")
	assert.Equal(t, tgtDefaultTeam, orders.TeamID)
	assert.Equal(t, "nexus_kz_default.orders", orders.ClickHouseTable)
	assert.Equal(t, []string{"API.example.com"}, orders.URLAllowedHosts)
	assert.Equal(t, "api:s3cret", orders.AuthCredentials)
	assert.Equal(t, domain.NodeStatusPaused, nodeByID(set, nodePull).Status, "pull-узел — на паузе по умолчанию")
	assert.Equal(t, BackupNodeStatusPlan{Enabled: 2, Paused: 1}, p.NodeStatuses)

	require.Len(t, set.PeerInstances, 1)
	assert.Equal(t, "p1", set.PeerInstances[0].ID)
	assert.Contains(t, p.Skipped, BackupIssue{Code: "peer_instance_self", Subject: "https://nexus-new.example.ru"})

	assert.Equal(t, BackupCHTablesPlan{Create: 3}, p.Tables)
	assert.Equal(t, []string{"sentry"}, p.SettingsSections)
	require.NotNil(t, set.Audit)
	assert.Equal(t, domain.ActionConfigImport, set.Audit.Action)
	assert.NotEmpty(t, p.Digest)
}

// TestBackupPreview_SameInstanceKeepsDatabaseNames — тот же instance.id:
// имена баз из файла, включая legacy nexus_<slug>, иначе уцелевшие логи
// стали бы невидимы.
func TestBackupPreview_SameInstanceKeepsDatabaseNames(t *testing.T) {
	t.Parallel()
	src := sourceSnapshot(t)
	src.Teams[1].CHDatabase = "nexus_acme"
	src.Nodes[2].ClickHouseTable = "nexus_acme.acme"
	f := newImportFixture(t, targetSnapshot(), "kz")
	f.ch.dbs["nexus_acme"] = port.CHDatabaseState{Ownership: port.CHOwnershipOwned, Tables: 1}
	f.ch.tables["nexus_acme.acme"] = true

	p, err := f.uc.PreviewImport(context.Background(), Actor{}, previewRequest(sealBackup(t, src, "kz", 43)))
	require.NoError(t, err)
	require.Empty(t, p.Blockers)
	assert.Equal(t, "nexus_acme", f.restorer.last.Teams[0].CHDatabase)
	assert.Equal(t, "nexus_acme.acme", nodeByID(f.restorer.last, nodeAcme).ClickHouseTable)
	assert.Equal(t, 1, p.Tables.Existing, "существующая таблица доводится до схемы, а не создаётся")
	assert.Contains(t, p.Warnings, BackupIssue{Code: "ch_database_owned", Subject: "nexus_acme", Detail: "1"})
}

func TestBackupPreview_StatusModes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		mode BackupStatusMode
		want BackupNodeStatusPlan
	}{
		{BackupStatusAsFile, BackupNodeStatusPlan{Enabled: 3}},
		{BackupStatusPauseAll, BackupNodeStatusPlan{Paused: 3}},
		{"", BackupNodeStatusPlan{Enabled: 2, Paused: 1}},
	}
	file := sealBackup(t, sourceSnapshot(t), "old", 43)
	for _, tt := range tests {
		t.Run(string(tt.mode), func(t *testing.T) {
			t.Parallel()
			f := newImportFixture(t, targetSnapshot(), "kz")
			req := previewRequest(file)
			req.Options.StatusMode = tt.mode
			p, err := f.uc.PreviewImport(context.Background(), Actor{}, req)
			require.NoError(t, err)
			assert.Equal(t, tt.want, p.NodeStatuses)
		})
	}
}

// TestBackupPreview_DigestCoversOptions — пустой режим равен режиму по
// умолчанию, другой режим даёт другой дайджест.
func TestBackupPreview_DigestCoversOptions(t *testing.T) {
	t.Parallel()
	file := sealBackup(t, sourceSnapshot(t), "old", 43)
	digest := func(mode BackupStatusMode) string {
		f := newImportFixture(t, targetSnapshot(), "kz")
		req := previewRequest(file)
		req.Options.StatusMode = mode
		p, err := f.uc.PreviewImport(context.Background(), Actor{}, req)
		require.NoError(t, err)
		return p.Digest
	}
	assert.Equal(t, digest(""), digest(BackupStatusPausePull))
	assert.NotEqual(t, digest(BackupStatusPausePull), digest(BackupStatusAsFile))
}

// TestBackupPreview_Refusals — отказы до плана. На нечистой цели файл не
// расшифровывается: неверный пароль не маскирует настоящую причину.
func TestBackupPreview_Refusals(t *testing.T) {
	t.Parallel()
	good := sealBackup(t, sourceSnapshot(t), "old", 43)
	tests := []struct {
		name    string
		mutate  func(f *importFixture, req *BackupImportRequest)
		wantErr error
	}{
		{"нечистая цель", func(f *importFixture, req *BackupImportRequest) {
			f.target.state = port.BackupTargetState{Nodes: 3, Teams: 2}
			req.Passphrase = "wrong passphrase!!"
		}, port.ErrTargetNotClean},
		{"нет ClickHouse", func(f *importFixture, _ *BackupImportRequest) {
			f.uc.chInspector = nil
		}, ErrBackupCHUnavailable},
		{"схема файла новее", func(_ *importFixture, req *BackupImportRequest) {
			req.File = sealBackup(t, sourceSnapshot(t), "old", 44)
		}, ErrBackupSchemaNewer},
		{"неверный пароль", func(_ *importFixture, req *BackupImportRequest) {
			req.Passphrase = "wrong passphrase!!"
		}, backupfile.ErrDecrypt},
		{"неизвестный режим", func(_ *importFixture, req *BackupImportRequest) {
			req.Options.StatusMode = "all_on"
		}, ErrBackupOptionsInvalid},
		{"dry-run: цель перестала быть чистой", func(f *importFixture, _ *BackupImportRequest) {
			f.restorer.err = port.ErrTargetNotClean
		}, port.ErrTargetNotClean},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newImportFixture(t, targetSnapshot(), "kz")
			req := previewRequest(good)
			tt.mutate(f, &req)
			_, err := f.uc.PreviewImport(context.Background(), Actor{}, req)
			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}

// TestBackupPreview_CHDatabaseDecisions — решения по маркеру владения §70.
func TestBackupPreview_CHDatabaseDecisions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		state       port.CHDatabaseState
		adopt       bool
		wantAction  string
		wantBlocker string
	}{
		{"нет базы", port.CHDatabaseState{Ownership: port.CHOwnershipNone}, false, chActionCreate, ""},
		{"наша", port.CHDatabaseState{Ownership: port.CHOwnershipOwned, Tables: 4}, false, chActionReuse, ""},
		{"без маркера, пустая", port.CHDatabaseState{Ownership: port.CHOwnershipUnclaimed}, false, chActionClaim, ""},
		{"без маркера, с таблицами", port.CHDatabaseState{Ownership: port.CHOwnershipUnclaimed, Tables: 2}, false, chActionBlocked, "ch_database_unclaimed"},
		{"без маркера, с таблицами, галочка", port.CHDatabaseState{Ownership: port.CHOwnershipUnclaimed, Tables: 2}, true, chActionAdopt, ""},
		{"чужая", port.CHDatabaseState{Ownership: port.CHOwnershipForeign, Owner: "msk"}, false, chActionBlocked, "ch_database_foreign"},
		{"конфликт", port.CHDatabaseState{Ownership: port.CHOwnershipConflict}, true, chActionBlocked, "ch_database_conflict"},
	}
	file := sealBackup(t, sourceSnapshot(t), "old", 43)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newImportFixture(t, targetSnapshot(), "kz")
			f.ch.dbs["nexus_kz_acme"] = tt.state
			req := previewRequest(file)
			req.Options.AdoptUnclaimed = tt.adopt
			p, err := f.uc.PreviewImport(context.Background(), Actor{}, req)
			require.NoError(t, err)
			var got BackupCHDatabasePlan
			for _, d := range p.Databases {
				if d.Name == "nexus_kz_acme" {
					got = d
				}
			}
			assert.Equal(t, tt.wantAction, got.Action)
			if tt.wantBlocker == "" {
				assert.Empty(t, p.Blockers)
				assert.Equal(t, 1, f.restorer.calls)
				return
			}
			require.NotEmpty(t, p.Blockers)
			assert.Equal(t, tt.wantBlocker, p.Blockers[0].Code)
			assert.Zero(t, f.restorer.calls, "при блокировке dry-run не выполняется")
		})
	}
}

// TestBackupPreview_NodeChecks — узел, не проходящий нынешнюю проверку или
// ведущий на вход самой цели, восстанавливается выключенным; превышение
// лимита узлов блокирует.
func TestBackupPreview_NodeChecks(t *testing.T) {
	t.Parallel()
	src := sourceSnapshot(t)
	src.Nodes[0].TimeoutMs = -1
	src.Nodes[2].TargetURL = "https://nexus-new.example.ru/api/v1/acme/loop?token=secret"
	f := newImportFixture(t, targetSnapshot(), "kz")
	p, err := f.uc.PreviewImport(context.Background(), Actor{}, previewRequest(sealBackup(t, src, "old", 43)))
	require.NoError(t, err)
	assert.Equal(t, domain.NodeStatusDisabled, nodeByID(f.restorer.last, nodeOrders).Status)
	assert.Equal(t, domain.NodeStatusDisabled, nodeByID(f.restorer.last, nodeAcme).Status)
	codes := map[string]BackupIssue{}
	for _, w := range p.Warnings {
		codes[w.Code] = w
	}
	assert.Equal(t, "default/orders", codes["node_invalid"].Subject)
	assert.Equal(t, "https://nexus-new.example.ru/api/v1/acme/loop", codes["node_self_reference"].Detail,
		"query с токеном не попадает в отчёт")

	f = newImportFixture(t, targetSnapshot(), "kz")
	f.uc.nodesHardLimit = 1
	p, err = f.uc.PreviewImport(context.Background(), Actor{}, previewRequest(sealBackup(t, sourceSnapshot(t), "old", 43)))
	require.NoError(t, err)
	require.Len(t, p.Blockers, 1)
	assert.Equal(t, BackupIssue{Code: "team_node_limit", Subject: "default", Detail: "2 > 1"}, p.Blockers[0])
}

// TestBackupPreview_DryRunFailureBlocks — нарушение ограничения в dry-run
// становится блокирующей проблемой, а не ошибкой запроса.
func TestBackupPreview_DryRunFailureBlocks(t *testing.T) {
	t.Parallel()
	f := newImportFixture(t, targetSnapshot(), "kz")
	f.restorer.err = errors.New("insert node: violates check constraint")
	p, err := f.uc.PreviewImport(context.Background(), Actor{}, previewRequest(sealBackup(t, sourceSnapshot(t), "old", 43)))
	require.NoError(t, err)
	require.Len(t, p.Blockers, 1)
	assert.Equal(t, "dry_run_failed", p.Blockers[0].Code)
}

// TestBackupPreview_ExternalAndOutsideTables — внешняя таблица не создаётся
// (отсутствующая — предупреждение), таблица вне баз команд остаётся как есть.
func TestBackupPreview_ExternalAndOutsideTables(t *testing.T) {
	t.Parallel()
	src := sourceSnapshot(t)
	src.Nodes[0].ExternalTable, src.Nodes[0].ClickHouseTemplateID = true, ""
	src.Nodes[0].ClickHouseTable = "ext.orders"
	src.Nodes[2].ClickHouseTable = "legacy.acme"
	f := newImportFixture(t, targetSnapshot(), "kz")
	p, err := f.uc.PreviewImport(context.Background(), Actor{}, previewRequest(sealBackup(t, src, "old", 43)))
	require.NoError(t, err)
	assert.Equal(t, BackupCHTablesPlan{Create: 1, External: 1, ExternalMissing: 1, Unmanaged: 1}, p.Tables)
	assert.Equal(t, "ext.orders", nodeByID(f.restorer.last, nodeOrders).ClickHouseTable)
	assert.Contains(t, p.Warnings, BackupIssue{Code: "external_table_missing", Subject: "ext.orders"})
	assert.Contains(t, p.Warnings, BackupIssue{Code: "table_outside_team_db", Subject: "acme/acme", Detail: "legacy.acme"})
}
