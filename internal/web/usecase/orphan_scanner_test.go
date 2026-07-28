package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"nexus/internal/domain"
	"nexus/internal/platform/config"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// nilConnProvider — отдаёт nil-conn; используется чтобы проверять
// предусловия Scan/Drop, которые срабатывают ДО первого обращения к CH.
type nilConnProvider struct{}

func (nilConnProvider) Conn() chdriver.Conn { return nil }

// orphanNodeRepo — узкий stub под port.NodeRepo, возвращает заданный
// набор узлов из List (этого достаточно для knownTables в Scan/Drop).
type orphanNodeRepo struct {
	listResult []*domain.Node
	listErr    error
}

func (r *orphanNodeRepo) List(_ context.Context, _ port.ListNodesFilter) ([]*domain.Node, error) {
	return r.listResult, r.listErr
}
func (r *orphanNodeRepo) Get(_ context.Context, _ string) (*domain.Node, error) {
	return nil, domain.ErrNodeNotFound
}
func (r *orphanNodeRepo) GetByPath(_ context.Context, _ string) (*domain.Node, error) {
	return nil, domain.ErrNodeNotFound
}
func (r *orphanNodeRepo) Count(_ context.Context, _ string) (int, error) { return 0, nil }
func (r *orphanNodeRepo) Create(_ context.Context, _ *domain.Node) error { return nil }
func (r *orphanNodeRepo) Update(_ context.Context, _ *domain.Node) error { return nil }
func (r *orphanNodeRepo) Delete(_ context.Context, _ string) error       { return nil }
func (r *orphanNodeRepo) UpdateAllowedHostsSnapshot(_ context.Context, _ string, _ []string, _ string) error {
	return nil
}

// fakeTeamRepo — minimal port.TeamRepo для orphan-тестов: возвращает
// одну team с заданным ch_database, остальные методы — nopTeamRepo.
type fakeTeamRepo struct {
	nopTeamRepo
	chDatabase string
}

func (r *fakeTeamRepo) List(context.Context) ([]*domain.Team, error) {
	return []*domain.Team{{ID: "team-1", Slug: "default", CHDatabase: r.chDatabase}}, nil
}

func newOrphanScanner(t *testing.T, dbName string, nodes []*domain.Node) *OrphanScanner {
	t.Helper()
	repo := &orphanNodeRepo{listResult: nodes}
	teams := &fakeTeamRepo{chDatabase: dbName}
	cfg := &config.ClickHouseSection{Database: dbName}
	audit := NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop())
	return NewOrphanScanner(nilConnProvider{}, repo, teams, cfg, audit, logging.NewNoop())
}

func TestIsSafeTableNameLocal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"valid simple", "db.table", true},
		{"valid with digits", "db1.tbl_2024", true},
		{"valid with underscores", "_db_._tbl_", true},
		{"valid mixed case", "MyDB.LogTable", true},
		{"empty", "", false},
		{"no dot", "tablename", false},
		{"two dots", "a.b.c", false},
		{"leading dot", ".table", false},
		{"trailing dot", "db.", false},
		{"only dot", ".", false},
		{"sql injection semicolon", "db.table; DROP", false},
		{"sql injection quote", "db.table'--", false},
		{"sql injection space", "db.t name", false},
		{"backtick", "db.`tbl`", false},
		{"cyrillic", "db.таблица", false},
		{"hyphen", "db.my-table", false},
		{"too long", strings.Repeat("a", 65) + "." + strings.Repeat("b", 64), false},
		{"exactly 128 boundary ok", strings.Repeat("a", 63) + "." + strings.Repeat("b", 64), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, isSafeTableNameLocal(tc.in), "input %q", tc.in)
		})
	}
}

func TestOrphanScanner_Scan_NilConn(t *testing.T) {
	t.Parallel()

	sc := newOrphanScanner(t, "nexus", nil)
	out, err := sc.Scan(context.Background())
	require.Error(t, err)
	assert.Nil(t, out)
	assert.Contains(t, err.Error(), "clickhouse conn is nil")
}

func TestOrphanScanner_Drop_InvalidName(t *testing.T) {
	t.Parallel()

	sc := newOrphanScanner(t, "nexus", nil)
	err := sc.Drop(context.Background(), SystemActor(), "bad name; DROP")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid table name")
}

func TestOrphanScanner_Drop_WrongDatabase(t *testing.T) {
	t.Parallel()

	sc := newOrphanScanner(t, "nexus", nil)
	err := sc.Drop(context.Background(), SystemActor(), "otherdb.some_table")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "drop allowed only in tenant databases")
}

func TestOrphanScanner_Drop_RefusesIfStillUsedByNode(t *testing.T) {
	t.Parallel()

	// Узел с ClickHouseTable = nexus.log_node1 → значит таблица «своя»,
	// её удалять нельзя даже если кто-то вручную дёрнул Drop.
	nodes := []*domain.Node{
		{ID: "n1", Path: "n1", ClickHouseTable: "nexus.log_node1"},
	}
	sc := newOrphanScanner(t, "nexus", nodes)
	err := sc.Drop(context.Background(), SystemActor(), "nexus.log_node1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "in use by a node")
}

// stubOrphanOwnership — гейт владения БД (§70.4).
type stubOrphanOwnership struct {
	owned map[string]bool
	err   error
}

func (s *stubOrphanOwnership) OwnsDatabase(_ context.Context, db string) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	return s.owned[db], nil
}

func (s *stubOrphanOwnership) AssertOwnsDatabase(ctx context.Context, db string) error {
	owns, err := s.OwnsDatabase(ctx, db)
	if err != nil {
		return err
	}
	if !owns {
		return domain.ErrCHForeignDatabase
	}
	return nil
}

// §70.4: таблицы соседней ноды не числятся в НАШЕЙ PostgreSQL, поэтому выглядят
// «бесхозными». Удаление такой таблицы по кнопке — потеря чужих данных.
func TestOrphanScanner_Drop_ForeignDatabaseRejected(t *testing.T) {
	t.Parallel()

	sc := newOrphanScanner(t, "nexus_default", nil)
	sc.SetOwnership(&stubOrphanOwnership{owned: map[string]bool{"nexus_default": false}})

	err := sc.Drop(context.Background(), SystemActor(), "nexus_default.orphan_42")
	require.ErrorIs(t, err, domain.ErrCHForeignDatabase)
}

// Своя БД удаляется как раньше — гейт доходит до проверки соединения.
func TestOrphanScanner_Drop_OwnDatabasePassesGate(t *testing.T) {
	t.Parallel()

	sc := newOrphanScanner(t, "nexus_kz_default", nil)
	sc.SetOwnership(&stubOrphanOwnership{owned: map[string]bool{"nexus_kz_default": true}})

	err := sc.Drop(context.Background(), SystemActor(), "nexus_kz_default.orphan_42")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "clickhouse conn is nil")
}

// Скан не должен даже показывать чужие БД: их таблицы не наши, и предлагать их
// к удалению нельзя. Проверяем allow-list напрямую — Scan упирается в отсутствие
// соединения раньше (conn проверяется первым).
func TestOrphanScanner_AllowedDatabases_SkipsForeign(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	foreign := newOrphanScanner(t, "nexus_default", nil)
	foreign.SetOwnership(&stubOrphanOwnership{owned: map[string]bool{"nexus_default": false}})
	dbs, err := foreign.allowedDatabases(ctx)
	require.NoError(t, err, "чужая БД — не ошибка сканирования, просто нечего показывать")
	assert.Empty(t, dbs)

	own := newOrphanScanner(t, "nexus_kz_default", nil)
	own.SetOwnership(&stubOrphanOwnership{owned: map[string]bool{"nexus_kz_default": true}})
	dbs, err = own.allowedDatabases(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"nexus_kz_default"}, dbs)

	// Сбой проверки владения тоже исключает БД из скана: показать чужую таблицу
	// как «бесхозную» опаснее, чем не показать свою.
	broken := newOrphanScanner(t, "nexus_kz_default", nil)
	broken.SetOwnership(&stubOrphanOwnership{err: errors.New("clickhouse down")})
	dbs, err = broken.allowedDatabases(ctx)
	require.NoError(t, err)
	assert.Empty(t, dbs)
}

func TestOrphanScanner_Drop_NilConn_AfterChecks(t *testing.T) {
	t.Parallel()

	// Все защиты пройдены (имя валидно, БД совпадает, не в known-set),
	// но conn = nil — дойдём до проверки conn и упадём.
	sc := newOrphanScanner(t, "nexus", nil)
	err := sc.Drop(context.Background(), SystemActor(), "nexus.orphan_42")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "clickhouse conn is nil")
}

// knownTables регистрирует имена case-insensitive — Drop с другой case'ой
// должен распознать пересечение.
func TestOrphanScanner_Drop_CaseInsensitiveMatch(t *testing.T) {
	t.Parallel()

	nodes := []*domain.Node{
		{ID: "n1", Path: "n1", ClickHouseTable: "Nexus.LOG_NODE1"},
	}
	sc := newOrphanScanner(t, "nexus", nodes)
	// Drop проверяет EqualFold(db) и lowercased full-name match.
	err := sc.Drop(context.Background(), SystemActor(), "nexus.log_node1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "in use by a node")
}
