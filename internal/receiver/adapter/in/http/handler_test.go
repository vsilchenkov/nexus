package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
)

// TestSplitTeamSlugAndPath фиксирует контракт парсинга URL Receiver'а
// (Phase 10.E.1): /api/v1/request/<team_slug>/<node_path> для multi-tenancy
// и /api/v1/request/<node_path> для legacy default-team.
//
// Cross-team изоляция в Receiver работает на уровне БД: NodeReader.Get
// делает SELECT через JOIN с teams (WHERE slug=$1 AND path=$2). Если
// клиент указал чужой team_slug, узел не найдётся — вернётся
// ErrNodeNotFound (404), а не утечка существования узла другой команды.
func TestSplitTeamSlugAndPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		raw          string
		wantTeamSlug string
		wantNodePath string
	}{
		// Catch-all gin всегда передаёт строку с ведущим '/'.
		{"multi-tenant simple", "/acme/order", "acme", "order"},
		{"multi-tenant nested", "/acme/order/v2", "acme", "order/v2"},
		{"multi-tenant deep", "/globex/foo/bar/baz", "globex", "foo/bar/baz"},
		{"legacy single segment", "/order", "", "order"},
		{"legacy with subpath", "/order_root", "", "order_root"},
		// Trim ведущего '/' и пустой ввод — конкретный handler потом
		// вернёт 400 на пустой node_path.
		{"empty raw", "", "", ""},
		{"only slash", "/", "", ""},
		// Без ведущего '/' (на всякий случай — Gin его всегда даёт, но
		// функция остаётся robust):
		{"no leading slash", "acme/order", "acme", "order"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotTeam, gotPath := splitTeamSlugAndPath(tc.raw)
			assert.Equal(t, tc.wantTeamSlug, gotTeam, "team_slug mismatch")
			assert.Equal(t, tc.wantNodePath, gotPath, "node_path mismatch")
		})
	}
}

// TestClassifyDomainError фиксирует маппинг доменных ошибок в HTTP-коды —
// общий для sync и async ответов (§3, #5/#7).
func TestClassifyDomainError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		err          error
		wantStatus   int
		wantInternal bool
	}{
		{"not found", domain.ErrNodeNotFound, http.StatusNotFound, false},
		{"disabled", domain.ErrNodeDisabled, http.StatusServiceUnavailable, false},
		{"method not allowed", domain.ErrNodeMethodNotAllowed, http.StatusMethodNotAllowed, false},
		{"url not allowed", domain.ErrURLNotAllowed, http.StatusForbidden, false},
		{"unauthorized", domain.ErrUnauthorized, http.StatusUnauthorized, false},
		{"unknown -> 502 internal", assert.AnError, http.StatusBadGateway, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			status, msg, internal := classifyDomainError(tc.err)
			assert.Equal(t, tc.wantStatus, status)
			assert.Equal(t, tc.wantInternal, internal)
			assert.NotEmpty(t, msg)
		})
	}
}

// TestReplyAsyncError_BodyShape: async-ошибка отвечает {"result":false,"message":...}
// (§3, #7), а не {"error":...} как sync-вариант.
func TestReplyAsyncError_BodyShape(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	h := &Handler{logger: logging.NewNoop()}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/requestAsync/demo", nil)

	h.replyAsyncError(c, domain.ErrNodeNotFound, "demo", "test")

	require.Equal(t, http.StatusNotFound, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, false, body["result"])
	assert.Equal(t, "node not found", body["message"])
	_, hasError := body["error"]
	assert.False(t, hasError, "async error body must not use the sync 'error' key")
}
