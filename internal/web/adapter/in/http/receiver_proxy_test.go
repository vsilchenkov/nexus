package http

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/logging"
)

// TestReceiverProxy_ForwardsAllV1Paths (§78.2) — Web проксирует в Receiver ВСЁ
// под /api/v1/ дословно: и legacy-формы адреса, и короткую (§78.1). Разбирать
// форму Web не должен — этим занимается Receiver.
//
// Заодно фиксирует, что catch-all не перехватывает админский REST (/api/…) и
// не мешает SPA-фолбэку: до §78 маршрутов было три, и легко было бы получить
// обратное.
func TestReceiverProxy_ForwardsAllV1Paths(t *testing.T) {
	var gotPaths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("receiver"))
	}))
	defer upstream.Close()

	gin.SetMode(gin.TestMode)
	r := gin.New()
	// Куски реального дерева Web: админский REST и SPA-фолбэк.
	r.GET("/api/nodes", func(c *gin.Context) { c.String(http.StatusOK, "nodes") })
	r.GET("/api/nodes/:id", func(c *gin.Context) { c.String(http.StatusOK, "node") })
	r.GET("/api/settings/public", func(c *gin.Context) { c.String(http.StatusOK, "settings") })
	r.NoRoute(func(c *gin.Context) { c.String(http.StatusNotFound, "spa") })

	require.NotPanics(t, func() {
		RegisterReceiverProxy(r, upstream.URL, logging.NewNoop())
	}, "catch-all /api/v1/*path обязан регистрироваться рядом с деревом /api/…")

	// Через настоящий сервер, а не ResponseRecorder: httputil.ReverseProxy с
	// FlushInterval>0 требует CloseNotifier, которого у рекордера нет.
	web := httptest.NewServer(r)
	defer web.Close()

	proxied := []string{
		"/api/v1/request/webhook/sbp-qr",
		"/api/v1/requestAsync/webhook/sbp-qr",
		"/api/v1/callback/webhook/sbp-qr",
		"/api/v1/webhook/sbp-qr", // короткая форма §78.1
		"/api/v1/sbp-qr",         // короткая форма без слога команды
	}
	for _, p := range proxied {
		code, body := httpGet(t, http.MethodPost, web.URL+p)
		assert.Equal(t, http.StatusOK, code, "путь %s обязан уходить в Receiver", p)
		assert.Equal(t, "receiver", body, "ответ Receiver возвращается как есть (%s)", p)
	}
	assert.Equal(t, proxied, gotPaths, "путь обязан доезжать до Receiver дословно")

	// Админский REST и SPA не задеты.
	for path, want := range map[string]string{
		"/api/nodes":           "nodes",
		"/api/nodes/abc":       "node",
		"/api/settings/public": "settings",
		"/nodes/abc":           "spa",
	} {
		_, body := httpGet(t, http.MethodGet, web.URL+path)
		assert.Equal(t, want, body, "путь %s не должен попадать в прокси Receiver", path)
	}
}

// httpGet — запрос к тестовому серверу; возвращает код и тело.
func httpGet(t *testing.T, method, url string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	buf := make([]byte, 1024)
	n, _ := resp.Body.Read(buf)
	return resp.StatusCode, string(buf[:n])
}

// TestReceiverProxy_DisabledWithoutURL — пустой web.receiver_url оставляет
// маршруты незарегистрированными (single-process dev): запрос падает в NoRoute.
func TestReceiverProxy_DisabledWithoutURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.NoRoute(func(c *gin.Context) { c.String(http.StatusNotFound, "spa") })

	RegisterReceiverProxy(r, "", logging.NewNoop())

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/webhook/sbp-qr", nil))
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Equal(t, "spa", w.Body.String())
}
