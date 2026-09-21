package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
	"nexus/internal/web/usecase/port"
)

// catalogListerStub — lister справочника с заранее заданным ответом.
type catalogListerStub struct {
	page port.CatalogNodesPage
	err  error
}

func (s catalogListerStub) NodesUsing(context.Context, string, []string, int) (port.CatalogNodesPage, error) {
	return s.page, s.err
}

// catalogUsageEngine — три маршрута §104 с одним и тем же lister'ом на все виды.
func catalogUsageEngine(l port.CatalogNodeLister) *gin.Engine {
	gin.SetMode(gin.TestMode)
	teams := &scopeTeamRepo{memberships: []*domain.UserTeam{
		{Team: domain.Team{ID: "t1", Slug: "logistics", Name: "Логистика"}},
	}}
	uc := usecase.NewCatalogUsageUsecase(map[usecase.CatalogKind]port.CatalogNodeLister{
		usecase.CatalogKindGroup: l, usecase.CatalogKindHeader: l, usecase.CatalogKindHost: l,
	}, teams, logging.NewNoop())
	h := NewCatalogUsageHandler(uc, logging.NewNoop())

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(ctxSessionKey, &domain.Session{UserID: "u1", Role: domain.UserRoleViewer, CurrentTeamID: "t1"})
		c.Next()
	})
	r.GET("/api/node-groups/:id/nodes", h.GroupNodes)
	r.GET("/api/headers/:id/nodes", h.HeaderNodes)
	r.GET("/api/allowed-hosts/:id/nodes", h.HostNodes)
	return r
}

func getCatalogNodes(r *gin.Engine, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func TestCatalogUsageHandler_OK(t *testing.T) {
	t.Parallel()
	r := catalogUsageEngine(catalogListerStub{page: port.CatalogNodesPage{
		Items: []port.CatalogNodeRef{{
			ID: "n1", Path: "1c/stock", TargetURL: "", URLMode: domain.URLModeFromRequest,
			RootMethod: domain.RootMethodRequestAsync, Status: domain.NodeStatusPaused, TeamID: "t1",
		}},
		Visible: 1, Total: 3,
	}})

	w := getCatalogNodes(r, "/api/allowed-hosts/h1/nodes")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp CatalogNodesResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, CatalogNodesResponse{
		Items: []CatalogNodeItem{{
			ID: "n1", Path: "1c/stock", URLMode: "from_request", RootMethod: "requestAsync",
			Status: "paused", TeamID: "t1", TeamSlug: "logistics", TeamName: "Логистика",
		}},
		Total: 3, Visible: 1, Hidden: 2,
	}, resp)
}

// Пустой список — `[]`, а не `null`: фронт делает items.map без проверки.
func TestCatalogUsageHandler_EmptyItemsIsArray(t *testing.T) {
	t.Parallel()
	r := catalogUsageEngine(catalogListerStub{page: port.CatalogNodesPage{Total: 4}})
	w := getCatalogNodes(r, "/api/headers/h1/nodes")
	require.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"items":[],"total":4,"visible":0,"hidden":4}`, w.Body.String())
}

func TestCatalogUsageHandler_Errors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		path string
		err  error
		want int
	}{
		{"группы нет", "/api/node-groups/g1/nodes", domain.ErrNodeGroupNotFound, http.StatusNotFound},
		{"заголовка нет", "/api/headers/h1/nodes", domain.ErrHeaderNotFound, http.StatusNotFound},
		{"хоста нет", "/api/allowed-hosts/h1/nodes", domain.ErrHostNotFound, http.StatusNotFound},
		{"сбой БД", "/api/node-groups/g1/nodes", errors.New("pg down"), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := getCatalogNodes(catalogUsageEngine(catalogListerStub{err: tt.err}), tt.path)
			assert.Equal(t, tt.want, w.Code, w.Body.String())
			assert.NotContains(t, w.Body.String(), "pg down", "текст внутренней ошибки не уходит клиенту")
		})
	}
}
