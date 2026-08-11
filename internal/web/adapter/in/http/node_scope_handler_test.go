package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
	"nexus/internal/web/usecase/port"
)

// scopeNodeRepo — репозиторий узлов, запоминающий фильтр последнего List (§86).
// Нужен, чтобы отличить «сквозной режим отработал» от «молча свалился в скоуп
// одной команды» — по внешнему коду ответа эти случаи неразличимы.
type scopeNodeRepo struct {
	aqNodeRepo
	nodes      []*domain.Node
	lastFilter port.ListNodesFilter
	listCalls  int
	created    *domain.Node
}

func (r *scopeNodeRepo) List(_ context.Context, f port.ListNodesFilter) ([]*domain.Node, error) {
	r.lastFilter = f
	r.listCalls++
	return r.nodes, nil
}

func (r *scopeNodeRepo) Create(_ context.Context, n *domain.Node) error {
	r.created = n
	return nil
}

// scopeTeamRepo — port.TeamRepo, из которого сквозному режиму нужен ровно один
// метод: ListUserTeams. Остальные — заглушки (интерфейс широкий, §18).
type scopeTeamRepo struct {
	memberships []*domain.UserTeam
}

func (r *scopeTeamRepo) ListUserTeams(_ context.Context, _ string) ([]*domain.UserTeam, error) {
	return r.memberships, nil
}
func (r *scopeTeamRepo) GetByID(_ context.Context, _ string) (*domain.Team, error) {
	return nil, domain.ErrTeamNotFound
}
func (r *scopeTeamRepo) GetBySlug(_ context.Context, _ string) (*domain.Team, error) {
	return nil, domain.ErrTeamNotFound
}
func (r *scopeTeamRepo) List(_ context.Context) ([]*domain.Team, error)    { return nil, nil }
func (r *scopeTeamRepo) Create(_ context.Context, _ *domain.Team) error    { return nil }
func (r *scopeTeamRepo) Update(_ context.Context, _ *domain.Team) error    { return nil }
func (r *scopeTeamRepo) Delete(_ context.Context, _ string) error          { return nil }
func (r *scopeTeamRepo) RemoveMember(_ context.Context, _, _ string) error { return nil }
func (r *scopeTeamRepo) AddMember(_ context.Context, _, _ string, _ domain.TeamRole) error {
	return nil
}
func (r *scopeTeamRepo) UpdateMemberRole(_ context.Context, _, _ string, _ domain.TeamRole) error {
	return nil
}
func (r *scopeTeamRepo) ListMembers(_ context.Context, _ string) ([]*domain.TeamMember, error) {
	return nil, nil
}
func (r *scopeTeamRepo) ListTeamsByUsers(_ context.Context, _ []string) (map[string][]*domain.UserTeam, error) {
	return nil, nil
}

// scopeEngine собирает GET /api/nodes с сессией и, опционально, с признаком
// API-токена в контексте (как это делает api_token_middleware).
func scopeEngine(repo *scopeNodeRepo, teams port.TeamRepo, asAPIToken bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	uc := usecase.NewNodeUsecase(repo, nil,
		usecase.NewAuditUsecase(aqAuditRepo{}, logging.NewNoop()),
		nil, teams, nil, nil, time.Minute, 0, "session-team", nil, logging.NewNoop())
	h := NewNodeHandler(uc, nil, logging.NewNoop())

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(ctxSessionKey, &domain.Session{
			UserID: "u1", Role: domain.UserRoleViewer, CurrentTeamID: "session-team",
		})
		if asAPIToken {
			c.Set(ctxAPITokenKey, &domain.APIToken{TeamID: "session-team"})
		}
		c.Next()
	})
	r.GET("/api/nodes", h.List)
	r.POST("/api/nodes", h.Create)
	return r
}

// createBody — минимальное валидное тело создания узла; team задаётся отдельно,
// чтобы тесты §86.6 отличались от него ровно одним полем.
//
// Имя таблицы дано сразу с префиксом БД: тогда normalizeCHTable выходит рано и
// не требует резолва команды — префикс проверяется отдельным unit-тестом
// usecase, где он и живёт.
func createBody(teamField string) string {
	return `{` + teamField + `"path":"svc/parcel","root_method":"request",` +
		`"target_url":"https://api.example.com/v1","clickhouse_table":"nexus_test.parcel"}`
}

// TestNodeHandler_Create_TeamID (§86.6): поле team_id необязательное, но
// валидируемое; пустое — прежний контракт «создать в команде сессии».
func TestNodeHandler_Create_TeamID(t *testing.T) {
	t.Parallel()

	t.Run("нет поля → команда сессии (старые клиенты не ломаются)", func(t *testing.T) {
		t.Parallel()
		repo := &scopeNodeRepo{}
		r := scopeEngine(repo, &scopeTeamRepo{memberships: scopeMemberships()}, false)

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/nodes", strings.NewReader(createBody("")))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)

		require.Equal(t, http.StatusCreated, w.Code)
		assert.Equal(t, "session-team", repo.created.TeamID)
	})

	t.Run("невалидный UUID → 400, узел не создаётся", func(t *testing.T) {
		t.Parallel()
		repo := &scopeNodeRepo{}
		r := scopeEngine(repo, &scopeTeamRepo{memberships: scopeMemberships()}, false)

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/nodes",
			strings.NewReader(createBody(`"team_id":"not-a-uuid",`)))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)

		require.Equal(t, http.StatusBadRequest, w.Code)
		assert.Nil(t, repo.created)
	})

	t.Run("чужая команда → 403, узел не создаётся", func(t *testing.T) {
		t.Parallel()
		repo := &scopeNodeRepo{}
		// Пользователь состоит в team1/team2, а просит создать в третьей.
		r := scopeEngine(repo, &scopeTeamRepo{memberships: scopeMemberships()}, false)

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/nodes",
			strings.NewReader(createBody(`"team_id":"8f14e45f-ceea-467a-9575-7bd0e2c1a111",`)))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)

		require.Equal(t, http.StatusForbidden, w.Code)
		assert.Nil(t, repo.created, "чужая команда не должна доходить до репозитория")
	})
}

func scopeMemberships() []*domain.UserTeam {
	return []*domain.UserTeam{
		{Team: domain.Team{ID: "team1", Slug: "alpha", Name: "Alpha"}, Role: domain.TeamRoleMember},
		{Team: domain.Team{ID: "team2", Slug: "beta", Name: "Beta"}, Role: domain.TeamRoleMember},
	}
}

// TestNodeHandler_List_ScopeAll (§86.3): scope=all под сессией уходит в скоуп
// членств, а не текущей команды.
func TestNodeHandler_List_ScopeAll(t *testing.T) {
	t.Parallel()
	repo := &scopeNodeRepo{nodes: []*domain.Node{
		{ID: "n1", TeamID: "team1", Path: "svc/parcel"},
		{ID: "n2", TeamID: "team2", Path: "svc/parcel"},
	}}
	r := scopeEngine(repo, &scopeTeamRepo{memberships: scopeMemberships()}, false)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/nodes?scope=all", nil))

	require.Equal(t, http.StatusOK, w.Code)
	assert.ElementsMatch(t, []string{"team1", "team2"}, repo.lastFilter.TeamIDs)
	assert.Empty(t, repo.lastFilter.TeamID, "команда сессии не подмешивается в сквозной скоуп")

	var body struct {
		Items []NodeResponse `json:"items"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Len(t, body.Items, 2)
	// team_id обязан доехать до клиента: имя команды фронт резолвит по нему
	// самостоятельно, сервер выдачу не обогащает (§86.3).
	assert.Equal(t, "team1", body.Items[0].TeamID)
	assert.Equal(t, "team2", body.Items[1].TeamID)
}

// TestNodeHandler_List_ScopeAll_DeniedForAPIToken (§86.3, §86.9): API-токен
// закреплён за одной командой, сквозной режим ему недоступен.
func TestNodeHandler_List_ScopeAll_DeniedForAPIToken(t *testing.T) {
	t.Parallel()
	repo := &scopeNodeRepo{}
	r := scopeEngine(repo, &scopeTeamRepo{memberships: scopeMemberships()}, true)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/nodes?scope=all", nil))

	require.Equal(t, http.StatusForbidden, w.Code)
	// Отказ обязан быть именно отказом, а не тихим откатом в команду токена:
	// иначе клиент считал бы, что видит все команды, глядя на одну.
	assert.Zero(t, repo.listCalls, "при отказе запрос в репозиторий не уходит")
}

// TestNodeHandler_List_DefaultScopeUnchanged (§86.3): без параметра и с чужим
// значением scope поведение прежнее — команда сессии.
func TestNodeHandler_List_DefaultScopeUnchanged(t *testing.T) {
	t.Parallel()

	for _, q := range []string{"", "?scope=", "?scope=team", "?scope=ALLX"} {
		t.Run("query"+q, func(t *testing.T) {
			t.Parallel()
			repo := &scopeNodeRepo{}
			r := scopeEngine(repo, &scopeTeamRepo{memberships: scopeMemberships()}, false)

			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/nodes"+q, nil))

			require.Equal(t, http.StatusOK, w.Code)
			assert.Equal(t, "session-team", repo.lastFilter.TeamID)
			assert.Empty(t, repo.lastFilter.TeamIDs, "неизвестный scope не включает сквозной режим")
		})
	}
}

// TestWantsAllTeams (§86.3): регистр и пробелы нормализуются — значение приходит
// из адресной строки, где его может набрать человек.
func TestWantsAllTeams(t *testing.T) {
	t.Parallel()

	cases := []struct {
		query string
		want  bool
	}{
		{"scope=all", true},
		{"scope=ALL", true},
		{"scope=%20all%20", true},
		{"scope=", false},
		{"", false},
		{"scope=alll", false},
		{"scope=*", false},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			t.Parallel()
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/?"+tc.query, nil)
			assert.Equal(t, tc.want, wantsAllTeams(c))
		})
	}
}
