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

// TestTeamExternalURL_Repo — §89.4: внешняя ссылка команды переживает запись и
// чтение ВСЕМИ путями.
//
// Главный смысл теста — ловушка синхронности: у команды ТРИ независимых места
// сканирования (общий teamCols для Get/List, свой SELECT в ListUserTeams и ещё
// один в ListTeamsByUsers). Пропуск колонки в любом из двух последних не роняет
// ничего — просто отдаёт пустую строку, причём именно в /api/me/teams, то есть
// ровно там, где значение ждёт интерфейс. Unit-тестами это не ловится.
func TestTeamExternalURL_Repo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	logger := logging.NewNoop()
	teamRepo := pgrepo.NewTeamRepoPg(pool, logger)
	userRepo := pgrepo.NewUserRepoPg(pool, logger)

	const external = "https://gw.example.com/nexus"

	acme := &domain.Team{
		Slug: "acme", Name: "Acme Corp",
		CHDatabase:  domain.CHDatabaseForSlug("acme"),
		ExternalURL: external,
	}
	require.NoError(t, teamRepo.Create(ctx, acme))
	require.NotEmpty(t, acme.ID)

	// Вторая команда без ссылки — проверяем, что пусто остаётся пустым, а не
	// подхватывает соседнее значение из-за сдвига позиций в Scan.
	globex := &domain.Team{
		Slug: "globex", Name: "Globex",
		CHDatabase: domain.CHDatabaseForSlug("globex"),
	}
	require.NoError(t, teamRepo.Create(ctx, globex))

	u := &domain.User{
		Login: "ext_user", Role: domain.UserRoleViewer,
		Active: true, Lang: domain.UserLangEN,
	}
	require.NoError(t, userRepo.Create(ctx, u))
	require.NoError(t, teamRepo.AddMember(ctx, u.ID, acme.ID, domain.TeamRoleMember))
	require.NoError(t, teamRepo.AddMember(ctx, u.ID, globex.ID, domain.TeamRoleMember))

	// 1. GetByID / GetBySlug — общий teamCols.
	got, err := teamRepo.GetByID(ctx, acme.ID)
	require.NoError(t, err)
	assert.Equal(t, external, got.ExternalURL)

	bySlug, err := teamRepo.GetBySlug(ctx, "acme")
	require.NoError(t, err)
	assert.Equal(t, external, bySlug.ExternalURL)

	// 2. List — тот же teamCols, но другой путь вызова.
	all, err := teamRepo.List(ctx)
	require.NoError(t, err)
	assert.Equal(t, external, findTeam(t, all, acme.ID).ExternalURL)
	assert.Empty(t, findTeam(t, all, globex.ID).ExternalURL)

	// 3. ListUserTeams — СВОЙ список колонок. Именно он питает /api/me/teams.
	mine, err := teamRepo.ListUserTeams(ctx, u.ID)
	require.NoError(t, err)
	require.Len(t, mine, 2)
	byID := map[string]string{}
	for _, ut := range mine {
		byID[ut.Team.ID] = ut.Team.ExternalURL
	}
	assert.Equal(t, external, byID[acme.ID], "ListUserTeams потерял external_url")
	assert.Empty(t, byID[globex.ID])

	// 4. ListTeamsByUsers — ЕЩЁ ОДИН свой список колонок (§44.G).
	byUser, err := teamRepo.ListTeamsByUsers(ctx, []string{u.ID})
	require.NoError(t, err)
	require.Len(t, byUser[u.ID], 2)
	found := ""
	for _, ut := range byUser[u.ID] {
		if ut.Team.ID == acme.ID {
			found = ut.Team.ExternalURL
		}
	}
	assert.Equal(t, external, found, "ListTeamsByUsers потерял external_url")

	// 5. Update пишет новое значение и умеет СТИРАТЬ ссылку: пустая строка —
	// законное «не задана», а не «оставь прежнюю».
	got.ExternalURL = "https://other.example.com"
	require.NoError(t, teamRepo.Update(ctx, got))
	after, err := teamRepo.GetByID(ctx, acme.ID)
	require.NoError(t, err)
	assert.Equal(t, "https://other.example.com", after.ExternalURL)

	after.ExternalURL = ""
	require.NoError(t, teamRepo.Update(ctx, after))
	cleared, err := teamRepo.GetByID(ctx, acme.ID)
	require.NoError(t, err)
	assert.Empty(t, cleared.ExternalURL)
}

func findTeam(t *testing.T, teams []*domain.Team, id string) *domain.Team {
	t.Helper()
	for _, tm := range teams {
		if tm.ID == id {
			return tm
		}
	}
	t.Fatalf("команда %s не найдена в списке", id)
	return nil
}
