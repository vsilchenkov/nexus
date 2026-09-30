package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
)

// §111.3: право переименовать базу сидированной команды определяется состоянием
// ноды (свежая PostgreSQL и базы ClickHouse ещё не захватывались), а не тем,
// какой процесс вставил строку instance_identity.
func TestDecideRebase(t *testing.T) {
	t.Parallel()

	const seeded = "nexus_default"
	tests := []struct {
		name       string
		identity   Identity
		current    string
		want       string
		wantRename bool
		wantErr    error
	}{
		{
			name:       "нода только что поднята — переименовать",
			identity:   Identity{ID: "kz", FreshPG: true},
			current:    seeded,
			want:       "nexus_kz_default",
			wantRename: true,
		},
		{
			name:     "имя уже по идентификатору — оставить",
			identity: Identity{ID: "kz", FreshPG: true},
			current:  "nexus_kz_default",
			want:     "nexus_kz_default",
		},
		{
			name:     "имя по идентификатору на действующей ноде — оставить",
			identity: Identity{ID: "kz", CHClaimed: true},
			current:  "nexus_kz_default",
			want:     "nexus_kz_default",
		},
		{
			name:     "пустой идентификатор — сидированное имя и есть ожидаемое",
			identity: Identity{FreshPG: true},
			current:  seeded,
			want:     seeded,
		},
		{
			name:     "есть узлы или команды — отказ",
			identity: Identity{ID: "kz"},
			current:  seeded,
			want:     "nexus_kz_default",
			wantErr:  errSeededDBInUse,
		},
		{
			name:     "есть узлы, и базы уже захвачены — отказ по первой причине",
			identity: Identity{ID: "kz", CHClaimed: true},
			current:  seeded,
			want:     "nexus_kz_default",
			wantErr:  errSeededDBInUse,
		},
		{
			name:     "базы уже захвачены под сидированным именем — отказ",
			identity: Identity{ID: "kz", FreshPG: true, CHClaimed: true},
			current:  seeded,
			want:     "nexus_kz_default",
			wantErr:  errSeededDBClaimed,
		},
		{
			name:     "имя чужой ноды — отказ, даже на только что поднятой",
			identity: Identity{ID: "kz", FreshPG: true},
			current:  "nexus_ru_default",
			want:     "nexus_kz_default",
			wantErr:  errTeamDBUnexpected,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			want, rename, err := decideRebase(tt.identity, "default", tt.current)

			assert.Equal(t, tt.want, want)
			assert.Equal(t, tt.wantRename, rename)
			if tt.wantErr == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tt.wantErr)
			assert.ErrorContains(t, err, tt.current, "оператор видит, какое имя записано")
			assert.ErrorContains(t, err, `"default"`, "оператор видит, о какой команде речь")
		})
	}
}

// Отказ на сидированном имени ищут в логе по одной и той же фразе независимо от
// причины — она стоит в DEPLOYMENT и была текстом отказа до §111.
func TestDecideRebase_RefusalKeepsSearchablePhrase(t *testing.T) {
	t.Parallel()

	for _, identity := range []Identity{
		{ID: "kz"},
		{ID: "kz", FreshPG: true, CHClaimed: true},
	} {
		_, _, err := decideRebase(identity, "default", domain.CHDatabaseForSlug("default"))
		require.ErrorContains(t, err, "is already bound to")
	}
}
