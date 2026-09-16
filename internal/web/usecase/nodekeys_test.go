package usecase

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/metrics"
	"nexus/internal/web/usecase/port"
)

// slugDirectory — справочник команд, отдающий слоги по id (§101.9): метка team
// несёт слог, а узел — team_id.
func slugDirectory(slugByID map[string]string) port.TeamRepo {
	teams := make([]*domain.Team, 0, len(slugByID))
	for id, slug := range slugByID {
		teams = append(teams, &domain.Team{ID: id, Slug: slug})
	}
	return notifTeamRepo{teams: teams}
}

// failingTeams — справочник команд, у которого не читается список.
type failingTeams struct{ nopTeamRepo }

func (failingTeams) List(context.Context) ([]*domain.Team, error) {
	return nil, errors.New("pg down")
}

// fakeNodeKeys — port.NodeKeyResolver: отдаёт узлы с запрошенными путями и
// запоминает, о каких путях спросили. Тест проверяет не только результат, но и
// то, ЧТО именно спросили у базы.
type fakeNodeKeys struct {
	refs  []port.NodeKeyRef
	err   error
	mu    sync.Mutex
	asked []string
}

func (f *fakeNodeKeys) RefsByPaths(_ context.Context, paths []string) ([]port.NodeKeyRef, error) {
	f.mu.Lock()
	f.asked = append(f.asked, paths...)
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	want := make(map[string]bool, len(paths))
	for _, p := range paths {
		want[p] = true
	}
	var out []port.NodeKeyRef
	for _, r := range f.refs {
		if want[r.Key.Path] {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeNodeKeys) askedPaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...)
}

func nk(team, path string) port.NodeKey { return port.NodeKey{Team: team, Path: path} }

// Однозначность пути — по всем командам сразу: путь в двух командах владельца
// не имеет.
func TestUniquePathOwners(t *testing.T) {
	t.Parallel()
	got := uniquePathOwners([]port.NodeKeyRef{
		{ID: "n-solo", Key: nk("a", "solo")},
		{ID: "n-twin-a", Key: nk("a", "twin")},
		{ID: "n-twin-b", Key: nk("b", "twin")},
	})
	assert.Equal(t, map[string]port.NodeKeyRef{"solo": {ID: "n-solo", Key: nk("a", "solo")}}, got)
}

func TestWithLegacy(t *testing.T) {
	t.Parallel()
	m := map[port.NodeKey]float64{
		nk("a", "solo"): 10,
		nk("", "solo"):  5,
		nk("b", "twin"): 3,
		nk("", "twin"):  100,
	}
	owners := map[string]port.NodeKeyRef{"solo": {ID: "n-solo", Key: nk("a", "solo")}}

	tests := []struct {
		name string
		key  port.NodeKey
		want float64
	}{
		{name: "однозначный путь своего узла — ряд до §101 прибавляется", key: nk("a", "solo"), want: 15},
		{name: "однозначный путь, но узел другой команды — не прибавляется", key: nk("b", "solo"), want: 0},
		{name: "неоднозначный путь — только ряд пары", key: nk("b", "twin"), want: 3},
		{name: "ключ без слога не читается вовсе", key: nk("", "solo"), want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.InDelta(t, tc.want, withLegacy(m, tc.key, owners, addFloat), 1e-9)
		})
	}
}

// p95 ряда до §101 к узлу не прибавляется: перцентили не складываются.
func TestAddCounters_KeepsP95(t *testing.T) {
	t.Parallel()
	got := addCounters(
		port.NodeThroughput{In: 10, Out: 9, Errors: 1, P95ms: 50},
		port.NodeThroughput{In: 5, Out: 4, Errors: 2, P95ms: 999})
	assert.Equal(t, port.NodeThroughput{In: 15, Out: 13, Errors: 3, P95ms: 50}, got)
}

func TestLegacyRowPaths(t *testing.T) {
	t.Parallel()
	m := map[port.NodeKey]int{
		nk("a", "solo"):                1,
		nk("", "zeta"):                 1,
		nk("", "solo"):                 1,
		nk("", metrics.NodeUnresolved): 1,
		nk("", ""):                     1,
	}
	assert.Equal(t, []string{"solo", "zeta"}, legacyRowPaths(m, nil),
		"пути без команды, по порядку; заглушка <unresolved> узлом не является")
	assert.Equal(t, []string{"solo"}, legacyRowPaths(m, func(p string) bool { return p == "solo" }))
}

// Fail-closed: без резолвера или при его сбое ряды до §101 не приписываются
// никому.
func TestResolveLegacyOwners_Degrades(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	log := logging.NewNoop()

	require.Nil(t, resolveLegacyOwners(ctx, nil, []string{"solo"}, log))

	failing := &fakeNodeKeys{err: errors.New("pg down")}
	require.Nil(t, resolveLegacyOwners(ctx, failing, []string{"solo"}, log))

	idle := &fakeNodeKeys{}
	require.Nil(t, resolveLegacyOwners(ctx, idle, nil, log))
	require.Empty(t, idle.askedPaths(), "без рядов до §101 базу не спрашивают")
}

// Без справочника команд узлы остаются без слога — и получают нули, а не ряды
// одноимённого узла другой команды.
func TestTeamSlugsByID_Degrades(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	log := logging.NewNoop()
	require.Empty(t, teamSlugsByID(ctx, nil, log))
	require.Empty(t, teamSlugsByID(ctx, failingTeams{}, log))
	require.Equal(t, map[string]string{"t1": "alpha"},
		teamSlugsByID(ctx, slugDirectory(map[string]string{"t1": "alpha"}), log))
}
