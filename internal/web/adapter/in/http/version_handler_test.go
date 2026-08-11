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
	h := NewVersionHandler("1.2.3", "abc1234", "2026-06-18T10:00:00Z", false, nil, "", false)
	resp := doVersion(t, h)
	assert.Equal(t, "1.2.3", resp.Version)
	assert.Equal(t, "abc1234", resp.Commit)
	assert.Equal(t, "2026-06-18T10:00:00Z", resp.BuildDate)
	assert.False(t, resp.OverrideAllowed)
}

// §70.8: бейдж ноды. Пустой идентификатор поле не отдаёт (omitempty) — SPA
// действующей ноды не показывает чип.
func TestVersionHandler_Instance(t *testing.T) {
	t.Parallel()
	withID := doVersion(t, NewVersionHandler("1.2.3", "", "", false, nil, "kz", false))
	assert.Equal(t, "kz", withID.Instance)

	plain := doVersion(t, NewVersionHandler("1.2.3", "", "", false, nil, "", false))
	assert.Empty(t, plain.Instance)
}

func TestVersionHandler_OverrideAppliedWhenAllowed(t *testing.T) {
	t.Parallel()
	override := func(context.Context) string { return "dev-local" }
	h := NewVersionHandler("1.2.3", "abc1234", "2026-06-18", true, override, "", false)
	resp := doVersion(t, h)
	assert.Equal(t, "dev-local", resp.Version, "override must replace version in dev")
	assert.Equal(t, "abc1234", resp.Commit, "commit stays ground truth")
	assert.Equal(t, "2026-06-18", resp.BuildDate)
	assert.True(t, resp.OverrideAllowed)
}

func TestVersionHandler_OverrideIgnoredWhenNotAllowed(t *testing.T) {
	t.Parallel()
	override := func(context.Context) string { return "dev-local" }
	h := NewVersionHandler("1.2.3", "abc1234", "2026-06-18", false, override, "", false)
	resp := doVersion(t, h)
	assert.Equal(t, "1.2.3", resp.Version, "override ignored when gate off (prod)")
	assert.False(t, resp.OverrideAllowed)
}

func TestVersionHandler_EmptyOverrideFallsBackToLdflags(t *testing.T) {
	t.Parallel()
	override := func(context.Context) string { return "" }
	h := NewVersionHandler("1.2.3", "abc1234", "2026-06-18", true, override, "", false)
	resp := doVersion(t, h)
	assert.Equal(t, "1.2.3", resp.Version, "empty override → git version")
}

// §85.8: dev_mode — ОТДЕЛЬНЫЙ признак среды, а не производная от
// override_allowed. Тест держит их врозь: свяжи два поведения одним ключом — и
// выключенный на стенде override версии молча унесёт подсказку логина, а
// включённый на боевой установке вернёт `admin` на страницу входа.
func TestVersionHandler_DevModeIsIndependentOfOverrideGate(t *testing.T) {
	t.Parallel()

	dev := doVersion(t, NewVersionHandler("1.2.3", "", "", false, nil, "", true))
	assert.True(t, dev.DevMode, "стенд объявляет себя стендом даже без override версии")
	assert.False(t, dev.OverrideAllowed)

	prod := doVersion(t, NewVersionHandler("1.2.3", "", "", true, nil, "", false))
	assert.False(t, prod.DevMode, "разрешённый override версии НЕ делает установку стендом")
	assert.True(t, prod.OverrideAllowed)
}
