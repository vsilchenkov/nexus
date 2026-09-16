package nodestatus_test

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/nodestatus"
)

const (
	idMessengers = "f6693f87-0000-4000-8000-000000000001"
	idWebhook    = "d3aae718-0000-4000-8000-000000000002"
	idBilling    = "0b1e2c3d-0000-4000-8000-000000000003"
)

func newMigrator(t *testing.T) (*nodestatus.LegacyMigrator, *miniredis.Miniredis) {
	t.Helper()
	srv := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: srv.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return nodestatus.NewLegacyMigrator(client), srv
}

// TestKeys_ByNodeID (§101.6) — ключи исхода и серии отказов строятся по id узла.
// Путь уникален лишь внутри команды: ключ по пути делил один бейдж между
// одноимёнными узлами разных команд.
func TestKeys_ByNodeID(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "nexus:node:last_error:id:"+idMessengers, nodestatus.Key(idMessengers))
	assert.Equal(t, "nexus:node:fail_streak:id:"+idMessengers, nodestatus.StreakKey(idMessengers))
	assert.Equal(t, "nexus:node:last_error:telegram", nodestatus.LegacyKey("telegram"))
}

// TestSetLastOutcome_SameNamePathsKeepOwnOutcome (§101.6) — исходы двух узлов
// telegram (команды messengers и webhook) лежат порознь.
func TestSetLastOutcome_SameNamePathsKeepOwnOutcome(t *testing.T) {
	t.Parallel()
	w, srv := newWriter(t)
	ctx := context.Background()

	w.SetLastOutcome(ctx, idMessengers, domain.NodeOutcomeDown)
	w.SetLastOutcome(ctx, idWebhook, domain.NodeOutcomeOK)

	down, err := srv.Get(nodestatus.Key(idMessengers))
	require.NoError(t, err)
	assert.Equal(t, "1", down)
	ok, err := srv.Get(nodestatus.Key(idWebhook))
	require.NoError(t, err)
	assert.Equal(t, "0", ok)
}

// TestSetLastOutcome_EmptyNodeIDWritesNothing (§101.6) — без id узла исход не
// пишется: ключ по пути снова склеил бы одноимённые узлы, а ключ «id:» с пустым
// хвостом — вообще все такие вызовы.
func TestSetLastOutcome_EmptyNodeIDWritesNothing(t *testing.T) {
	t.Parallel()
	w, srv := newWriter(t)

	got := w.SetLastOutcome(context.Background(), "", domain.NodeOutcomeDown)

	assert.Equal(t, domain.NodeOutcomeDown, got, "исход возвращается как есть — по нему ставится гаудж")
	assert.Empty(t, srv.Keys())
}

// TestLegacyMigrator_MovesKeepingTTL — значение переезжает на ключ по id с
// оставшимся сроком; старый ключ остаётся для отката версии (§101.12); путь без
// id не трогается.
func TestLegacyMigrator_MovesKeepingTTL(t *testing.T) {
	t.Parallel()
	m, srv := newMigrator(t)
	ctx := context.Background()
	require.NoError(t, srv.Set(nodestatus.LegacyKey("billing"), "1"))
	srv.SetTTL(nodestatus.LegacyKey("billing"), 5*time.Hour)
	require.NoError(t, srv.Set(nodestatus.LegacyKey("telegram"), "2"))
	srv.SetTTL(nodestatus.LegacyKey("telegram"), 5*time.Hour)

	moved, err := m.Migrate(ctx, map[string]string{"billing": idBilling})
	require.NoError(t, err)

	assert.Equal(t, 1, moved)
	v, err := srv.Get(nodestatus.Key(idBilling))
	require.NoError(t, err)
	assert.Equal(t, "1", v)
	assert.InDelta(t, (5 * time.Hour).Seconds(), srv.TTL(nodestatus.Key(idBilling)).Seconds(), 1)
	assert.True(t, srv.Exists(nodestatus.LegacyKey("billing")), "старый ключ нужен откату")
	assert.False(t, srv.Exists(nodestatus.Key(idMessengers)), "telegram неоднозначен и в карту не попал")
}

// TestLegacyMigrator_KeepsNewerIDKey — ключ по id уже записан новым Sender'ом, он
// свежее старого и не перезаписывается.
func TestLegacyMigrator_KeepsNewerIDKey(t *testing.T) {
	t.Parallel()
	m, srv := newMigrator(t)
	require.NoError(t, srv.Set(nodestatus.LegacyKey("billing"), "1"))
	require.NoError(t, srv.Set(nodestatus.Key(idBilling), "0"))

	moved, err := m.Migrate(context.Background(), map[string]string{"billing": idBilling})
	require.NoError(t, err)

	assert.Zero(t, moved)
	v, err := srv.Get(nodestatus.Key(idBilling))
	require.NoError(t, err)
	assert.Equal(t, "0", v)
}

// TestLegacyMigrator_NoTTLGetsDefault — ключ без срока не переносится вечным.
func TestLegacyMigrator_NoTTLGetsDefault(t *testing.T) {
	t.Parallel()
	m, srv := newMigrator(t)
	require.NoError(t, srv.Set(nodestatus.LegacyKey("billing"), "2"))

	_, err := m.Migrate(context.Background(), map[string]string{"billing": idBilling})
	require.NoError(t, err)

	assert.Greater(t, srv.TTL(nodestatus.Key(idBilling)), 24*time.Hour)
}

// TestLegacyMigrator_PathsAndMarker — в перечень попадают только старые ключи;
// после переноса отметка стоит, и повторный старт Web ничего не переносит.
func TestLegacyMigrator_PathsAndMarker(t *testing.T) {
	t.Parallel()
	m, srv := newMigrator(t)
	ctx := context.Background()
	require.NoError(t, srv.Set(nodestatus.LegacyKey("billing"), "1"))
	require.NoError(t, srv.Set(nodestatus.LegacyKey("partner/echo"), "0"))
	require.NoError(t, srv.Set(nodestatus.Key(idMessengers), "1"))
	require.NoError(t, srv.Set(nodestatus.StreakKey(idMessengers), "3"))
	require.NoError(t, srv.Set("session:abc", "x"))

	paths, err := m.LegacyPaths(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"billing", "partner/echo"}, paths)

	done, err := m.Migrated(ctx)
	require.NoError(t, err)
	require.False(t, done)

	_, err = m.Migrate(ctx, nil)
	require.NoError(t, err)
	done, err = m.Migrated(ctx)
	require.NoError(t, err)
	assert.True(t, done, "отметка ставится и при пустом переносе")
}
