package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/logging"
)

// stubMigrationStore — port.NodeStatusMigrator с записью вызовов.
type stubMigrationStore struct {
	migrated   bool
	paths      []string
	moveErr    error
	pathsCalls int
	moved      []map[string]string
}

func (s *stubMigrationStore) LegacyMigrated(context.Context) (bool, error) { return s.migrated, nil }

func (s *stubMigrationStore) LegacyOutcomePaths(context.Context) ([]string, error) {
	s.pathsCalls++
	return s.paths, nil
}

func (s *stubMigrationStore) MigrateLegacyOutcomes(_ context.Context, ids map[string]string) (int, error) {
	s.moved = append(s.moved, ids)
	return len(ids), s.moveErr
}

// TestNodeStatusMigration_MovesOnlyUniquePaths (§101.6) — telegram занят узлами
// двух команд, его исход — смесь, и в перенос он не попадает.
func TestNodeStatusMigration_MovesOnlyUniquePaths(t *testing.T) {
	t.Parallel()
	store := &stubMigrationStore{paths: []string{"billing", "telegram", "deleted"}}
	nodes := &fakeNodePaths{ids: map[string]string{"billing": "id-billing"}}

	err := NewNodeStatusMigration(store, nodes, logging.NewNoop()).Run(context.Background())

	require.NoError(t, err)
	assert.Equal(t, []string{"billing", "telegram", "deleted"}, nodes.asked)
	assert.Equal(t, []map[string]string{{"billing": "id-billing"}}, store.moved)
}

// TestNodeStatusMigration_AlreadyDone — повторный старт ничего не переносит:
// иначе воскресал бы исход, снятый ручным сбросом защиты (§81.4.2).
func TestNodeStatusMigration_AlreadyDone(t *testing.T) {
	t.Parallel()
	store := &stubMigrationStore{migrated: true, paths: []string{"billing"}}
	nodes := &fakeNodePaths{ids: map[string]string{"billing": "id-billing"}}

	require.NoError(t, NewNodeStatusMigration(store, nodes, logging.NewNoop()).Run(context.Background()))

	assert.Zero(t, store.pathsCalls)
	assert.Empty(t, nodes.asked)
	assert.Empty(t, store.moved)
}

// TestNodeStatusMigration_NoLegacyKeysStillMarks — старых ключей нет (чистая
// установка): PostgreSQL не спрашивается, но отметка ставится.
func TestNodeStatusMigration_NoLegacyKeysStillMarks(t *testing.T) {
	t.Parallel()
	store := &stubMigrationStore{}
	nodes := &fakeNodePaths{}

	require.NoError(t, NewNodeStatusMigration(store, nodes, logging.NewNoop()).Run(context.Background()))

	assert.Empty(t, nodes.asked)
	require.Len(t, store.moved, 1)
	assert.Empty(t, store.moved[0])
}

// TestNodeStatusMigration_ResolveErrorLeavesUnmarked — без ответа PostgreSQL
// перенос не отмечается выполненным, следующий старт повторит его.
func TestNodeStatusMigration_ResolveErrorLeavesUnmarked(t *testing.T) {
	t.Parallel()
	store := &stubMigrationStore{paths: []string{"billing"}}
	nodes := &fakeNodePaths{err: errors.New("pg down")}

	err := NewNodeStatusMigration(store, nodes, logging.NewNoop()).Run(context.Background())

	require.Error(t, err)
	assert.Empty(t, store.moved)
}

func TestNodeStatusMigration_StoreErrorReturned(t *testing.T) {
	t.Parallel()
	store := &stubMigrationStore{paths: []string{"billing"}, moveErr: errors.New("redis down")}
	nodes := &fakeNodePaths{ids: map[string]string{"billing": "id-billing"}}

	err := NewNodeStatusMigration(store, nodes, logging.NewNoop()).Run(context.Background())

	require.ErrorContains(t, err, "redis down")
}
