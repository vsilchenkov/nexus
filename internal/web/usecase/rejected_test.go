package usecase

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/clock"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// rejectedRepoStub — журнал отказов в памяти: запоминает фильтр последнего
// запроса (по нему проверяется область видимости) и отдаёт заданные группы.
type rejectedRepoStub struct {
	port.RejectedRepo

	groups     []*domain.RejectedGroup
	lastFilter port.RejectedFilter
	summary    port.RejectedSummary

	resolvedID string
	resolvedBy string
	deletedID  string
}

func (r *rejectedRepoStub) ListRejected(_ context.Context, f port.RejectedFilter) ([]*domain.RejectedGroup, error) {
	r.lastFilter = f
	return r.groups, nil
}

func (r *rejectedRepoStub) CountRejected(_ context.Context, f port.RejectedFilter) (int64, error) {
	r.lastFilter = f
	return int64(len(r.groups)), nil
}

func (r *rejectedRepoStub) SummaryRejected(_ context.Context, f port.RejectedFilter) (port.RejectedSummary, error) {
	r.lastFilter = f
	return r.summary, nil
}

func (r *rejectedRepoStub) GetRejected(_ context.Context, id string) (*domain.RejectedGroup, error) {
	for _, g := range r.groups {
		if g.ID == id {
			return g, nil
		}
	}
	return nil, domain.ErrRejectedGroupNotFound
}

func (r *rejectedRepoStub) ListRejectedClients(context.Context, string, int) ([]domain.RejectedClient, error) {
	return nil, nil
}

func (r *rejectedRepoStub) ListRejectedSamples(context.Context, string, int) ([]domain.RejectedSample, error) {
	return nil, nil
}

func (r *rejectedRepoStub) ResolveRejected(_ context.Context, id, by string, _ time.Time) error {
	r.resolvedID, r.resolvedBy = id, by
	return nil
}

func (r *rejectedRepoStub) DeleteRejected(_ context.Context, id string) error {
	r.deletedID = id
	return nil
}

// rejectedTeamRepo — резолв команды сессии в слог.
type rejectedTeamRepo struct {
	nopTeamRepo
	team *domain.Team
}

func (r *rejectedTeamRepo) GetByID(_ context.Context, id string) (*domain.Team, error) {
	if r.team != nil && r.team.ID == id {
		return r.team, nil
	}
	return nil, domain.ErrTeamNotFound
}

func newRejectedUC(t *testing.T, repo port.RejectedRepo, teams port.TeamRepo, nodes port.NodeRepo) *RejectedUsecase {
	t.Helper()
	return NewRejectedUsecase(repo, teams, nodes,
		NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()),
		NewRejectRetentionProvider(), logging.NewNoop(),
		WithRejectedClock(clock.Func(func() time.Time {
			return time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
		})))
}

func rejectedGroup(id, slug, path string) *domain.RejectedGroup {
	return &domain.RejectedGroup{
		ID: id,
		RejectedGroupKey: domain.RejectedGroupKey{
			TeamSlug: slug, NodePath: path,
			Reason: domain.RejectReasonNodeNotFound, HTTPMethod: "POST",
		},
		Status: 404, Count: 5,
	}
}

// TestRejectedList_ScopeForOperator: оператор видит только свою команду —
// фильтр обязан уехать в репозиторий, иначе выдача покажет чужие адреса.
func TestRejectedList_ScopeForOperator(t *testing.T) {
	t.Parallel()

	repo := &rejectedRepoStub{groups: []*domain.RejectedGroup{rejectedGroup("g1", "vika", "telephony")}}
	teams := &rejectedTeamRepo{team: &domain.Team{ID: "team-1", Slug: "vika"}}
	uc := newRejectedUC(t, repo, teams, nil)

	res, err := uc.List(context.Background(), RejectedScope{TeamID: "team-1"}, port.RejectedFilter{})
	require.NoError(t, err)
	assert.Len(t, res.Groups, 1)
	assert.Equal(t, []string{"vika"}, repo.lastFilter.TeamSlugs)
	assert.False(t, repo.lastFilter.UnknownTeamOnly, "неопознанные группы — привилегия администратора")
	assert.Equal(t, domain.RejectedRetentionDefaultDays, res.RetentionDays)
	assert.True(t, res.Collecting)
}

// TestRejectedList_ScopeForOperatorWithoutTeam: без команды в сессии выдача
// пуста — показывать всё было бы утечкой.
func TestRejectedList_ScopeForOperatorWithoutTeam(t *testing.T) {
	t.Parallel()

	repo := &rejectedRepoStub{}
	uc := newRejectedUC(t, repo, &rejectedTeamRepo{}, nil)

	_, err := uc.List(context.Background(), RejectedScope{}, port.RejectedFilter{})
	require.NoError(t, err)
	require.NotNil(t, repo.lastFilter.TeamSlugs, "nil означал бы «без ограничения», то есть все команды")
	assert.Empty(t, repo.lastFilter.TeamSlugs)
}

// TestRejectedList_ScopeForAdmin: администратор видит все команды, а фильтры
// «команда» и «неопознанные» работают только у него.
func TestRejectedList_ScopeForAdmin(t *testing.T) {
	t.Parallel()

	t.Run("no team restriction", func(t *testing.T) {
		t.Parallel()
		repo := &rejectedRepoStub{}
		uc := newRejectedUC(t, repo, &rejectedTeamRepo{}, nil)
		_, err := uc.List(context.Background(), RejectedScope{IsAdmin: true}, port.RejectedFilter{})
		require.NoError(t, err)
		assert.Nil(t, repo.lastFilter.TeamSlugs, "nil = ограничения нет")
	})

	t.Run("explicit team filter", func(t *testing.T) {
		t.Parallel()
		repo := &rejectedRepoStub{}
		uc := newRejectedUC(t, repo, &rejectedTeamRepo{}, nil)
		_, err := uc.List(context.Background(),
			RejectedScope{IsAdmin: true, TeamSlug: "geo", UnknownTeamOnly: true}, port.RejectedFilter{})
		require.NoError(t, err)
		assert.Equal(t, []string{"geo"}, repo.lastFilter.TeamSlugs)
		assert.True(t, repo.lastFilter.UnknownTeamOnly)
	})

	t.Run("team filter of a non-admin is ignored", func(t *testing.T) {
		t.Parallel()
		repo := &rejectedRepoStub{}
		teams := &rejectedTeamRepo{team: &domain.Team{ID: "team-1", Slug: "vika"}}
		uc := newRejectedUC(t, repo, teams, nil)
		// Оператор просит чужую команду — скоуп всё равно его собственный.
		_, err := uc.List(context.Background(),
			RejectedScope{TeamID: "team-1", TeamSlug: "geo", UnknownTeamOnly: true}, port.RejectedFilter{})
		require.NoError(t, err)
		assert.Equal(t, []string{"vika"}, repo.lastFilter.TeamSlugs)
		assert.False(t, repo.lastFilter.UnknownTeamOnly)
	})
}

// TestRejectedDetails_ForeignGroupLooksMissing: чужая группа отдаётся как «не
// найдено», а не как 403 — иначе по коду ответа восстанавливались бы адреса
// других команд.
func TestRejectedDetails_ForeignGroupLooksMissing(t *testing.T) {
	t.Parallel()

	repo := &rejectedRepoStub{groups: []*domain.RejectedGroup{rejectedGroup("g1", "geo", "find")}}
	teams := &rejectedTeamRepo{team: &domain.Team{ID: "team-1", Slug: "vika"}}
	uc := newRejectedUC(t, repo, teams, nil)

	_, err := uc.Details(context.Background(), RejectedScope{TeamID: "team-1"}, "g1")
	assert.ErrorIs(t, err, domain.ErrRejectedGroupNotFound)

	// Администратору та же группа доступна.
	_, err = uc.Details(context.Background(), RejectedScope{IsAdmin: true}, "g1")
	assert.NoError(t, err)
}

// TestRejectedResolveDelete_ScopeAndAudit: мутации проверяют область видимости
// тем же способом, что и чтение.
func TestRejectedResolveDelete_ScopeAndAudit(t *testing.T) {
	t.Parallel()

	repo := &rejectedRepoStub{groups: []*domain.RejectedGroup{rejectedGroup("g1", "vika", "telephony")}}
	teams := &rejectedTeamRepo{team: &domain.Team{ID: "team-1", Slug: "vika"}}
	uc := newRejectedUC(t, repo, teams, nil)
	actor := Actor{UserID: "u1", UserLogin: "operator", TeamID: "team-1"}

	require.NoError(t, uc.Resolve(context.Background(), RejectedScope{TeamID: "team-1"}, actor, "g1"))
	assert.Equal(t, "g1", repo.resolvedID)
	assert.Equal(t, "operator", repo.resolvedBy)

	require.NoError(t, uc.Delete(context.Background(), RejectedScope{TeamID: "team-1"}, actor, "g1"))
	assert.Equal(t, "g1", repo.deletedID)

	// Чужая команда — «не найдено», ничего не меняем.
	foreign := &rejectedRepoStub{groups: []*domain.RejectedGroup{rejectedGroup("g2", "geo", "find")}}
	uc2 := newRejectedUC(t, foreign, teams, nil)
	assert.ErrorIs(t, uc2.Resolve(context.Background(), RejectedScope{TeamID: "team-1"}, actor, "g2"),
		domain.ErrRejectedGroupNotFound)
	assert.Empty(t, foreign.resolvedID)
}

// TestRejectedSummary_IncludesResolved: «сколько отказов за сутки» не должно
// меняться от того, что кто-то нажал «разобрано».
func TestRejectedSummary_IncludesResolved(t *testing.T) {
	t.Parallel()

	repo := &rejectedRepoStub{summary: port.RejectedSummary{Count: 100, Groups: 4, Clients: 7, Unresolved: 2}}
	teams := &rejectedTeamRepo{team: &domain.Team{ID: "team-1", Slug: "vika"}}
	uc := newRejectedUC(t, repo, teams, nil)

	s, err := uc.Summary(context.Background(), RejectedScope{TeamID: "team-1"},
		port.RejectedFilter{IncludeResolved: false})
	require.NoError(t, err)
	assert.True(t, repo.lastFilter.IncludeResolved, "сводка считает и разобранные группы")
	assert.Equal(t, int64(100), s.Count)
	assert.Equal(t, int64(2), s.Unresolved, "бейдж светится по неразобранным")
	assert.True(t, s.Collecting)
}

// TestRejectedSuggestion: подсказка ищет ближайший узел команды и молчит там,
// где подсказывать нечего.
func TestRejectedSuggestion(t *testing.T) {
	t.Parallel()

	nodes := &fakeNodeRepo{list: []*domain.Node{
		{ID: "n1", Path: "telephony_v2"},
		{ID: "n2", Path: "sms"},
	}}
	teams := &rejectedTeamRepo{team: &domain.Team{ID: "team-1", Slug: "vika"}}

	t.Run("typo suggests the closest node", func(t *testing.T) {
		t.Parallel()
		g := rejectedGroup("g1", "vika", "telephony_v3")
		g.TeamID = "team-1"
		uc := newRejectedUC(t, &rejectedRepoStub{groups: []*domain.RejectedGroup{g}}, teams, nodes)

		d, err := uc.Details(context.Background(), RejectedScope{IsAdmin: true}, "g1")
		require.NoError(t, err)
		require.NotNil(t, d.Suggestion)
		assert.Equal(t, "telephony_v2", d.Suggestion.Path)
		assert.Equal(t, "n1", d.Suggestion.NodeID)
		assert.Equal(t, 1, d.Suggestion.Distance)
	})

	t.Run("distant path suggests nothing", func(t *testing.T) {
		t.Parallel()
		g := rejectedGroup("g1", "vika", "wp-login.php")
		g.TeamID = "team-1"
		uc := newRejectedUC(t, &rejectedRepoStub{groups: []*domain.RejectedGroup{g}}, teams, nodes)

		d, err := uc.Details(context.Background(), RejectedScope{IsAdmin: true}, "g1")
		require.NoError(t, err)
		assert.Nil(t, d.Suggestion)
	})

	t.Run("exact match suggests nothing", func(t *testing.T) {
		t.Parallel()
		// Путь совпал с существующим узлом: отказ был не про опечатку (узел
		// выключен, гонка с созданием), и подсказка вводила бы в заблуждение.
		g := rejectedGroup("g1", "vika", "sms")
		g.TeamID = "team-1"
		uc := newRejectedUC(t, &rejectedRepoStub{groups: []*domain.RejectedGroup{g}}, teams, nodes)

		d, err := uc.Details(context.Background(), RejectedScope{IsAdmin: true}, "g1")
		require.NoError(t, err)
		assert.Nil(t, d.Suggestion)
	})

	t.Run("unknown team suggests nothing", func(t *testing.T) {
		t.Parallel()
		// Слог не резолвится в команду — сравнивать не с чем, в чужие команды
		// заглядывать нельзя.
		g := rejectedGroup("g1", "nosuchteam", "telephony_v3")
		uc := newRejectedUC(t, &rejectedRepoStub{groups: []*domain.RejectedGroup{g}}, teams, nodes)

		d, err := uc.Details(context.Background(), RejectedScope{IsAdmin: true}, "g1")
		require.NoError(t, err)
		assert.Nil(t, d.Suggestion)
	})

	t.Run("non-404 reason suggests nothing", func(t *testing.T) {
		t.Parallel()
		// У 405 узел существует — подсказывать нечего.
		g := rejectedGroup("g1", "vika", "telephony_v3")
		g.TeamID = "team-1"
		g.Reason = domain.RejectReasonMethodNotAllowed
		uc := newRejectedUC(t, &rejectedRepoStub{groups: []*domain.RejectedGroup{g}}, teams, nodes)

		d, err := uc.Details(context.Background(), RejectedScope{IsAdmin: true}, "g1")
		require.NoError(t, err)
		assert.Nil(t, d.Suggestion)
	})
}

func TestLevenshtein(t *testing.T) {
	t.Parallel()

	tests := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"", "abc", 3},
		{"abc", "", 3},
		{"abc", "abc", 0},
		{"telephony", "telephony_v2", 3},
		{"sms", "sms2", 1},
		{"кириллица", "кириллица", 0},
		{"кириллица", "кирилица", 1},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, levenshtein(tt.a, tt.b), "%q vs %q", tt.a, tt.b)
	}
}
