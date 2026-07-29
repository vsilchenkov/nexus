package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
)

// stubPreferenceRepo — port.UserPreferenceRepo для unit-тестов (§71):
// запоминает upsert'ы, отдаёт настраиваемые список/ошибки.
type stubPreferenceRepo struct {
	items   []*domain.UserPreference
	listErr error
	setErr  error

	saved []savedPreference
}

type savedPreference struct {
	teamID     string
	key        string
	value      string
	maxPerUser int
}

func (r *stubPreferenceRepo) ListPreferences(context.Context, string) ([]*domain.UserPreference, error) {
	return r.items, r.listErr
}

func (r *stubPreferenceRepo) SetPreference(_ context.Context, p *domain.UserPreference, maxPerUser int) error {
	if r.setErr != nil {
		return r.setErr
	}
	r.saved = append(r.saved, savedPreference{
		teamID: p.TeamID, key: p.Key, value: string(p.Value), maxPerUser: maxPerUser,
	})
	return nil
}

// stubMembershipLister — TeamMembershipLister: пользователь состоит в teams.
type stubMembershipLister struct {
	teams []string
	err   error
}

func (l *stubMembershipLister) ListUserTeams(context.Context, string) ([]*domain.UserTeam, error) {
	if l.err != nil {
		return nil, l.err
	}
	out := make([]*domain.UserTeam, 0, len(l.teams))
	for _, id := range l.teams {
		out = append(out, &domain.UserTeam{Team: domain.Team{ID: id}})
	}
	return out, nil
}

const (
	prefUserID = "11111111-1111-1111-1111-111111111111"
	prefTeamA  = "22222222-2222-2222-2222-222222222222"
	prefTeamB  = "33333333-3333-3333-3333-333333333333"
)

func mkPrefUC(repo *stubPreferenceRepo, members *stubMembershipLister) *PreferenceUsecase {
	if members == nil {
		members = &stubMembershipLister{teams: []string{prefTeamA}}
	}
	return NewPreferenceUsecase(repo, members, logging.NewNoop())
}

func TestPreferenceUsecase_SetPreference_Valid(t *testing.T) {
	t.Parallel()

	repo := &stubPreferenceRepo{}
	uc := mkPrefUC(repo, nil)

	err := uc.SetPreference(context.Background(), prefUserID, prefTeamA,
		domain.PreferenceKeyOverviewPeriod, json.RawMessage(`{"kind":"preset","range":"7d"}`))
	require.NoError(t, err)

	require.Len(t, repo.saved, 1)
	assert.Equal(t, prefTeamA, repo.saved[0].teamID)
	assert.Equal(t, domain.PreferenceKeyOverviewPeriod, repo.saved[0].key)
	assert.JSONEq(t, `{"kind":"preset","range":"7d"}`, repo.saved[0].value)
	assert.Equal(t, maxPreferencesPerUser, repo.saved[0].maxPerUser, "потолок доезжает до репозитория")
}

func TestPreferenceUsecase_SetPreference_GlobalSkipsMembershipCheck(t *testing.T) {
	t.Parallel()

	repo := &stubPreferenceRepo{}
	// Пользователь не состоит ни в одной команде — глобальный преф всё равно
	// сохраняется: он не привязан к команде.
	uc := mkPrefUC(repo, &stubMembershipLister{err: errors.New("must not be called")})

	err := uc.SetPreference(context.Background(), prefUserID, "",
		domain.PreferenceKeyOverviewPeriod, json.RawMessage(`{"kind":"preset","range":"24h"}`))
	require.NoError(t, err)
	require.Len(t, repo.saved, 1)
	assert.Empty(t, repo.saved[0].teamID)
}

func TestPreferenceUsecase_SetPreference_Rejects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		teamID  string
		key     string
		value   json.RawMessage
		wantErr error
	}{
		{"empty key", prefTeamA, "", json.RawMessage(`1`), domain.ErrPreferenceKeyInvalid},
		{"upper case key", prefTeamA, "Overview.Period", json.RawMessage(`1`), domain.ErrPreferenceKeyInvalid},
		{"leading digit key", prefTeamA, "1period", json.RawMessage(`1`), domain.ErrPreferenceKeyInvalid},
		{"trailing dot key", prefTeamA, "overview.", json.RawMessage(`1`), domain.ErrPreferenceKeyInvalid},
		{"too long key", prefTeamA, strings.Repeat("a", 65), json.RawMessage(`1`), domain.ErrPreferenceKeyInvalid},

		{"nil value", prefTeamA, "overview.period", nil, domain.ErrPreferenceValueInvalid},
		{"malformed value", prefTeamA, "overview.period", json.RawMessage(`{oops`), domain.ErrPreferenceValueInvalid},
		{"null value", prefTeamA, "overview.period", json.RawMessage(`null`), domain.ErrPreferenceValueInvalid},
		{"oversized value", prefTeamA, "overview.period",
			json.RawMessage(`"` + strings.Repeat("x", 4096) + `"`), domain.ErrPreferenceValueInvalid},

		{"foreign team", prefTeamB, "overview.period", json.RawMessage(`1`), domain.ErrUserNotTeamMember},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repo := &stubPreferenceRepo{}
			uc := mkPrefUC(repo, nil)

			err := uc.SetPreference(context.Background(), prefUserID, tt.teamID, tt.key, tt.value)

			assert.True(t, errors.Is(err, tt.wantErr), "got %v, want %v", err, tt.wantErr)
			assert.Empty(t, repo.saved, "отклонённый преф не доходит до репозитория")
		})
	}
}

func TestPreferenceUsecase_SetPreference_LimitPropagates(t *testing.T) {
	t.Parallel()

	repo := &stubPreferenceRepo{setErr: domain.ErrPreferencesLimit}
	uc := mkPrefUC(repo, nil)

	err := uc.SetPreference(context.Background(), prefUserID, prefTeamA,
		domain.PreferenceKeyOverviewPeriod, json.RawMessage(`1`))
	assert.ErrorIs(t, err, domain.ErrPreferencesLimit, "ошибка хранилища доезжает до handler'а как есть")
}

func TestPreferenceUsecase_Preferences(t *testing.T) {
	t.Parallel()

	t.Run("returns items", func(t *testing.T) {
		t.Parallel()

		items := []*domain.UserPreference{
			{TeamID: "", Key: domain.PreferenceKeyOverviewPeriod, Value: json.RawMessage(`{"kind":"preset","range":"24h"}`)},
			{TeamID: prefTeamA, Key: domain.PreferenceKeyOverviewPeriod, Value: json.RawMessage(`{"kind":"preset","range":"7d"}`)},
		}
		uc := mkPrefUC(&stubPreferenceRepo{items: items}, nil)

		got := uc.Preferences(context.Background(), prefUserID)
		assert.Equal(t, items, got, "порядок репозитория сохраняется")
	})

	t.Run("storage failure degrades to empty", func(t *testing.T) {
		t.Parallel()

		uc := mkPrefUC(&stubPreferenceRepo{listErr: errors.New("db is down")}, nil)

		got := uc.Preferences(context.Background(), prefUserID)
		assert.NotNil(t, got, "не nil: сериализуется в [], а не в null")
		assert.Empty(t, got)
	})

	t.Run("nil normalized to empty slice", func(t *testing.T) {
		t.Parallel()

		uc := mkPrefUC(&stubPreferenceRepo{items: nil}, nil)

		got := uc.Preferences(context.Background(), prefUserID)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})
}
