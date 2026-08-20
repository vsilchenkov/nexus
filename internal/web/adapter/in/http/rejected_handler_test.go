package http

import (
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
)

// TestRejectedFilterFromQuery: разбор фильтров выдачи (§94.6). Общая функция
// для списка, сводки и выгрузки — расхождение означало бы, что CSV содержит не
// то, что показано на экране.
func TestRejectedFilterFromQuery(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	newCtx := func(query string) *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/api/rejected?"+query, nil)
		return c
	}

	t.Run("defaults", func(t *testing.T) {
		t.Parallel()
		f := rejectedFilterFromQuery(newCtx(""), rejectedDefaultLimit, rejectedMaxLimit)
		assert.Equal(t, rejectedDefaultLimit, f.Limit)
		assert.False(t, f.IncludeResolved, "разобранные группы по умолчанию скрыты")
		assert.Empty(t, f.Reasons)
		assert.True(t, f.From.IsZero())
	})

	t.Run("period and paging", func(t *testing.T) {
		t.Parallel()
		f := rejectedFilterFromQuery(newCtx(
			"from=2026-08-19T00:00:00Z&to=2026-08-20T00:00:00Z&limit=25&offset=50"),
			rejectedDefaultLimit, rejectedMaxLimit)
		assert.Equal(t, time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC), f.From)
		assert.Equal(t, time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC), f.To)
		assert.Equal(t, 25, f.Limit)
		assert.Equal(t, 50, f.Offset)
	})

	t.Run("limit is capped", func(t *testing.T) {
		t.Parallel()
		f := rejectedFilterFromQuery(newCtx("limit=999999"), rejectedDefaultLimit, rejectedMaxLimit)
		assert.Equal(t, rejectedMaxLimit, f.Limit)
	})

	t.Run("reasons filtered to known codes", func(t *testing.T) {
		t.Parallel()
		f := rejectedFilterFromQuery(newCtx("reasons=node_not_found,выдумка,rate_limited"),
			rejectedDefaultLimit, rejectedMaxLimit)
		// Неизвестный код отбрасывается: найти он ничего не может, а в запросе
		// лишь удлинял бы условие.
		assert.Equal(t, []domain.RejectReason{
			domain.RejectReasonNodeNotFound, domain.RejectReasonRateLimited,
		}, f.Reasons)
	})

	t.Run("broken period is ignored, not fatal", func(t *testing.T) {
		t.Parallel()
		f := rejectedFilterFromQuery(newCtx("from=вчера"), rejectedDefaultLimit, rejectedMaxLimit)
		assert.True(t, f.From.IsZero(), "неразобранная граница означает «без ограничения»")
	})
}

// TestRejectedRoutesDoNotConflict: /summary и /export.csv соседствуют с /:id.
//
// Проверка не теоретическая: gin роняет ВЕСЬ роутер при старте, если
// маршруты конфликтуют, и обнаружилось бы это только при запуске сервиса
// (§78.2 — там на этом уже спотыкались с catch-all).
func TestRejectedRoutesDoNotConflict(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	var got []string
	assert.NotPanics(t, func() {
		r := gin.New()
		g := r.Group("/api/rejected")
		g.GET("", func(c *gin.Context) { got = append(got, "list") })
		g.GET("/summary", func(c *gin.Context) { got = append(got, "summary") })
		g.GET("/export.csv", func(c *gin.Context) { got = append(got, "export") })
		g.GET("/:id", func(c *gin.Context) { got = append(got, "get:"+c.Param("id")) })
		g.POST("/:id/resolve", func(c *gin.Context) { got = append(got, "resolve:"+c.Param("id")) })
		g.DELETE("/:id", func(c *gin.Context) { got = append(got, "delete:"+c.Param("id")) })

		for _, req := range []*http.Request{
			httptest.NewRequest(http.MethodGet, "/api/rejected", nil),
			httptest.NewRequest(http.MethodGet, "/api/rejected/summary", nil),
			httptest.NewRequest(http.MethodGet, "/api/rejected/export.csv", nil),
			httptest.NewRequest(http.MethodGet, "/api/rejected/abc-123", nil),
			httptest.NewRequest(http.MethodPost, "/api/rejected/abc-123/resolve", nil),
			httptest.NewRequest(http.MethodDelete, "/api/rejected/abc-123", nil),
		} {
			r.ServeHTTP(httptest.NewRecorder(), req)
		}
	})

	assert.Equal(t, []string{
		"list", "summary", "export", "get:abc-123", "resolve:abc-123", "delete:abc-123",
	}, got, "статический сегмент не должен попадать в :id")
}

// TestRejectedScopeFromSession: администратору достаются фильтры «команда» и
// «неопознанные», остальным — только их сессия.
func TestRejectedScopeFromSession(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &RejectedHandler{}
	newCtx := func(role domain.UserRole, query string) *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/api/rejected?"+query, nil)
		c.Set(ctxSessionKey, &domain.Session{UserID: "u1", Role: role, CurrentTeamID: "team-1"})
		return c
	}

	admin := h.scope(newCtx(domain.UserRoleAdmin, "team_slug=geo&unknown_team=true"))
	assert.True(t, admin.IsAdmin)
	assert.Equal(t, "geo", admin.TeamSlug)
	assert.True(t, admin.UnknownTeamOnly)
	assert.Equal(t, "team-1", admin.TeamID)

	op := h.scope(newCtx(domain.UserRoleOperator, "team_slug=geo&unknown_team=true"))
	assert.False(t, op.IsAdmin, "область видимости оператора задаёт сессия, а не query")
	assert.Equal(t, "team-1", op.TeamID)
}

// TestRejectedGroupResponse_NoBodyLeaks: наружу уходит размер тела, но не тело.
func TestRejectedGroupResponse_NoBodyLeaks(t *testing.T) {
	t.Parallel()

	s := rejectedSampleResponse{
		At: time.Now(), ClientIP: "10.0.0.1", HTTPMethod: "POST",
		RawPath: "/api/v1/vika/telephony?token=***", Status: 404, BodyBytes: 1234,
		Headers: map[string]string{"Authorization": "Bearer ***"},
	}
	raw, err := json.Marshal(s)
	require.NoError(t, err)

	body := string(raw)
	assert.Contains(t, body, `"body_bytes":1234`)
	assert.NotContains(t, strings.ToLower(body), `"body"`)
	assert.Contains(t, body, "Bearer ***")
}

// TestRejectedSummaryResponse_CarriesState: интерфейс обязан отличать «отказов
// не было» от «сбор выключен» (§94.5).
func TestRejectedSummaryResponse_CarriesState(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(rejectedSummaryResponse{
		Count: 0, Groups: 0, Clients: 0, Unresolved: 0,
		RetentionDays: 0, Collecting: false,
	})
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"retention_days":0`)
	assert.Contains(t, string(raw), `"collecting":false`)
}
