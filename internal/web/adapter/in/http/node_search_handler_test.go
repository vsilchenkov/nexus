package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
)

// TestNodeHandler_SearchAcrossTeams_Comment (§103): элемент выдачи глобального
// поиска несёт комментарий узла — без него фронт не построит сниппет для узла,
// найденного только по комментарию.
func TestNodeHandler_SearchAcrossTeams_Comment(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	repo := &scopeNodeRepo{nodes: []*domain.Node{{
		ID: "n1", Path: "svc/orders", TargetURL: "https://api.example.com/orders",
		RootMethod: domain.RootMethodRequest, Status: domain.NodeStatusEnabled,
		TeamID: "t1", Comment: "Интеграция со СДЭК",
	}}}
	teams := &scopeTeamRepo{memberships: []*domain.UserTeam{
		{Team: domain.Team{ID: "t1", Slug: "logistics", Name: "Логистика"}},
	}}
	uc := usecase.NewNodeUsecase(repo, nil,
		usecase.NewAuditUsecase(aqAuditRepo{}, logging.NewNoop()),
		nil, teams, nil, nil, time.Minute, 0, "session-team", nil, logging.NewNoop())
	h := NewNodeHandler(uc, nil, logging.NewNoop())

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(ctxSessionKey, &domain.Session{UserID: "u1", Role: domain.UserRoleViewer, CurrentTeamID: "t1"})
		c.Next()
	})
	r.GET("/api/search/nodes", h.SearchAcrossTeams)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/search/nodes?q=%D1%81%D0%B4%D1%8D%D0%BA", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		Items []map[string]any `json:"items"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Items, 1)
	assert.Equal(t, "Интеграция со СДЭК", resp.Items[0]["comment"])
	assert.Equal(t, "Логистика", resp.Items[0]["team_name"])
	assert.Equal(t, "сдэк", repo.lastFilter.Search, "поиск уходит в репозиторий как есть")
}
