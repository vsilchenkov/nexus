package http

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"nexus/internal/domain"
)

// TestActorFromCtx фиксирует атрибуцию актёра аудита: логин берётся из сессии
// (раньше терялся и все действия писались как "system" при верном user_id),
// при отсутствии сессии — SystemActor, а legacy-сессия без поля Login
// деградирует в "system" без падения.
func TestActorFromCtx(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	t.Run("session login becomes actor login", func(t *testing.T) {
		t.Parallel()
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("GET", "/", nil)
		c.Set(ctxSessionKey, &domain.Session{UserID: "u1", Login: "alice", CurrentTeamID: "team1"})

		a := actorFromCtx(c)
		assert.Equal(t, "u1", a.UserID)
		assert.Equal(t, "alice", a.UserLogin)
		assert.Equal(t, "team1", a.TeamID)
	})

	t.Run("no session falls back to system actor", func(t *testing.T) {
		t.Parallel()
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("GET", "/", nil)

		a := actorFromCtx(c)
		assert.Empty(t, a.UserID)
		assert.Equal(t, "system", a.UserLogin)
	})

	t.Run("legacy session without login keeps system label", func(t *testing.T) {
		t.Parallel()
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("GET", "/", nil)
		c.Set(ctxSessionKey, &domain.Session{UserID: "u2", Login: ""})

		a := actorFromCtx(c)
		assert.Equal(t, "u2", a.UserID)
		assert.Equal(t, "system", a.UserLogin)
	})
}
