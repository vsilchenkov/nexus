package http

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/config"
	"nexus/internal/platform/logging"
)

// §90.2: Secure ставится по фактической схеме запроса, а не только по конфигу.
// Регресс, который это чинит: с session_cookie_secure=true вход по http://<IP>
// отвечал 200, но браузер отбрасывал Secure-куку, и следующий /api/auth/me
// получал 401 — SPA возвращала на форму входа (петля).

// newCookieTestHandler — handler с минимумом зависимостей: setSessionCookie
// использует только cfg и ttl, usecase в этих тестах не задействован.
func newCookieTestHandler(secure bool, sameSite string) *AuthHandler {
	cfg := &config.WebSection{
		SessionCookieName:     "nexus_session",
		SessionCookieSecure:   secure,
		SessionCookieSamesite: sameSite,
	}
	return NewAuthHandler(nil, cfg, func() time.Duration { return time.Hour }, logging.NewNoop())
}

// setCookieOn прогоняет setSessionCookie на запросе с заданной схемой и
// возвращает разобранную куку из ответа.
func setCookieOn(t *testing.T, h *AuthHandler, tlsOn bool, xfp, token string, maxAge int) *http.Cookie {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	if tlsOn {
		c.Request.TLS = &tls.ConnectionState{}
	}
	if xfp != "" {
		c.Request.Header.Set("X-Forwarded-Proto", xfp)
	}

	h.setSessionCookie(c, token, maxAge)

	cookies := (&http.Response{Header: rec.Header()}).Cookies()
	require.Len(t, cookies, 1, "ожидалась ровно одна Set-Cookie")
	return cookies[0]
}

func TestSetSessionCookie_SecureFollowsRequestScheme(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		cfgSecure  bool
		tlsOn      bool
		xfp        string
		wantSecure bool
	}{
		// Прямое TLS-соединение (без прокси).
		{"secure=true, прямой https", true, true, "", true},
		// Штатный прод: nginx терминирует TLS и сообщает схему заголовком.
		{"secure=true, за прокси с XFP=https", true, false, "https", true},
		{"secure=true, XFP=HTTPS (регистр не важен)", true, false, "HTTPS", true},
		{"secure=true, XFP-цепочка https,http", true, false, "https, http", true},
		// Ровно тот случай, ради которого сделан фикс: вход по IP без TLS.
		{"secure=true, plain http — Secure не ставим", true, false, "", false},
		{"secure=true, XFP=http", true, false, "http", false},
		// Аварийный выключатель: false запрещает Secure при любой схеме.
		{"secure=false, прямой https", false, true, "", false},
		{"secure=false, XFP=https", false, false, "https", false},
		{"secure=false, plain http", false, false, "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newCookieTestHandler(tt.cfgSecure, "strict")
			got := setCookieOn(t, h, tt.tlsOn, tt.xfp, "tok", 3600)

			assert.Equal(t, tt.wantSecure, got.Secure)
			assert.Equal(t, "nexus_session", got.Name)
			assert.Equal(t, "tok", got.Value)
			assert.True(t, got.HttpOnly, "session-cookie обязана быть HttpOnly")
			assert.Equal(t, "/", got.Path)
		})
	}
}

func TestSetSessionCookie_SameSiteFromConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		cfgValue string
		want     http.SameSite
	}{
		{"strict", http.SameSiteStrictMode},
		{"lax", http.SameSiteLaxMode},
		{"none", http.SameSiteNoneMode},
		{"", http.SameSiteStrictMode}, // неизвестное значение → самый строгий режим
	}

	for _, tt := range tests {
		t.Run("samesite="+tt.cfgValue, func(t *testing.T) {
			t.Parallel()
			h := newCookieTestHandler(true, tt.cfgValue)
			got := setCookieOn(t, h, true, "", "tok", 3600)
			assert.Equal(t, tt.want, got.SameSite)
		})
	}
}

// Удаляющая кука обязана нести те же атрибуты, что и установленная:
// раньше Logout не звал SetSameSite вовсе.
func TestSetSessionCookie_DeleteKeepsAttributes(t *testing.T) {
	t.Parallel()

	h := newCookieTestHandler(true, "lax")
	got := setCookieOn(t, h, false, "https", "", -1)

	assert.Equal(t, "", got.Value)
	assert.Less(t, got.MaxAge, 0, "maxAge<0 — команда удалить куку")
	assert.True(t, got.Secure)
	assert.Equal(t, http.SameSiteLaxMode, got.SameSite)
	assert.True(t, got.HttpOnly)
}
