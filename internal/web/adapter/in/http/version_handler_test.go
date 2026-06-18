package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() { gin.SetMode(gin.TestMode) }

// doVersion вызывает VersionHandler.Get и возвращает распарсенный ответ.
func doVersion(t *testing.T, h *VersionHandler) VersionResponse {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/version", nil)
	h.Get(c)
	require.Equal(t, http.StatusOK, w.Code)
	var resp VersionResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	return resp
}

func TestVersionHandler_ReturnsAllFields(t *testing.T) {
	t.Parallel()
	h := NewVersionHandler("1.2.3", "abc1234", "2026-06-18T10:00:00Z", false, nil)
	resp := doVersion(t, h)
	assert.Equal(t, "1.2.3", resp.Version)
	assert.Equal(t, "abc1234", resp.Commit)
	assert.Equal(t, "2026-06-18T10:00:00Z", resp.BuildDate)
	assert.False(t, resp.OverrideAllowed)
}

func TestVersionHandler_OverrideAppliedWhenAllowed(t *testing.T) {
	t.Parallel()
	override := func(context.Context) string { return "dev-local" }
	h := NewVersionHandler("1.2.3", "abc1234", "2026-06-18", true, override)
	resp := doVersion(t, h)
	assert.Equal(t, "dev-local", resp.Version, "override must replace version in dev")
	assert.Equal(t, "abc1234", resp.Commit, "commit stays ground truth")
	assert.Equal(t, "2026-06-18", resp.BuildDate)
	assert.True(t, resp.OverrideAllowed)
}

func TestVersionHandler_OverrideIgnoredWhenNotAllowed(t *testing.T) {
	t.Parallel()
	override := func(context.Context) string { return "dev-local" }
	h := NewVersionHandler("1.2.3", "abc1234", "2026-06-18", false, override)
	resp := doVersion(t, h)
	assert.Equal(t, "1.2.3", resp.Version, "override ignored when gate off (prod)")
	assert.False(t, resp.OverrideAllowed)
}

func TestVersionHandler_EmptyOverrideFallsBackToLdflags(t *testing.T) {
	t.Parallel()
	override := func(context.Context) string { return "" }
	h := NewVersionHandler("1.2.3", "abc1234", "2026-06-18", true, override)
	resp := doVersion(t, h)
	assert.Equal(t, "1.2.3", resp.Version, "empty override → git version")
}
