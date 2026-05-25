package i18n

import "github.com/gin-gonic/gin"

// GinMiddleware — парсит Accept-Language и кладёт Lang в request context
// (и в gin context под ключом ctxKeyLang). Handlers могут получить язык
// через i18n.FromContext(c.Request.Context()).
func GinMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		lang := FromHTTP(c.Request)
		c.Set(string(ctxKeyLangValue), string(lang))
		c.Request = c.Request.WithContext(WithLang(c.Request.Context(), lang))
		c.Next()
	}
}

// ctxKeyLangValue — gin Context.Set ключ. Используется параллельно с
// request-context для удобства handlers, которые работают с *gin.Context.
const ctxKeyLangValue = "i18n.lang"

// FromGin — короткий helper: язык из *gin.Context.
func FromGin(c *gin.Context) Lang {
	if v, ok := c.Get(ctxKeyLangValue); ok {
		if s, ok := v.(string); ok {
			return Lang(s)
		}
	}
	return FromContext(c.Request.Context())
}
