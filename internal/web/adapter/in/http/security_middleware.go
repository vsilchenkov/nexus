package http

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"nexus/internal/platform/logging"
)

// CSRFOriginCheck — защита от CSRF для cookie-аутентифицированных мутаций
// (Phase AUD.4, дополнение к SameSite-cookie). Для POST/PUT/PATCH/DELETE
// сверяет host из заголовка Origin с Host запроса:
//
//   - Origin отсутствует → пропускаем (не-браузерные клиенты: curl, скрипты,
//     integration-тесты — браузер для cross-origin мутаций Origin шлёт всегда);
//   - Authorization: Bearer → пропускаем (API-токены CSRF не подвержены:
//     браузер не приложит такой заголовок автоматически);
//   - host Origin ≠ Host запроса (включая Origin: null из sandboxed-iframe) →
//     403 без выполнения handler'а.
//
// Вешается на группу /api/* (SPA same-origin, поэтому легитимные запросы
// фронта всегда проходят). Reverse-proxy Receiver (/api/v1/*) регистрируется
// отдельно и этим middleware не покрывается — там аутентификация узла своя,
// cookie не используется.
func CSRFOriginCheck(logger logging.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			c.Next()
			return
		}
		if strings.HasPrefix(c.GetHeader("Authorization"), "Bearer ") {
			c.Next()
			return
		}
		origin := c.GetHeader("Origin")
		if origin == "" {
			c.Next()
			return
		}
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" || !strings.EqualFold(u.Host, c.Request.Host) {
			logger.Warn("cross-origin mutating request rejected",
				logger.Str("origin", origin),
				logger.Str("host", c.Request.Host),
				logger.Str("path", c.Request.URL.Path))
			c.AbortWithStatusJSON(http.StatusForbidden,
				gin.H{"error": "cross-origin request rejected"})
			return
		}
		c.Next()
	}
}

// cspPolicy — Content-Security-Policy SPA: всё с собственного origin;
// 'unsafe-inline' только для style (Tailwind/Radix инлайнят style-атрибуты).
//
// Внешних источников в политике нет вовсе: с §89.1 шрифты эталона §21 лежат
// локально (web-ui/src/assets/fonts → бандл → embed.FS), Google Fonts из
// index.html убраны, и панель полностью работоспособна в изолированном контуре.
// font-src без data: — намеренно: инлайн шрифта в base64 запрещён на стороне
// сборки (build.assetsInlineLimit в vite.config.ts), и политика это дублирует.
const cspPolicy = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"font-src 'self'; " +
	"img-src 'self' data:; " +
	"connect-src 'self'; " +
	"frame-ancestors 'none'"

// SecurityHeaders — стандартные защитные заголовки для всех ответов Web
// (Phase AUD.4). CSP не ставится на /swagger/* — Swagger UI использует
// inline-скрипт конфигурации и сломается под script-src 'self'.
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		if !strings.HasPrefix(c.Request.URL.Path, "/swagger/") {
			h.Set("Content-Security-Policy", cspPolicy)
		}
		c.Next()
	}
}
