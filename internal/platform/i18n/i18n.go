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
	for part := range strings.SplitSeq(header, ",") {
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
		// allowed hosts catalog (§23)
		"host.not_found":          "host pattern not found",
		"host.already_exists":     "a host pattern with this value already exists",
		"host.in_use":             "the host pattern is used by nodes and cannot be modified or deleted",
		"host.invalid_kind":       "kind must be exact, wildcard or regex",
		"host.pattern_length":     "pattern length must be between 1 and 512 characters",
		"host.exact_format":       "exact host must be a valid hostname (no scheme, port or path)",
		"host.wildcard_format":    "wildcard host must look like *.example.com",
		"host.regex_invalid":      "regex does not compile",
		"host.description_length": "description must be at most 500 characters",
		// ch-templates (§19)
		"ch_template.not_found":                 "clickhouse template not found",
		"ch_template.already_exists":            "a template with this name already exists",
		"ch_template.in_use":                    "the template is used by nodes and cannot be deleted",
		"ch_template.default_immutable":         "the default template cannot be deleted",
		"ch_template.unavailable":               "clickhouse is unavailable",
		"ch_template.name_format":               "name may contain only latin letters, digits, space, hyphen and underscore, must start with a letter or digit and be at most 64 characters",
		"ch_template.description_length":        "description must be at most 1000 characters",
		"ch_template.invalid_engine":            "engine must be MergeTree",
		"ch_template.invalid_partition":         "partitioning expression is not in the allowed set",
		"ch_template.empty_order_by":            "order by must list at least one column",
		"ch_template.invalid_order_by":          "order by may reference only required log columns",
		"ch_template.unknown_column":            "a codec override references an unknown column",
		"ch_template.invalid_codec":             "a column codec is invalid",
		"ch_template.invalid_index_name":        "index name may contain only latin letters, digits and underscore",
		"ch_template.invalid_index_expr":        "index expression must reference a required log column",
		"ch_template.invalid_index_type":        "index type is invalid",
		"ch_template.invalid_index_granularity": "index granularity must be between 1 and 1000000",
		"ch_template.invalid_ttl_mode":          "retention mode must be \"none\" or \"ttl_days\"",
		"ch_template.ttl_days_required":         "retention days must be greater than 0 when TTL is enabled",
		"ch_template.invalid_table_name":        "table name must be in db.table format",
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
		// allowed hosts catalog (§23)
		"host.not_found":          "паттерн хоста не найден",
		"host.already_exists":     "паттерн хоста с таким значением уже существует",
		"host.in_use":             "паттерн используется узлами — изменить или удалить нельзя",
		"host.invalid_kind":       "тип должен быть exact, wildcard или regex",
		"host.pattern_length":     "длина паттерна должна быть от 1 до 512 символов",
		"host.exact_format":       "точный хост должен быть валидным hostname (без схемы, порта и пути)",
		"host.wildcard_format":    "wildcard-хост должен иметь вид *.example.com",
		"host.regex_invalid":      "регулярное выражение не компилируется",
		"host.description_length": "описание должно быть не длиннее 500 символов",
		// ch-templates (§19)
		"ch_template.not_found":                 "шаблон ClickHouse не найден",
		"ch_template.already_exists":            "шаблон с таким названием уже существует",
		"ch_template.in_use":                    "шаблон используется узлами, удалить нельзя",
		"ch_template.default_immutable":         "шаблон по умолчанию нельзя удалить",
		"ch_template.unavailable":               "ClickHouse недоступен",
		"ch_template.name_format":               "название может содержать только латинские буквы, цифры, пробел, дефис и подчёркивание, начинаться с буквы или цифры и быть не длиннее 64 символов",
		"ch_template.description_length":        "описание не может быть длиннее 1000 символов",
		"ch_template.invalid_engine":            "движок должен быть MergeTree",
		"ch_template.invalid_partition":         "выражение партиционирования не входит в список разрешённых",
		"ch_template.empty_order_by":            "сортировка должна содержать хотя бы одну колонку",
		"ch_template.invalid_order_by":          "сортировка может ссылаться только на обязательные колонки лога",
		"ch_template.unknown_column":            "переопределение CODEC ссылается на несуществующую колонку",
		"ch_template.invalid_codec":             "CODEC колонки некорректен",
		"ch_template.invalid_index_name":        "имя индекса может содержать только латинские буквы, цифры и подчёркивание",
		"ch_template.invalid_index_expr":        "выражение индекса должно ссылаться на обязательную колонку лога",
		"ch_template.invalid_index_type":        "тип индекса некорректен",
		"ch_template.invalid_index_granularity": "гранулярность индекса должна быть от 1 до 1000000",
		"ch_template.invalid_ttl_mode":          "режим хранения должен быть «none» или «ttl_days»",
		"ch_template.ttl_days_required":         "при включённом TTL число дней хранения должно быть больше 0",
		"ch_template.invalid_table_name":        "имя таблицы должно быть в формате db.table",
	},
}
