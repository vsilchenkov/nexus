package usecase

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// scopeTeams — TeamRepo, умеющий и членства, и резолв команды по id: второе
// нужно normalizeCHTable, чтобы проверить, что префикс БД ClickHouse следует за
// ВЫБРАННОЙ командой (§86.6), а не за командой сессии.
type scopeTeams struct {
	nopTeamRepo
	memberships []*domain.UserTeam
	byID        map[string]domain.Team
	listCalls   int
}

func (r *scopeTeams) ListUserTeams(context.Context, string) ([]*domain.UserTeam, error) {
	r.listCalls++
	return r.memberships, nil
}

func (r *scopeTeams) GetByID(_ context.Context, id string) (*domain.Team, error) {
	t, ok := r.byID[id]
	if !ok {
		return nil, domain.ErrTeamNotFound
	}
	return &t, nil
}

func newScopeTeams() *scopeTeams {
	alpha := domain.Team{ID: "team1", Slug: "alpha", Name: "Alpha", CHDatabase: "nexus_alpha"}
	beta := domain.Team{ID: "team2", Slug: "beta", Name: "Beta", CHDatabase: "nexus_beta"}
	return &scopeTeams{
		memberships: []*domain.UserTeam{
			{Team: alpha, Role: domain.TeamRoleMember},
			{Team: beta, Role: domain.TeamRoleMember},
		},
		byID: map[string]domain.Team{alpha.ID: alpha, beta.ID: beta},
	}
}

// newNodeCreate — узел-заготовка для тестов создания.
func newNodeCreate(teamID, table string) *domain.Node {
	return &domain.Node{
		TeamID:          teamID,
		Path:            "svc/parcel",
		RootMethod:      domain.RootMethodRequest,
		TargetURL:       "https://api.example.com/v1",
		ClickHouseTable: table,
	}
}

// TestNodeUC_Create_ExplicitTeam (§86.6): команда с формы применяется, а префикс
// БД ClickHouse следует за НЕЙ, а не за командой сессии.
func TestNodeUC_Create_ExplicitTeam(t *testing.T) {
	t.Parallel()
	repo := newMemNodeRepo()
	teams := newScopeTeams()
	uc := newSearchUC(repo, teams)
	actor := Actor{UserID: "u1", UserLogin: "alice", TeamID: "team1"} // сессия в alpha

	n := newNodeCreate("team2", "parcel") // а узел создаём в beta
	require.NoError(t, uc.Create(context.Background(), actor, n))

	assert.Equal(t, "team2", n.TeamID)
	// Главное: неверный префикс здесь означал бы, что узел одной команды пишет
	// логи в БД другой, и заметить это можно было бы только по факту.
	assert.Equal(t, "nexus_beta.parcel", n.ClickHouseTable)
}

// TestNodeUC_Create_AuditCarriesTeam (§86.6): команда больше не подразумевается
// сессией, поэтому обязана быть в журнале явно.
func TestNodeUC_Create_AuditCarriesTeam(t *testing.T) {
	t.Parallel()
	auditRepo := &stubAuditRepo{}
	teams := newScopeTeams()
	uc := NewNodeUsecase(newMemNodeRepo(), nopNodeCache{}, NewAuditUsecase(auditRepo, logging.NewNoop()),
		nil, teams, nil, nil, time.Minute, 0, "default-team", nil, logging.NewNoop())
	actor := Actor{UserID: "u1", UserLogin: "alice", TeamID: "team1"}

	require.NoError(t, uc.Create(context.Background(), actor, newNodeCreate("team2", "parcel")))

	require.Len(t, auditRepo.entries, 1)
	assert.Equal(t, "team2", auditRepo.entries[0].Details["team_id"],
		"без команды в журнале нельзя восстановить, куда узел был создан")
}

// TestNodeUC_Create_ForeignTeam_Denied (§86.6, §86.9): команда вне членств —
// отказ, и узел не создаётся.
func TestNodeUC_Create_ForeignTeam_Denied(t *testing.T) {
	t.Parallel()
	repo := newMemNodeRepo()
	teams := newScopeTeams()
	uc := newSearchUC(repo, teams)
	actor := Actor{UserID: "u1", UserLogin: "alice", TeamID: "team1"}

	n := newNodeCreate("team-foreign", "parcel")
	err := uc.Create(context.Background(), actor, n)

	require.ErrorIs(t, err, domain.ErrPermissionDenied)
	// Проверка обязана стоять ДО подготовки узла: иначе normalizeCHTable уже
	// сходил бы в PG за ch_database чужой команды, а лимит узлов посчитался бы
	// по ней же. Признак — нетронутое имя таблицы.
	assert.Equal(t, "parcel", n.ClickHouseTable, "подготовка узла не должна была начаться")
	nodes, _ := repo.List(context.Background(), port.ListNodesFilter{TeamID: "team-foreign"})
	assert.Empty(t, nodes)
}

// TestNodeUC_Create_TeamFallbackAndTrust (§86.6): обратная совместимость и
// отсутствие лишнего запроса в PG на общем пути.
func TestNodeUC_Create_TeamFallbackAndTrust(t *testing.T) {
	t.Parallel()

	t.Run("команда сессии → членства не перечитываются", func(t *testing.T) {
		t.Parallel()
		teams := newScopeTeams()
		uc := newSearchUC(newMemNodeRepo(), teams)
		actor := Actor{UserID: "u1", UserLogin: "alice", TeamID: "team1"}

		require.NoError(t, uc.Create(context.Background(), actor, newNodeCreate("team1", "parcel")))
		// Якорь доверия для команды сессии — логин и switch-team (§18.3/§18.9);
		// дублировать его запросом на каждое создание узла незачем.
		assert.Zero(t, teams.listCalls, "на общем пути членства не перечитываются")
	})

	t.Run("пустая команда → узел уходит в default (прежний контракт)", func(t *testing.T) {
		t.Parallel()
		teams := newScopeTeams()
		uc := newSearchUC(newMemNodeRepo(), teams)
		// Actor без команды: путь CLI/legacy, где team_id не приходит ниоткуда.
		n := newNodeCreate("", "")
		require.NoError(t, uc.Create(context.Background(), Actor{UserID: "u1"}, n))
		assert.Equal(t, "default-team", n.TeamID)
		assert.Zero(t, teams.listCalls)
	})
}

// TestNodeUC_ListAcrossTeams_Scope (§86.2): скоуп сквозного списка — членства
// пользователя, а не команда сессии.
func TestNodeUC_ListAcrossTeams_Scope(t *testing.T) {
	t.Parallel()
	repo := &capturingNodeRepo{
		memNodeRepo: newMemNodeRepo(),
		listResult: []*domain.Node{
			{ID: "n1", TeamID: "team1", Path: "svc/parcel"},
			{ID: "n2", TeamID: "team2", Path: "svc/parcel"}, // тот же путь в другой команде — норма
		},
	}
	teams := &membershipTeamRepo{memberships: []*domain.UserTeam{
		{Team: domain.Team{ID: "team1", Slug: "alpha", Name: "Alpha"}, Role: domain.TeamRoleMember},
		{Team: domain.Team{ID: "team2", Slug: "beta", Name: "Beta"}, Role: domain.TeamRoleMember},
	}}
	uc := newSearchUC(repo, teams)

	// f.TeamID заполнен командой сессии — сквозной режим обязан его отбросить,
	// иначе в репозиторий уехали бы два конфликтующих скоупа сразу.
	nodes, err := uc.ListAcrossTeams(context.Background(), "u1", port.ListNodesFilter{TeamID: "session-team"})
	require.NoError(t, err)
	require.Len(t, nodes, 2)

	assert.ElementsMatch(t, []string{"team1", "team2"}, repo.lastFilter.TeamIDs)
	assert.Empty(t, repo.lastFilter.TeamID, "команда сессии не должна попадать в сквозной фильтр")
}

// TestNodeUC_ListAcrossTeams_KeepsFilters (§86.3): поиск, метод и лимит работают
// так же, как в обычном списке — сквозной режим меняет только скоуп.
func TestNodeUC_ListAcrossTeams_KeepsFilters(t *testing.T) {
	t.Parallel()
	repo := &capturingNodeRepo{memNodeRepo: newMemNodeRepo()}
	teams := &membershipTeamRepo{memberships: []*domain.UserTeam{
		{Team: domain.Team{ID: "team1", Slug: "alpha"}, Role: domain.TeamRoleMember},
	}}

	_, err := newSearchUC(repo, teams).ListAcrossTeams(context.Background(), "u1", port.ListNodesFilter{
		Search:     "parcel",
		RootMethod: "request",
		Limit:      50,
		Offset:     10,
	})
	require.NoError(t, err)

	assert.Equal(t, "parcel", repo.lastFilter.Search)
	assert.Equal(t, "request", repo.lastFilter.RootMethod)
	assert.Equal(t, 50, repo.lastFilter.Limit)
	assert.Equal(t, 10, repo.lastFilter.Offset)
}

// TestNodeUC_ListAcrossTeams_Guards (§86.2, §86.9): деградационные ветки.
func TestNodeUC_ListAcrossTeams_Guards(t *testing.T) {
	t.Parallel()

	t.Run("нет членств → пустой список, репозиторий не вызван", func(t *testing.T) {
		t.Parallel()
		repo := &capturingNodeRepo{memNodeRepo: newMemNodeRepo()}
		nodes, err := newSearchUC(repo, &membershipTeamRepo{}).ListAcrossTeams(
			context.Background(), "u1", port.ListNodesFilter{})
		require.NoError(t, err)
		assert.Empty(t, nodes)
		// Ключевая проверка: пустой TeamIDs в репозитории означал бы «фильтра
		// нет» → выдачу узлов ВСЕХ команд инстанса. Запрос не должен уйти вовсе.
		assert.Zero(t, repo.listCalls, "без членств запрос в репозиторий не уходит")
	})

	t.Run("без TeamRepo → ошибка, а не тихий сквозной доступ", func(t *testing.T) {
		t.Parallel()
		repo := &capturingNodeRepo{memNodeRepo: newMemNodeRepo()}
		_, err := newSearchUC(repo, nil).ListAcrossTeams(context.Background(), "u1", port.ListNodesFilter{})
		require.Error(t, err)
		assert.Zero(t, repo.listCalls)
	})
}
