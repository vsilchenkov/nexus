// Package i18n — лёгкая локализация серверных тексто-ответов API (§7.11 ТЗ).
//
// Логи в ClickHouse, stderr и Prometheus метрики всегда на английском —
// здесь только тексты ошибок и сообщений, которые рендерятся в UI.
//
// Текущие языки: en (default), ru. Расширение — добавление нового
// мап-литерала в translations и записи в parseLang.
package i18n

import (
	"context"
	"net/http"
	"strings"
)

// Lang — допустимое значение языка.
type Lang string

const (
	LangEN Lang = "en"
	LangRU Lang = "ru"
)

// DefaultLang — что подставляется, если Accept-Language не распознан.
const DefaultLang = LangEN

// ctxKey — приватный ключ, чтобы не пересекаться с другими ctx.Value.
type ctxKey struct{}

// WithLang кладёт язык в контекст.
func WithLang(ctx context.Context, l Lang) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// FromContext возвращает язык из контекста или DefaultLang.
func FromContext(ctx context.Context) Lang {
	if v, ok := ctx.Value(ctxKey{}).(Lang); ok {
		return v
	}
	return DefaultLang
}

// ParseAcceptLanguage — простой парсер заголовка вида "ru,en;q=0.9".
// Возвращает первый распознанный язык (ru или en); если ничего не
// распознано — DefaultLang.
func ParseAcceptLanguage(header string) Lang {
	for _, part := range strings.Split(header, ",") {
		p := strings.TrimSpace(part)
		// убираем q-параметр
		if i := strings.IndexByte(p, ';'); i >= 0 {
			p = p[:i]
		}
		p = strings.ToLower(p)
		switch {
		case p == "ru" || strings.HasPrefix(p, "ru-"):
			return LangRU
		case p == "en" || strings.HasPrefix(p, "en-"):
			return LangEN
		}
	}
	return DefaultLang
}

// FromHTTP — удобный helper для middleware: парсит Accept-Language
// заголовка запроса.
func FromHTTP(r *http.Request) Lang {
	return ParseAcceptLanguage(r.Header.Get("Accept-Language"))
}

// Translate возвращает локализованную строку по ключу. Если ключ не
// найден в выбранном языке — fallback на английский. Если и там не
// найден — возвращает сам ключ (видимо в UI, чтобы переводчик
// заметил пропуск).
func Translate(lang Lang, key string) string {
	if m, ok := translations[lang]; ok {
		if s, ok := m[key]; ok {
			return s
		}
	}
	if s, ok := translations[LangEN][key]; ok {
		return s
	}
	return key
}

// T — короткий хелпер, вытаскивает язык из контекста и переводит.
func T(ctx context.Context, key string) string {
	return Translate(FromContext(ctx), key)
}

// translations — централизованный словарь. Структурирован по ключам
// "namespace.subkey". Backend-стороне нужно немного: ошибки auth,
// node, replay, ratelimit, generic.
var translations = map[Lang]map[string]string{
	LangEN: {
		// generic
		"error.internal":     "internal error",
		"error.bad_request":  "bad request",
		"error.unauthorized": "unauthorized",
		"error.forbidden":    "forbidden",
		"error.not_found":    "not found",
		"error.conflict":     "conflict",
		"error.rate_limited": "rate limit exceeded",
		// auth
		"auth.invalid_credentials": "invalid credentials",
		"auth.user_inactive":       "user is inactive",
		"auth.session_expired":     "session expired",
		// node
		"node.not_found":      "node not found",
		"node.already_exists": "node with this path already exists",
		"node.disabled":       "node is disabled; enable it before replay",
		"node.limit_reached":  "node limit reached, contact administrator",
		"node.paused":         "node is paused",
		// replay
		"replay.too_old": "cannot replay failed request older than 7 days",
		// url
		"url.required":    "url parameter is required",
		"url.invalid":     "target url is invalid",
		"url.not_allowed": "target url not in allowlist",
	},
	LangRU: {
		"error.internal":           "внутренняя ошибка",
		"error.bad_request":        "некорректный запрос",
		"error.unauthorized":       "не авторизован",
		"error.forbidden":          "доступ запрещён",
		"error.not_found":          "не найдено",
		"error.conflict":           "конфликт",
		"error.rate_limited":       "превышен лимит запросов",
		"auth.invalid_credentials": "неверный логин или пароль",
		"auth.user_inactive":       "пользователь отключён",
		"auth.session_expired":     "сессия истекла",
		"node.not_found":           "узел не найден",
		"node.already_exists":      "узел с таким путём уже существует",
		"node.disabled":            "узел отключён; включите его перед replay",
		"node.limit_reached":       "достигнут лимит узлов, обратитесь к администратору",
		"node.paused":              "узел в паузе",
		"replay.too_old":           "нельзя повторить запрос с ошибкой старше 7 дней",
		"url.required":             "параметр URL обязателен",
		"url.invalid":              "целевой URL невалиден",
		"url.not_allowed":          "целевой URL не входит в allowlist",
	},
}
