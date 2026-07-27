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
		"error.internal":         "internal error",
		"error.bad_request":      "bad request",
		"error.unauthorized":     "unauthorized",
		"error.forbidden":        "forbidden",
		"error.not_found":        "not found",
		"error.conflict":         "conflict",
		"error.rate_limited":     "rate limit exceeded",
		"error.logs_unavailable": "logs backend (ClickHouse) is temporarily unavailable",
		// §48: синтаксис мини-языка поиска / RE2
		"error.bad_search_query": "invalid search query (check syntax or regular expression)",
		// auth
		"auth.invalid_credentials": "invalid credentials",
		"auth.user_inactive":       "user is inactive",
		"auth.session_expired":     "session expired",
		"auth.rate_limited":        "too many login attempts, try again later",
		// node
		"node.not_found":      "node not found",
		"node.already_exists": "node with this path already exists",
		"node.disabled":       "node is disabled; enable it before replay",
		"node.limit_reached":  "node limit reached, contact administrator",
		"node.paused":         "node is paused",
		// RabbitMQAsync (§27)
		"rmq.test_rate_limited": "too many RabbitMQ connection tests, try again in a minute",
		// Kafka monitoring (§4 spec)
		"kafka.rate_limited":    "too many Kafka monitoring requests, try again in a minute",
		"kafka.period_too_long": "custom period must not exceed 90 days",
		// replay
		"replay.too_old":               "cannot replay failed request older than 7 days",
		"replay.body_unavailable":      "original request body was not logged for this node — enter the body manually to replay",
		"replay.multipart_unavailable": "original request body was multipart/form-data and is not stored — enter the body manually to replay",
		"replay.bad_params":            "parameters must be a valid query string (a=1&b=2)",
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
		// headers catalog (§24)
		"header.not_found":          "header not found",
		"header.already_exists":     "a header with this name already exists",
		"header.in_use":             "the header is used by nodes and cannot be renamed or deleted",
		"header.name_length":        "header name length must be between 1 and 100 characters",
		"header.name_format":        "header name must be a valid RFC 7230 token (no spaces)",
		"header.description_length": "description must be at most 500 characters",
		// request fields catalog (§41)
		"request_field.not_found":          "request field not found",
		"request_field.already_exists":     "a request field with this name already exists",
		"request_field.name_length":        "request field name length must be between 1 and 64 characters",
		"request_field.name_format":        "request field name must start with a letter and contain only letters, digits, hyphen and underscore",
		"request_field.description_length": "description must be at most 500 characters",
		// ch-templates (§19)
		"ch_template.not_found":                       "clickhouse template not found",
		"ch_template.already_exists":                  "a template with this name already exists",
		"ch_template.in_use":                          "the template is used by nodes and cannot be deleted",
		"ch_template.default_immutable":               "the default template cannot be deleted",
		"ch_template.unavailable":                     "clickhouse is unavailable",
		"ch_sync.external_table_forbidden":            "schema sync is not available: the node uses an external table managed outside Nexus",
		"ch_template.name_format":                     "name may contain only latin letters, digits, space, hyphen and underscore, must start with a letter or digit and be at most 64 characters",
		"ch_template.description_length":              "description must be at most 1000 characters",
		"ch_template.invalid_engine":                  "engine must be MergeTree",
		"ch_template.invalid_partition":               "partitioning expression is not in the allowed set",
		"ch_template.empty_order_by":                  "order by must list at least one column",
		"ch_template.invalid_order_by":                "order by may reference only required log columns",
		"ch_template.unknown_column":                  "a codec override references an unknown column",
		"ch_template.invalid_codec":                   "a column codec is invalid",
		"ch_template.invalid_index_name":              "index name may contain only latin letters, digits and underscore",
		"ch_template.invalid_index_expr":              "index expression must reference a required log column",
		"ch_template.invalid_index_type":              "index type is invalid",
		"ch_template.invalid_index_granularity":       "index granularity must be between 1 and 1000000",
		"ch_template.invalid_ttl_mode":                "retention mode must be \"none\" or \"ttl_days\"",
		"ch_template.ttl_days_required":               "retention days must be greater than 0 when TTL is enabled",
		"ch_template.invalid_table_name":              "table name must be in db.table format",
		"node.validation.root_method":                 "invalid node type",
		"node.validation.incoming_method":             "invalid incoming method (allowed: GET, POST, PUT, DELETE)",
		"node.validation.outgoing_method":             "invalid outgoing method (allowed: GET, POST, PUT, DELETE)",
		"node.validation.url_mode":                    "invalid URL mode",
		"node.validation.auth_type":                   "invalid outgoing auth type",
		"node.validation.incoming_auth_type":          "invalid incoming auth type",
		"node.validation.status":                      "invalid status",
		"node.validation.path_length":                 "path length must be between 1 and 255 characters",
		"node.validation.path_format":                 "path may contain only latin letters, digits, slash, hyphen and underscore, and must start with a letter or digit",
		"node.validation.target_url_required":         "target URL is required when URL mode is static",
		"node.validation.target_url_length":           "target URL must be at most 2048 characters",
		"node.validation.target_url_scheme":           "target URL must start with http:// or https:// and contain a host",
		"node.validation.target_url_self":             "target URL must not point at the Nexus ingress itself (would cause a request loop)",
		"node.validation.url_param_name_length":       "URL parameter name length must be between 1 and 64 characters",
		"node.validation.url_param_name_format":       "URL parameter name must start with a letter and contain only letters, digits, hyphen and underscore",
		"node.validation.auth_dynamic_field_length":   "dynamic auth field name length must be between 1 and 64 characters",
		"node.validation.auth_dynamic_field_format":   "dynamic auth field name must start with a letter and contain only letters, digits, hyphen and underscore",
		"node.validation.auth_dynamic_source":         "dynamic auth source must be header or query",
		"node.validation.timeout_ms":                  "timeout must be between 100 and 600000 ms",
		"node.validation.retry_count":                 "retry count must be between 0 and 10",
		"node.validation.retry_backoff_ms":            "retry backoff must be between 0 and 60000 ms",
		"node.validation.allowed_hosts_size":          "allowed hosts list must have at most 50 entries",
		"node.validation.forward_headers_size":        "forward headers list must have at most 30 entries",
		"node.validation.webhook_sig_header_length":   "signature header name must be at most 128 characters",
		"node.validation.webhook_sig_prefix_length":   "signature prefix must be at most 64 characters",
		"node.validation.webhook_sig_header_required": "signature header is required for webhook_signature auth",
		"node.validation.webhook_sig_secret_required": "secret is required for webhook_signature auth",
		"node.validation.template_id":                 "ClickHouse template id must be a valid UUID",
		"node.validation.external_table_conflict":     "an external table cannot be combined with a ClickHouse table template — the template means Nexus manages the table",
		"node.validation.logs_not_configured":         "logging is enabled but no ClickHouse table name is set — pick a template or set the table name",
		"node.validation.clickhouse_table_format":     "ClickHouse table name must be db.table using only letters, digits and underscore (e.g. nexus_default.my_table)",
		"node.validation.max_body_size_range":         "max body size must be between 0 and 10000000 bytes",
		"node.validation.max_body_size_required":      "max body size must be greater than 0 when the limit is enabled",
		"node.validation.rmq_host":                    "RabbitMQ host is required (1..253 characters)",
		"node.validation.rmq_queue":                   "RabbitMQ queue name must be 1..255 characters and contain only letters, digits, dot, hyphen and underscore",
		"node.validation.pull_interval_sec":           "pull interval must be between 1 and 3600 seconds",
		"node.validation.pull_batch_size":             "pull batch size must be between 1 and 1000",
		"node.validation.pull_prefetch":               "pull prefetch must be between 1 and 1000",
	},
	LangRU: {
		"error.internal":               "внутренняя ошибка",
		"error.bad_request":            "некорректный запрос",
		"error.unauthorized":           "не авторизован",
		"error.forbidden":              "доступ запрещён",
		"error.not_found":              "не найдено",
		"error.conflict":               "конфликт",
		"error.rate_limited":           "превышен лимит запросов",
		"error.logs_unavailable":       "backend логов (ClickHouse) временно недоступен",
		"error.bad_search_query":       "некорректный поисковый запрос (проверьте синтаксис или регулярное выражение)",
		"auth.invalid_credentials":     "неверный логин или пароль",
		"auth.user_inactive":           "пользователь отключён",
		"auth.session_expired":         "сессия истекла",
		"auth.rate_limited":            "слишком много попыток входа, попробуйте позже",
		"node.not_found":               "узел не найден",
		"node.already_exists":          "узел с таким путём уже существует",
		"node.disabled":                "узел отключён; включите его перед replay",
		"node.limit_reached":           "достигнут лимит узлов, обратитесь к администратору",
		"node.paused":                  "узел в паузе",
		"rmq.test_rate_limited":        "слишком много проверок подключения к RabbitMQ, повторите через минуту",
		"kafka.rate_limited":           "слишком много запросов мониторинга Kafka, повторите через минуту",
		"kafka.period_too_long":        "произвольный период не может превышать 90 дней",
		"replay.too_old":               "нельзя повторить запрос с ошибкой старше 7 дней",
		"replay.body_unavailable":      "тело исходного запроса не сохранялось для этого узла — введите тело вручную, чтобы повторить",
		"replay.multipart_unavailable": "тело исходного запроса было multipart/form-data и не сохранялось — введите тело вручную, чтобы повторить",
		"replay.bad_params":            "параметры должны быть валидной query-строкой (a=1&b=2)",
		"url.required":                 "параметр URL обязателен",
		"url.invalid":                  "целевой URL невалиден",
		"url.not_allowed":              "целевой URL не входит в allowlist",
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
		// headers catalog (§24)
		"header.not_found":          "заголовок не найден",
		"header.already_exists":     "заголовок с таким именем уже существует",
		"header.in_use":             "заголовок используется узлами — переименовать или удалить нельзя",
		"header.name_length":        "длина имени заголовка должна быть от 1 до 100 символов",
		"header.name_format":        "имя заголовка должно быть валидным RFC 7230 token (без пробелов)",
		"header.description_length": "описание должно быть не длиннее 500 символов",
		// request fields catalog (§41)
		"request_field.not_found":          "поле запроса не найдено",
		"request_field.already_exists":     "поле запроса с таким именем уже существует",
		"request_field.name_length":        "длина имени поля запроса должна быть от 1 до 64 символов",
		"request_field.name_format":        "имя поля запроса должно начинаться с буквы и содержать только буквы, цифры, дефис и подчёркивание",
		"request_field.description_length": "описание должно быть не длиннее 500 символов",
		// ch-templates (§19)
		"ch_template.not_found":                       "шаблон ClickHouse не найден",
		"ch_template.already_exists":                  "шаблон с таким названием уже существует",
		"ch_template.in_use":                          "шаблон используется узлами, удалить нельзя",
		"ch_template.default_immutable":               "шаблон по умолчанию нельзя удалить",
		"ch_template.unavailable":                     "ClickHouse недоступен",
		"ch_sync.external_table_forbidden":            "синхронизация схемы недоступна: узел использует внешнюю таблицу, которой Nexus не управляет",
		"ch_template.name_format":                     "название может содержать только латинские буквы, цифры, пробел, дефис и подчёркивание, начинаться с буквы или цифры и быть не длиннее 64 символов",
		"ch_template.description_length":              "описание не может быть длиннее 1000 символов",
		"ch_template.invalid_engine":                  "движок должен быть MergeTree",
		"ch_template.invalid_partition":               "выражение партиционирования не входит в список разрешённых",
		"ch_template.empty_order_by":                  "сортировка должна содержать хотя бы одну колонку",
		"ch_template.invalid_order_by":                "сортировка может ссылаться только на обязательные колонки лога",
		"ch_template.unknown_column":                  "переопределение CODEC ссылается на несуществующую колонку",
		"ch_template.invalid_codec":                   "CODEC колонки некорректен",
		"ch_template.invalid_index_name":              "имя индекса может содержать только латинские буквы, цифры и подчёркивание",
		"ch_template.invalid_index_expr":              "выражение индекса должно ссылаться на обязательную колонку лога",
		"ch_template.invalid_index_type":              "тип индекса некорректен",
		"ch_template.invalid_index_granularity":       "гранулярность индекса должна быть от 1 до 1000000",
		"ch_template.invalid_ttl_mode":                "режим хранения должен быть «none» или «ttl_days»",
		"ch_template.ttl_days_required":               "при включённом TTL число дней хранения должно быть больше 0",
		"ch_template.invalid_table_name":              "имя таблицы должно быть в формате db.table",
		"node.validation.root_method":                 "недопустимый тип узла",
		"node.validation.incoming_method":             "недопустимый входящий метод (разрешены: GET, POST, PUT, DELETE)",
		"node.validation.outgoing_method":             "недопустимый исходящий метод (разрешены: GET, POST, PUT, DELETE)",
		"node.validation.url_mode":                    "недопустимый режим URL",
		"node.validation.auth_type":                   "недопустимый тип исходящей авторизации",
		"node.validation.incoming_auth_type":          "недопустимый тип входящей авторизации",
		"node.validation.status":                      "недопустимый статус",
		"node.validation.path_length":                 "длина пути должна быть от 1 до 255 символов",
		"node.validation.path_format":                 "путь может содержать только латинские буквы, цифры, слеш, дефис и подчёркивание и должен начинаться с буквы или цифры",
		"node.validation.target_url_required":         "целевой URL обязателен при режиме URL «static»",
		"node.validation.target_url_length":           "целевой URL не длиннее 2048 символов",
		"node.validation.target_url_scheme":           "целевой URL должен начинаться с http:// или https:// и содержать хост",
		"node.validation.target_url_self":             "целевой URL не должен указывать на сам вход Nexus (это вызовет зацикливание запросов)",
		"node.validation.url_param_name_length":       "имя URL-параметра должно быть от 1 до 64 символов",
		"node.validation.url_param_name_format":       "имя URL-параметра должно начинаться с буквы и содержать только буквы, цифры, дефис и подчёркивание",
		"node.validation.auth_dynamic_field_length":   "имя поля динамической авторизации должно быть от 1 до 64 символов",
		"node.validation.auth_dynamic_field_format":   "имя поля динамической авторизации должно начинаться с буквы и содержать только буквы, цифры, дефис и подчёркивание",
		"node.validation.auth_dynamic_source":         "источник динамической авторизации — header или query",
		"node.validation.timeout_ms":                  "таймаут должен быть от 100 до 600000 мс",
		"node.validation.retry_count":                 "число повторов должно быть от 0 до 10",
		"node.validation.retry_backoff_ms":            "пауза между повторами должна быть от 0 до 60000 мс",
		"node.validation.allowed_hosts_size":          "список разрешённых хостов — не более 50 элементов",
		"node.validation.forward_headers_size":        "список пробрасываемых заголовков — не более 30 элементов",
		"node.validation.webhook_sig_header_length":   "имя заголовка подписи — не длиннее 128 символов",
		"node.validation.webhook_sig_prefix_length":   "префикс подписи — не длиннее 64 символов",
		"node.validation.webhook_sig_header_required": "для авторизации webhook_signature требуется заголовок подписи",
		"node.validation.webhook_sig_secret_required": "для авторизации webhook_signature требуется секрет",
		"node.validation.template_id":                 "идентификатор шаблона ClickHouse должен быть корректным UUID",
		"node.validation.external_table_conflict":     "внешняя таблица несовместима с шаблоном CH-таблицы — шаблон означает, что таблицей управляет Nexus",
		"node.validation.logs_not_configured":         "логирование включено, но не задано имя таблицы ClickHouse — выберите шаблон или укажите имя таблицы",
		"node.validation.clickhouse_table_format":     "имя таблицы ClickHouse должно быть в формате db.table из латинских букв, цифр и подчёркивания (напр. nexus_default.my_table)",
		"node.validation.max_body_size_range":         "максимальный размер тела должен быть от 0 до 10000000 байт",
		"node.validation.max_body_size_required":      "максимальный размер тела должен быть больше 0, когда лимит включён",
		"node.validation.rmq_host":                    "хост RabbitMQ обязателен (1..253 символа)",
		"node.validation.rmq_queue":                   "имя очереди RabbitMQ — 1..255 символов, только буквы, цифры, точка, дефис и подчёркивание",
		"node.validation.pull_interval_sec":           "интервал опроса должен быть от 1 до 3600 секунд",
		"node.validation.pull_batch_size":             "размер батча должен быть от 1 до 1000",
		"node.validation.pull_prefetch":               "prefetch должен быть от 1 до 1000",
	},
}
