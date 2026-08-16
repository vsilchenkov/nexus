package http

import (
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/gin-gonic/gin"
)

// assetsCacheControl — кеш статики SPA (§89.1). Имена ассетов хешированы по
// содержимому (Vite: `<name>-<hash>.<ext>`), поэтому «протухшего» ответа под тем
// же адресом не бывает и `immutable` безопасен.
//
// До §89.1 заголовка не было вовсе, и браузер перекачивал весь бандл при каждом
// полном заходе: embed.FS отдаёт нулевой ModTime, из-за чего http.ServeContent
// не ставит Last-Modified, ETag не ставит никто, а без валидатора эвристическое
// кеширование (RFC 9111) неприменимо. С восемью шрифтовыми файлами (§89.1)
// цена выросла ещё.
//
// Инвариант: в assets/ не должно появляться НЕхешированных имён — иначе такой
// файл залипнет у пользователей на год. Проверка для чек-листа сборки:
// `ls internal/web/static/assets | grep -v -- '-[A-Za-z0-9_-]\{8\}\.'` пуст.
const assetsCacheControl = "public, max-age=31536000, immutable"

// assetContentType — MIME для расширений, которых нет во встроенной таблице Go
// (mime/type.go: .woff2/.woff/.ttf там отсутствуют) и которые не подтянутся из
// системной базы: базовый образ Web — alpine (deploy/docker/web.Dockerfile), а
// /etc/mime.types приходит с пакетом mailcap, которого в образе нет.
//
// Чистая функция, а не mime.AddExtensionType: тот пишет в глобальный реестр
// процесса из бутстрапа — состояние, которого в проекте нет (CLAUDE.md §4).
// Пустая строка = «решай сам»: http.ServeContent возьмёт свою таблицу либо
// определит тип по сигнатуре содержимого.
func assetContentType(ext string) string {
	switch strings.ToLower(ext) {
	case ".woff2":
		return "font/woff2"
	// .woff/.ttf сегодня в бандл не попадают (§89.1 везёт только woff2) — ветки
	// защитные: у Go их нет ровно по той же причине, поэтому добавленный когда-то
	// фолбэк для старых браузеров напоролся бы на этот же сниффинг молча.
	case ".woff":
		return "font/woff"
	case ".ttf":
		return "font/ttf"
	case ".map":
		// Sourcemap: расширения нет ни в одной таблице, сниффер отдаёт text/plain.
		return "application/json"
	}
	return ""
}

// staticAssetHeaders — заголовки статики SPA (/assets/*). Ставятся ДО
// обработчика: http.FileServer пишет тело сразу, и после него заголовок менять
// уже поздно. ServeContent уважает выставленный Content-Type (net/http/fs.go,
// ветка haveType) и тогда не читает первые 512 байт файла ради сниффинга.
func staticAssetHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", assetsCacheControl)
		if ct := assetContentType(path.Ext(c.Request.URL.Path)); ct != "" {
			c.Header("Content-Type", ct)
		}
		c.Next()
	}
}

// SPAFallback регистрирует handler для статики SPA + fallback на index.html
// для всех путей, не начинающихся с /api/, /swagger/, /metrics, /health, /ready.
//
// Использует embed.FS из internal/web/static.
func SPAFallback(r *gin.Engine, embedFS fs.FS) {
	indexBytes, err := fs.ReadFile(embedFS, "index.html")
	if err != nil {
		// В режиме без embedded UI просто отдаём короткое сообщение.
		indexBytes = []byte("<html><body>Nexus UI not embedded</body></html>")
	}

	// Статика SPA (/assets/*) — отдаём напрямую из embed.FS с корректными
	// MIME-типами (через http.FileServer). Иначе .js/.css проваливались бы
	// в NoRoute и возвращали index.html с text/html → белый экран.
	//
	// Группа с middleware вместо прежнего r.StaticFS("/assets", …): маршрут
	// получается тот же ("/assets/*filepath") и StripPrefix тот же ("/assets" —
	// gin отдаёт basePath группы как есть при пустом relativePath), но заголовки
	// успевают лечь до FileServer'а.
	if assetsFS, subErr := fs.Sub(embedFS, "assets"); subErr == nil {
		r.Group("/assets", staticAssetHeaders()).StaticFS("", http.FS(assetsFS))
	}

	// NoRoute уже определён в Handler.Register; перепишем его — теперь
	// fallback не на 404, а на index.html (с теми же исключениями).
	r.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		if isAPIOrInfra(p) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		// Несуществующий ассет — это 404, а не SPA. gin при отсутствии файла
		// делегирует управление сюда (createStaticHandler), и раньше запрос на
		// удалённый /assets/index-OLD.js получал index.html с кодом 200: браузер
		// видел text/html по адресу .js, nosniff отказывался исполнять, а в
		// консоли было невнятное «Refused to execute script» вместо честного
		// 404. С immutable-кешем (см. assetsCacheControl) цена такой ошибки
		// выросла на порядок, поэтому здесь же снимаем заголовок кеша,
		// выставленный группой /assets.
		if strings.HasPrefix(p, "/assets/") {
			c.Header("Cache-Control", "no-store")
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		serveIndex(c, indexBytes)
	})

	// Прямой / тоже отдаёт SPA index.
	r.GET("/", func(c *gin.Context) { serveIndex(c, indexBytes) })
}

// serveIndex отдаёт оболочку SPA. no-cache (а не no-store): браузер и так шёл
// за index.html каждый раз, но заголовок закрывает промежуточные прокси — без
// этого закешированный index.html ссылался бы на хешированные ассеты, которых
// уже нет, и immutable-кеш соседних файлов сделал бы починку неочевидной.
func serveIndex(c *gin.Context, indexBytes []byte) {
	c.Header("Cache-Control", "no-cache")
	c.Data(http.StatusOK, "text/html; charset=utf-8", indexBytes)
}

func isAPIOrInfra(p string) bool {
	switch {
	case strings.HasPrefix(p, "/api/"),
		strings.HasPrefix(p, "/swagger/"),
		p == "/metrics", p == "/health", p == "/ready":
		return true
	}
	return false
}
