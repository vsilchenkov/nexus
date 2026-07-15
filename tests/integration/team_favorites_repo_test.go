//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	pgrepo "nexus/internal/web/adapter/out/postgres"
)

// TestTeamFavorites_Repo — §49: репозиторий избранных команд.
//
// Сценарий:
//  1. Replace + List — roundtrip с сохранением порядка.
//  2. Повторный Replace полностью перезаписывает список (reorder + усечение).
//  3. Пустой Replace очищает избранное.
//  4. id команды вне членств → ErrUserNotTeamMember (составной FK).
//  5. RemoveMember каскадно удаляет избранное этой команды.
//  6. Удаление команды каскадно удаляет её из избранного всех пользователей.
func TestTeamFavorites_Repo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	logger := logging.NewNoop()
	teamRepo := pgrepo.NewTeamRepoPg(pool, logger)
	userRepo := pgrepo.NewUserRepoPg(pool, logger)

	// Пользователь + три команды, членство в двух (acme, globex); umbrella — чужая.
	u := &domain.User{
		Login: "fav_user", Role: domain.UserRoleViewer,
		Active: true, Lang: domain.UserLangEN,
	}
	require.NoError(t, userRepo.Create(ctx, u))

	mkTeam := func(slug, name string) *domain.Team {
		tm := &domain.Team{Slug: slug, Name: name, CHDatabase: domain.CHDatabaseForSlug(slug)}
		require.NoError(t, teamRepo.Create(ctx, tm))
		return tm
	}
	acme := mkTeam("acme", "Acme Corp")
	globex := mkTeam("globex", "Globex")
	umbrella := mkTeam("umbrella", "Umbrella")

	require.NoError(t, teamRepo.AddMember(ctx, u.ID, acme.ID, domain.TeamRoleMember))
	require.NoError(t, teamRepo.AddMember(ctx, u.ID, globex.ID, domain.TeamRoleMember))

	// 1. Roundtrip с порядком: globex раньше acme.
	require.NoError(t, teamRepo.ReplaceFavoriteTeams(ctx, u.ID, []string{globex.ID, acme.ID}))
	got, err := teamRepo.ListFavoriteTeamIDs(ctx, u.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{globex.ID, acme.ID}, got, "порядок = порядку в Replace")

	// 2. Повторный Replace перезаписывает: только acme.
	require.NoError(t, teamRepo.ReplaceFavoriteTeams(ctx, u.ID, []string{acme.ID}))
	got, err = teamRepo.ListFavoriteTeamIDs(ctx, u.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{acme.ID}, got)

	// 3. Пустой Replace очищает.
	require.NoError(t, teamRepo.ReplaceFavoriteTeams(ctx, u.ID, nil))
	got, err = teamRepo.ListFavoriteTeamIDs(ctx, u.ID)
	require.NoError(t, err)
	assert.Empty(t, got)

	// 4. Команда вне членств → ErrUserNotTeamMember, список не меняется.
	require.NoError(t, teamRepo.ReplaceFavoriteTeams(ctx, u.ID, []string{acme.ID}))
	err = teamRepo.ReplaceFavoriteTeams(ctx, u.ID, []string{acme.ID, umbrella.ID})
	assert.ErrorIs(t, err, domain.ErrUserNotTeamMember)
	got, err = teamRepo.ListFavoriteTeamIDs(ctx, u.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{acme.ID}, got, "неудачный Replace атомарен — старый список цел")

	// 5. Исключение из членства каскадно чистит избранное этой команды.
	require.NoError(t, teamRepo.ReplaceFavoriteTeams(ctx, u.ID, []string{globex.ID, acme.ID}))
	require.NoError(t, teamRepo.RemoveMember(ctx, u.ID, globex.ID))
	got, err = teamRepo.ListFavoriteTeamIDs(ctx, u.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{acme.ID}, got, "favorite погиб вместе с membership (FK CASCADE)")

	// 6. Удаление команды каскадно чистит её из избранного.
	require.NoError(t, teamRepo.Delete(ctx, acme.ID))
	got, err = teamRepo.ListFavoriteTeamIDs(ctx, u.ID)
	require.NoError(t, err)
	assert.Empty(t, got, "favorite погиб вместе с командой (teams → user_teams → favorites)")
}
