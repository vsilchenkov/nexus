package http

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newSPARouter — роутер с SPA-раздачей поверх fstest.MapFS. MapFS выбран
// намеренно: у её файлов, как и у embed.FS, нулевой ModTime, поэтому
// http.ServeContent не ставит Last-Modified — ровно то поведение, ради которого
// в §89.1 появились явные Cache-Control.
func newSPARouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.NoRoute(func(c *gin.Context) { c.Status(http.StatusNotFound) })
	SPAFallback(r, fstest.MapFS{
		"index.html": {Data: []byte("<html><body>nexus</body></html>")},
		// Полезная нагрузка НАМЕРЕННО без сигнатуры "wOF2": Go умеет опознать
		// её сниффером (net/http/sniff.go), и с настоящими байтами тест
		// проходил бы и без нашего заголовка — то есть проверял бы везение, а не
		// поведение. Здесь же сниффер дал бы text/plain, поэтому font/woff2
		// может взяться только из assetContentType по расширению.
		"assets/inter-latin-Dx4kXJAl.woff2": {Data: []byte("fake-font-payload-no-magic")},
		"assets/index-Dy_Q_gNU.js":          {Data: []byte("export const a = 1;\n")},
		"assets/index-Dy_Q_gNU.js.map":      {Data: []byte(`{"version":3}`)},
		"assets/index-uY6hgLZ9.css":         {Data: []byte(":root{--x:1}")},
	})
	return r
}

func doGet(t *testing.T, r *gin.Engine, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func TestSPAFallback_AssetContentTypes(t *testing.T) {
	t.Parallel()
	r := newSPARouter(t)

	tests := []struct {
		name      string
		path      string
		wantCType string
	}{
		// .woff2 нет во встроенной таблице Go, а в alpine-образе Web нет
		// /etc/mime.types — без явного заголовка тип определялся бы сниффером.
		{"woff2", "/assets/inter-latin-Dx4kXJAl.woff2", "font/woff2"},
		{"sourcemap", "/assets/index-Dy_Q_gNU.js.map", "application/json"},
		{"js", "/assets/index-Dy_Q_gNU.js", "text/javascript"},
		{"css", "/assets/index-uY6hgLZ9.css", "text/css"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := doGet(t, r, tt.path)
			require.Equal(t, http.StatusOK, w.Code)
			assert.Contains(t, w.Header().Get("Content-Type"), tt.wantCType)
			assert.Equal(t, assetsCacheControl, w.Header().Get("Cache-Control"))
		})
	}
}

// Регресс §89.1: запрос на удалённый хешированный ассет проваливался в NoRoute и
// получал index.html с кодом 200 и типом text/html. Браузер отказывался
// исполнять такой «скрипт» из-за nosniff и писал в консоль «Refused to execute
// script» — симптом, по которому причину не найти. С immutable-кешем соседних
// файлов цена ошибки выросла, поэтому здесь же обязателен no-store.
func TestSPAFallback_MissingAssetIs404(t *testing.T) {
	t.Parallel()
	w := doGet(t, newSPARouter(t), "/assets/index-OLDHASH.js")

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.NotContains(t, w.Header().Get("Content-Type"), "text/html")
	assert.NotContains(t, w.Body.String(), "<html")
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
}

func TestSPAFallback_IndexAndAPI(t *testing.T) {
	t.Parallel()
	r := newSPARouter(t)

	for _, p := range []string{"/", "/nodes/n1", "/settings/teams"} {
		w := doGet(t, r, p)
		assert.Equal(t, http.StatusOK, w.Code, p)
		assert.Contains(t, w.Header().Get("Content-Type"), "text/html", p)
		assert.Contains(t, w.Body.String(), "nexus", p)
		// index.html не должен кешироваться: он ссылается на хешированные
		// ассеты, и устаревшая копия указывала бы на уже удалённые файлы.
		assert.Equal(t, "no-cache", w.Header().Get("Cache-Control"), p)
	}

	// API и инфраструктурные пути в SPA не проваливаются.
	w := doGet(t, r, "/api/nope")
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), "not found")
	assert.NotContains(t, w.Body.String(), "<html")
}
