package usecase

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// capturingAuditRepo — stubAuditRepo, запоминающий фильтр последнего List:
// по возвращённым записям нельзя отличить «скоуп применён» от «скоуп потерян».
type capturingAuditRepo struct {
	stubAuditRepo
	lastFilter port.AuditFilter
	listCalls  int
}

func (r *capturingAuditRepo) List(_ context.Context, f port.AuditFilter) ([]*domain.AuditEntry, error) {
	r.lastFilter = f
	r.listCalls++
	return r.entries, nil
}

func newAuditScopeUC(repo port.AuditRepo, teams port.TeamRepo) *AuditUsecase {
	return NewAuditUsecase(repo, logging.NewNoop()).WithTeams(teams)
}

// TestAuditUC_ListAcrossTeams_Scope (§86.7): скоуп — членства пользователя,
// однокомандный TeamID затирается.
func TestAuditUC_ListAcrossTeams_Scope(t *testing.T) {
	t.Parallel()
	repo := &capturingAuditRepo{}
	uc := newAuditScopeUC(repo, newScopeTeams())

	_, err := uc.ListAcrossTeams(context.Background(), "u1", port.AuditFilter{
		TeamID: "session-team",
		Limit:  100,
	})
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{"team1", "team2"}, repo.lastFilter.TeamIDs)
	assert.Empty(t, repo.lastFilter.TeamID, "однокомандный скоуп не должен ехать вместе со сквозным")
	assert.Equal(t, 100, repo.lastFilter.Limit, "прочие фильтры сохраняются")
}

// TestAuditUC_ListAcrossTeams_Guards (§86.7): деградационные ветки.
func TestAuditUC_ListAcrossTeams_Guards(t *testing.T) {
	t.Parallel()

	t.Run("нет членств → пусто, запрос не уходит", func(t *testing.T) {
		t.Parallel()
		repo := &capturingAuditRepo{}
		uc := newAuditScopeUC(repo, &scopeTeams{})

		entries, err := uc.ListAcrossTeams(context.Background(), "u1", port.AuditFilter{})
		require.NoError(t, err)
		assert.Empty(t, entries)
		// Пустой TeamIDs в адаптере означает «фильтра нет» — то есть журнал
		// всего инстанса. Ровно то, чего в этом режиме быть не должно.
		assert.Zero(t, repo.listCalls)
	})

	t.Run("без TeamRepo → ошибка, а не глобальный журнал", func(t *testing.T) {
		t.Parallel()
		repo := &capturingAuditRepo{}
		uc := NewAuditUsecase(repo, logging.NewNoop()) // WithTeams не вызван

		_, err := uc.ListAcrossTeams(context.Background(), "u1", port.AuditFilter{})
		require.Error(t, err)
		assert.Zero(t, repo.listCalls)
	})
}

// TestAuditUC_List_UnchangedByScope (§86.7): обычный List не задет — сквозной
// режим добавляет ветку, а не меняет существующую.
func TestAuditUC_List_UnchangedByScope(t *testing.T) {
	t.Parallel()
	repo := &capturingAuditRepo{}
	uc := newAuditScopeUC(repo, newScopeTeams())

	_, err := uc.List(context.Background(), port.AuditFilter{TeamID: "session-team"})
	require.NoError(t, err)

	assert.Equal(t, "session-team", repo.lastFilter.TeamID)
	assert.Empty(t, repo.lastFilter.TeamIDs)
}
