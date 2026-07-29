//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	pgrepo "nexus/internal/web/adapter/out/postgres"
)

// TestUserPrefs_Repo — §71: репозиторий персональных предпочтений.
//
// Сценарии:
//  1. Upsert + List — roundtrip командного префа.
//  2. Повторный upsert обновляет запись, а не плодит вторую (updated_at растёт).
//  3. Глобальный преф (team_id IS NULL) при двойном upsert'е тоже даёт одну
//     строку — проверка UNIQUE NULLS NOT DISTINCT (без него ON CONFLICT для
//     NULL-строк не срабатывает и upsert превращается в append).
//  4. Глобальный и командный префы с одним ключом сосуществуют.
//  5. Изоляция per-user.
//  6. Лимит: новый ключ сверх потолка → ErrPreferencesLimit; обновление
//     существующего при достигнутом потолке проходит.
//  7. team_id вне членств → ErrUserNotTeamMember (FK 23503).
//  8. Каскады: RemoveMember сносит командный преф и не трогает глобальный,
//     удаление пользователя сносит оба.
//  9. CHECK'и БД: неверный формат ключа и значение 'null' отвергаются.
func TestUserPrefs_Repo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	const maxPerUser = 5
	logger := logging.NewNoop()
	userRepo := pgrepo.NewUserRepoPg(pool, logger)
	teamRepo := pgrepo.NewTeamRepoPg(pool, logger)
	defaultTeam := resolveDefaultTeamID(t, ctx, pool)

	mkUser := func(login string) *domain.User {
		u := &domain.User{
			Login: login, Role: domain.UserRoleViewer,
			Active: true, Lang: domain.UserLangEN,
		}
		require.NoError(t, userRepo.Create(ctx, u))
		return u
	}
	alice := mkUser("prefs_alice")
	bob := mkUser("prefs_bob")
	require.NoError(t, teamRepo.AddMember(ctx, alice.ID, defaultTeam, domain.TeamRoleMember))
	require.NoError(t, teamRepo.AddMember(ctx, bob.ID, defaultTeam, domain.TeamRoleMember))

	// Вторая команда — нужна для проверки «чужая команда» (alice в ней не состоит).
	other := &domain.Team{Slug: "prefs_other", Name: "Prefs Other", CHDatabase: "nexus_prefs_other"}
	require.NoError(t, teamRepo.Create(ctx, other))

	set := func(userID, teamID, key, value string) error {
		return userRepo.SetPreference(ctx, &domain.UserPreference{
			UserID: userID, TeamID: teamID, Key: key, Value: json.RawMessage(value),
		}, maxPerUser)
	}
	const periodKey = domain.PreferenceKeyOverviewPeriod

	// 1. Roundtrip командного префа.
	require.NoError(t, set(alice.ID, defaultTeam, periodKey, `{"kind":"preset","range":"7d"}`))
	got, err := userRepo.ListPreferences(ctx, alice.ID)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, defaultTeam, got[0].TeamID)
	assert.Equal(t, periodKey, got[0].Key)
	assert.JSONEq(t, `{"kind":"preset","range":"7d"}`, string(got[0].Value))

	// 2. Повторный upsert обновляет значение, не добавляя строку.
	firstUpdatedAt := got[0].UpdatedAt
	time.Sleep(2 * time.Millisecond)
	require.NoError(t, set(alice.ID, defaultTeam, periodKey, `{"kind":"preset","range":"1h"}`))
	got, err = userRepo.ListPreferences(ctx, alice.ID)
	require.NoError(t, err)
	require.Len(t, got, 1, "upsert обновляет, а не добавляет")
	assert.JSONEq(t, `{"kind":"preset","range":"1h"}`, string(got[0].Value))
	assert.True(t, got[0].UpdatedAt.After(firstUpdatedAt), "updated_at обновлён")

	// 3. Глобальный преф: двойной upsert → одна строка (NULLS NOT DISTINCT).
	require.NoError(t, set(alice.ID, "", periodKey, `{"kind":"preset","range":"24h"}`))
	require.NoError(t, set(alice.ID, "", periodKey, `{"kind":"preset","range":"30d"}`))
	got, err = userRepo.ListPreferences(ctx, alice.ID)
	require.NoError(t, err)
	require.Len(t, got, 2, "глобальный преф не дублируется при повторном upsert'е")

	// 4. Глобальный и командный с одним ключом сосуществуют; глобальный первый.
	assert.True(t, got[0].IsGlobal(), "глобальный преф идёт первым (ORDER BY key, team_id NULLS FIRST)")
	assert.JSONEq(t, `{"kind":"preset","range":"30d"}`, string(got[0].Value))
	assert.Equal(t, defaultTeam, got[1].TeamID)
	assert.JSONEq(t, `{"kind":"preset","range":"1h"}`, string(got[1].Value))

	// 5. Изоляция per-user: у боба своих префов нет.
	bobPrefs, err := userRepo.ListPreferences(ctx, bob.ID)
	require.NoError(t, err)
	assert.Empty(t, bobPrefs)

	// 6. Лимит: у alice уже 2 записи, добиваем до 5 и пробуем шестой ключ.
	for i := range 3 {
		require.NoError(t, set(alice.ID, defaultTeam, fmt.Sprintf("filler.k%d", i), `1`))
	}
	err = set(alice.ID, defaultTeam, "filler.overflow", `1`)
	assert.ErrorIs(t, err, domain.ErrPreferencesLimit, "новый ключ сверх потолка отвергнут")
	assert.NoError(t, set(alice.ID, defaultTeam, periodKey, `{"kind":"preset","range":"3h"}`),
		"обновление существующего ключа проходит и при достигнутом потолке")

	// 7. Чужая команда → ErrUserNotTeamMember (bob не состоит в other).
	// Проверяем на бобе, а не на alice: у alice уже достигнут потолок, и запрос
	// отбился бы по лимиту, не дойдя до FK (WHERE отсекает вставку раньше).
	err = set(bob.ID, other.ID, "team.pref", `1`)
	assert.ErrorIs(t, err, domain.ErrUserNotTeamMember)

	// 8. Каскады. Боб: глобальный + командный преф.
	require.NoError(t, set(bob.ID, "", periodKey, `{"kind":"preset","range":"14h"}`))
	require.NoError(t, set(bob.ID, defaultTeam, periodKey, `{"kind":"preset","range":"7d"}`))
	require.NoError(t, teamRepo.RemoveMember(ctx, bob.ID, defaultTeam))
	bobPrefs, err = userRepo.ListPreferences(ctx, bob.ID)
	require.NoError(t, err)
	require.Len(t, bobPrefs, 1, "исключение из команды сносит только командный преф")
	assert.True(t, bobPrefs[0].IsGlobal(), "глобальный преф пережил исключение из команды")

	require.NoError(t, userRepo.Delete(ctx, bob.ID))
	bobPrefs, err = userRepo.ListPreferences(ctx, bob.ID)
	require.NoError(t, err)
	assert.Empty(t, bobPrefs, "удаление пользователя сносит и глобальные префы")

	// 9. CHECK'и БД — последний рубеж поверх domain.Validate(): репозиторий
	// валидацию не дублирует, поэтому мусор должен отбить именно БД.
	err = set(alice.ID, defaultTeam, "Overview.Period", `1`)
	assert.Error(t, err, "ключ в верхнем регистре отвергнут CHECK'ом key_format")
	err = set(alice.ID, defaultTeam, "null.value", `null`)
	assert.Error(t, err, "значение null отвергнуто CHECK'ом value_not_null")
}
