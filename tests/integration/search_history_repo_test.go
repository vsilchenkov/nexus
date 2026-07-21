//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	pgrepo "nexus/internal/web/adapter/out/postgres"
)

// TestSearchHistory_Repo — §62: репозиторий истории поиска узлов.
//
// Сценарий:
//  1. Save + List — roundtrip, свежие раньше старых.
//  2. Повторный Save той же строки поднимает её наверх (upsert по searched_at).
//  3. Cap: 12 разных запросов → в истории только 10 самых свежих.
//  4. Изоляция per-user — история одного пользователя невидима другому.
//  5. Clear очищает всю историю пользователя.
//  6. Удаление пользователя каскадно чистит его историю (FK ON DELETE CASCADE).
func TestSearchHistory_Repo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	const keep = 10
	logger := logging.NewNoop()
	userRepo := pgrepo.NewUserRepoPg(pool, logger)

	mkUser := func(login string) *domain.User {
		u := &domain.User{
			Login: login, Role: domain.UserRoleViewer,
			Active: true, Lang: domain.UserLangEN,
		}
		require.NoError(t, userRepo.Create(ctx, u))
		return u
	}
	alice := mkUser("hist_alice")
	bob := mkUser("hist_bob")

	// searched_at имеет разрешение now(); чтобы порядок был детерминирован,
	// между сохранениями делаем крошечную паузу.
	save := func(userID, q string) {
		require.NoError(t, userRepo.SaveSearchQuery(ctx, userID, q, keep))
		time.Sleep(2 * time.Millisecond)
	}

	// 1. Roundtrip: сохранили parcel, потом push → push свежее.
	save(alice.ID, "parcel")
	save(alice.ID, "push")
	got, err := userRepo.ListSearchHistory(ctx, alice.ID, keep)
	require.NoError(t, err)
	assert.Equal(t, []string{"push", "parcel"}, got, "свежие раньше старых")

	// 2. Повторный поиск parcel поднимает его наверх (без дублей).
	save(alice.ID, "parcel")
	got, err = userRepo.ListSearchHistory(ctx, alice.ID, keep)
	require.NoError(t, err)
	assert.Equal(t, []string{"parcel", "push"}, got, "upsert поднимает повтор, дублей нет")

	// 3. Cap: 12 разных запросов → остаются 10 свежих (q00..q11 → q02..q11).
	clearErr := userRepo.ClearSearchHistory(ctx, alice.ID)
	require.NoError(t, clearErr)
	for i := range 12 {
		save(alice.ID, fmt.Sprintf("q%02d", i))
	}
	got, err = userRepo.ListSearchHistory(ctx, alice.ID, keep)
	require.NoError(t, err)
	require.Len(t, got, keep, "история подрезана до keep")
	assert.Equal(t, "q11", got[0], "самый свежий — наверху")
	assert.Equal(t, "q02", got[keep-1], "q00 и q01 вытеснены")

	// 4. Изоляция: у bob своя история.
	save(bob.ID, "bob-only")
	gotBob, err := userRepo.ListSearchHistory(ctx, bob.ID, keep)
	require.NoError(t, err)
	assert.Equal(t, []string{"bob-only"}, gotBob)
	got, err = userRepo.ListSearchHistory(ctx, alice.ID, keep)
	require.NoError(t, err)
	assert.NotContains(t, got, "bob-only", "история bob невидима alice")

	// 5. Clear очищает.
	require.NoError(t, userRepo.ClearSearchHistory(ctx, alice.ID))
	got, err = userRepo.ListSearchHistory(ctx, alice.ID, keep)
	require.NoError(t, err)
	assert.Empty(t, got)

	// 6. Удаление пользователя каскадно чистит историю.
	require.NoError(t, userRepo.Delete(ctx, bob.ID))
	gotBob, err = userRepo.ListSearchHistory(ctx, bob.ID, keep)
	require.NoError(t, err)
	assert.Empty(t, gotBob, "история погибла вместе с пользователем (FK CASCADE)")
}
