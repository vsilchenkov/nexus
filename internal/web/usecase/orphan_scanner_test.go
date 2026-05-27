package usecase

import (
	"context"
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

func newOrphanScanner(t *testing.T, dbName string, nodes []*domain.Node) *OrphanScanner {
	t.Helper()
	repo := &orphanNodeRepo{listResult: nodes}
	cfg := &config.ClickHouseSection{Database: dbName}
	audit := NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop())
	return NewOrphanScanner(nilConnProvider{}, repo, cfg, audit, "00000000-0000-0000-0000-000000000000", logging.NewNoop())
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
	assert.Contains(t, err.Error(), "drop allowed only in configured database")
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
