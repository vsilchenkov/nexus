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

// TestReplyTeamError_HasNodes (QA-2026-02 / П17): удаление команды с
// привязанными узлами должно отдавать 409 Conflict, а не 500.
func TestReplyTeamError_HasNodes(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &TeamHandler{logger: logging.NewNoop()}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("DELETE", "/api/teams/x", nil)

	h.replyTeamError(c, domain.ErrTeamHasNodes)

	require.Equal(t, http.StatusConflict, w.Code)
	var body map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.NotEmpty(t, body["error"])
}
