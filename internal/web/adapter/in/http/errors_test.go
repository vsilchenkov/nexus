package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/i18n"
)

func TestLocalizedError_DefaultLang(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/", func(c *gin.Context) {
		localizedError(c, http.StatusNotFound, "node.not_found")
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	require.Equal(t, http.StatusNotFound, w.Code)

	var body map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	// Должен быть переведённый текст (не сам ключ),
	// т.к. node.not_found определён в EN-словаре.
	assert.NotEmpty(t, body["error"])
}

func TestLocalizedError_UsesGinLang(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(i18n.GinMiddleware())
	r.GET("/", func(c *gin.Context) {
		localizedError(c, http.StatusNotFound, "node.not_found")
	})

	// Сначала EN.
	req1 := httptest.NewRequest("GET", "/", nil)
	req1.Header.Set("Accept-Language", "en")
	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, req1)

	// Потом RU.
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.Header.Set("Accept-Language", "ru")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)

	var b1, b2 map[string]string
	require.NoError(t, json.Unmarshal(w1.Body.Bytes(), &b1))
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &b2))
	// EN и RU варианты для node.not_found должны различаться.
	assert.NotEqual(t, b1["error"], b2["error"], "expected EN and RU translations to differ")
}

func TestLocalizedError_UnknownKey_FallsBackToKey(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/", func(c *gin.Context) {
		localizedError(c, http.StatusInternalServerError, "definitely.not.a.real.key")
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	require.Equal(t, http.StatusInternalServerError, w.Code)

	var body map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	// По контракту i18n.Translate — отсутствующий ключ возвращается as-is.
	assert.Equal(t, "definitely.not.a.real.key", body["error"])
}
