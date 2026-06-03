package i18n

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestGinMiddleware_SetsLangFromHeader(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(GinMiddleware())

	var seenGin, seenCtx Lang
	r.GET("/", func(c *gin.Context) {
		seenGin = FromGin(c)
		seenCtx = FromContext(c.Request.Context())
		c.Status(http.StatusOK)
	})

	cases := []struct {
		name   string
		header string
		want   Lang
	}{
		{"ru pure", "ru", LangRU},
		{"ru with region", "ru-RU,en;q=0.9", LangRU},
		{"en pure", "en", LangEN},
		{"en US with q", "en-US,en;q=0.9", LangEN},
		{"unknown falls back", "fr,de", DefaultLang},
		{"empty falls back", "", DefaultLang},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/", nil)
			if c.header != "" {
				req.Header.Set("Accept-Language", c.header)
			}
			seenGin, seenCtx = "", ""
			r.ServeHTTP(w, req)
			if seenGin != c.want {
				t.Errorf("FromGin = %q, want %q", seenGin, c.want)
			}
			if seenCtx != c.want {
				t.Errorf("FromContext = %q, want %q", seenCtx, c.want)
			}
		})
	}
}

func TestFromGin_ReadsCtxValue(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/", nil)
	c.Set(ctxKeyLangValue, string(LangRU))

	if got := FromGin(c); got != LangRU {
		t.Errorf("FromGin = %q, want ru", got)
	}
}

func TestFromGin_FallbackToRequestContext(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	req := httptest.NewRequest("GET", "/", nil)
	req = req.WithContext(WithLang(req.Context(), LangRU))
	c.Request = req
	// gin.Set не вызывался — должен взять из request context.

	if got := FromGin(c); got != LangRU {
		t.Errorf("FromGin (no gin.Set) = %q, want ru", got)
	}
}

func TestFromGin_FallbackToDefault(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/", nil)
	// ни gin.Set, ни request context — ожидаем DefaultLang.

	if got := FromGin(c); got != DefaultLang {
		t.Errorf("FromGin (empty) = %q, want %q", got, DefaultLang)
	}
}

func TestFromGin_BadCtxValueFallsBack(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/", nil)
	// Кладём не-string значение под ключом lang.
	c.Set(ctxKeyLangValue, 42)

	if got := FromGin(c); got != DefaultLang {
		t.Errorf("FromGin (bad type) = %q, want %q", got, DefaultLang)
	}
}
