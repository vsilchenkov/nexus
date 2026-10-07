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
		// §88: восстановление пароля
		"auth.reset_rate_limited":  "too many password reset requests, try again later",
		"auth.reset_token_invalid": "this password reset link is invalid or has already been used",
		"auth.reset_not_allowed":   "password reset is not available for this account",
		// node
		"node.not_found":      "node not found",
		"node.already_exists": "node with this path already exists",
		"node.disabled":       "node is disabled; enable it before replay",
		"node.limit_reached":  "node limit reached, contact administrator",
		"node.paused":         "node is paused",
		// audit revert (§108)
		"audit.revert.entry_not_found": "audit entry not found",
		"audit.revert.not_revertible":  "this audit entry cannot be reverted",
		"audit.revert.field_invalid":   "selected field cannot be reverted",
		"audit.revert.conflict":        "the node has changed since the preview — review the changes again",
		"audit.revert.not_held":        "some values would not be kept by the node settings",
		"audit.revert.node_gone":       "the node has been deleted",
		// team (§78.3, §89.4)
		"team.slug_reserved":        "this slug is reserved by the bus address (request, requestAsync, callback) — pick another one",
		"team.external_url_invalid": "external link must start with http:// or https:// and contain a host, without query string",
		// RabbitMQAsync (§27)
		"rmq.test_rate_limited": "too many RabbitMQ connection tests, try again in a minute",
		// Kafka monitoring (§4 spec)
		"kafka.rate_limited":    "too many Kafka monitoring requests, try again in a minute",
		"kafka.period_too_long": "custom period must not exceed 90 days",
		// replay
		"replay.too_old":               "cannot replay failed request older than 7 days",
		"replay.body_unavailable":      "original request body was not logged for this node — enter the body manually to replay",
		"replay.multipart_unavailable": "original request body was multipart/form-data and is not stored — enter the body manually to replay",
		"replay.original_unavailable":  "the original message is no longer in the queue and the stored log copy is truncated — replay would send a broken body",
		"replay.body_truncated":        "the stored log copy of the request body is truncated — enter the body manually to replay",
		"replay.bad_params":            "parameters must be a valid query string (a=1&b=2)",
		// replay from logs for a period (§85)
		"replay_period.no_window":       "both period bounds are required for a bulk replay",
		"replay_period.sync_node":       "node is synchronous: async ingress is closed, requests cannot be queued",
		"replay_period.body_not_logged": "node does not log request bodies — there is nothing to replay",
		"replay_period.logs_disabled":   "node logging is disabled or has no ClickHouse table",
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
		// node groups (§99)
		"node_group.not_found":           "node group not found",
		"node_group.already_exists":      "a group with this name already exists",
		"node_group.in_use":              "the group is used by nodes and cannot be deleted",
		"node_group.name_length":         "group name length must be between 1 and 100 characters",
		"node_group.description_length":  "description must be at most 500 characters",
		"node_group.sort_order_range":    "sort order must be between 0 and 100000",
		"node_group.invalid_direction":   "move direction must be up or down",
		"node_group.too_many_to_reorder": "too many groups to reorder — reduce the catalog",
		// log mask patterns (§95)
		"log_mask.not_found":           "log mask pattern not found",
		"log_mask.pattern_length":      "pattern length must be between 1 and 500 characters",
		"log_mask.pattern_invalid":     "pattern must be a valid RE2 regular expression",
		"log_mask.replacement_length":  "replacement must be at most 200 characters",
		"log_mask.description_length":  "description must be at most 500 characters",
		"log_mask.sort_order_negative": "order must be a non-negative number",
		// peer instances registry (§73)
		"instance.not_found":          "instance not found",
		"instance.already_exists":     "an instance with this address is already connected",
		"instance.url_invalid":        "address must be an http(s) origin like https://host[:port] — without path, query or credentials",
		"instance.title_length":       "instance name length must be between 1 and 64 characters",
		"instance.comment_length":     "comment must be at most 255 characters",
		"instance.status_invalid":     "invalid instance status",
		"instance.probe_rate_limited": "too many instance checks, try again in a minute",
		// configuration backup (§110)
		"backup.passphrase_required":    "enter the backup file password",
		"backup.passphrase_too_short":   "the backup file password must be at least %d characters long",
		"backup.snapshot_invalid":       "the configuration did not pass the restore check, the file was not created — see the Web service log",
		"backup.file_too_large":         "the backup file is too large",
		"backup.import_request_invalid": "select a backup file and enter its password",
		"backup.target_not_clean":       "restore is possible only on a clean instance: this one already has nodes or teams besides Default",
		"backup.restore_in_progress":    "another restore is already running",
		"backup.clickhouse_unavailable": "ClickHouse is not connected: team databases and log tables cannot be created",
		"backup.options_invalid":        "invalid restore options",
		"backup.schema_newer":           "the file was exported from a newer Nexus version — update this instance first",
		"backup.decrypt_failed":         "wrong password or the file is damaged",
		"backup.not_backup_file":        "this is not a Nexus configuration backup file",
		"backup.unsupported_version":    "unsupported backup file format version",
		"backup.file_invalid":           "the backup file contents did not pass the check",
		"backup.confirm_host_mismatch":  "the entered address does not match this server",
		"backup.digest_mismatch":        "the file, options or restore plan differ from the preview (the server state changed) — check the file again",
		"backup.import_blocked":         "the restore plan has blocking problems — check the file again",
		"backup.clickhouse_failed":      "failed to create ClickHouse databases or tables; PostgreSQL was not changed, the restore can be repeated",
		// logs export (§115)
		"logs_export.busy":                 "other log exports are running on this server — try again in half a minute",
		"logs_export.timeout":              "ClickHouse did not select the records in time — set a period or narrow the search",
		"logs_export.sheet_logs":           "Logs",
		"logs_export.sheet_params":         "Parameters",
		"logs_export.param":                "Parameter",
		"logs_export.value":                "Value",
		"logs_export.cell_truncated":       "…[truncated, %d characters in total]",
		"logs_export.status.complete":      "complete",
		"logs_export.status.limited_rows":  "limited by rows: more records match than were exported",
		"logs_export.status.limited_bytes": "limited by size: the export size limit was reached",
		"logs_export.status.incomplete":    "INCOMPLETE: ClickHouse failed during the export",
		"logs_export.status.aborted":       "aborted",
		"logs_export.reason.timeout":       "query timeout",
		"logs_export.reason.unavailable":   "ClickHouse is unavailable",
		"logs_export.reason.query_failed":  "query failed, see the Web service log",
		"logs_export.bodies_on":            "included, up to %d characters per cell",
		"logs_export.bodies_off":           "not exported",
		"logs_export.unbounded":            "not set",
		"logs_export.yes":                  "yes",
		"logs_export.no":                   "no",
		"logs_export.state.all":            "all",
		"logs_export.state.ok":             "OK",
		"logs_export.state.err":            "errors",
		"logs_export.done.all":             "any",
		"logs_export.done.yes":             "completed (done = 1)",
		"logs_export.done.no":              "not completed (done = 0)",
		"logs_export.p.status":             "Completeness",
		"logs_export.p.written":            "Records exported",
		"logs_export.p.selected":           "Records selected",
		"logs_export.p.more":               "More records match than the row limit",
		"logs_export.p.bytes":              "Cell text, bytes",
		"logs_export.p.bodies":             "Request and response bodies",
		"logs_export.p.truncated_bodies":   "Bodies truncated",
		"logs_export.p.truncated_cells":    "Cells truncated to the Excel limit",
		"logs_export.p.vanished":           "Records deleted during the export",
		"logs_export.p.max_rows":           "Row limit",
		"logs_export.p.max_bytes":          "Size limit, bytes",
		"logs_export.p.node":               "Node",
		"logs_export.p.table":              "ClickHouse table",
		"logs_export.p.from":               "Period from",
		"logs_export.p.to":                 "Period to",
		"logs_export.p.search":             "Search",
		"logs_export.p.state":              "State",
		"logs_export.p.done":               "Delivery run",
		"logs_export.p.method":             "Method",
		"logs_export.p.client":             "Client",
		"logs_export.p.user":               "Exported by",
		"logs_export.p.started":            "Started",
		"logs_export.p.finished":           "Finished",
		"logs_export.p.tz":                 "Time zone of dates",
		"logs_export.p.version":            "Nexus version",
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
		"ch_sync.foreign_database":                    "schema sync is not available: the ClickHouse database belongs to another Nexus instance",
		"queue.foreign_database":                      "clearing is not available: the ClickHouse table belongs to another Nexus instance or an external writer — Nexus only reads it",
		"breaker.unavailable":                         "circuit breaker state is unavailable: Redis is not configured or not responding",
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
		"node.validation.group_id":                    "the selected group does not exist — pick another one",
		"node.validation.external_table_conflict":     "an external table cannot be combined with a ClickHouse table template — the template means Nexus manages the table",
		"node.validation.logs_not_configured":         "logging is enabled but no ClickHouse table name is set — pick a template or set the table name",
		"node.validation.clickhouse_table_format":     "ClickHouse table name must be db.table using only letters, digits and underscore (e.g. nexus_default.my_table)",
		"node.validation.clickhouse_table_foreign":    "this ClickHouse database belongs to another Nexus instance; use a table of this instance or enable the external table mode",
		"node.validation.max_body_size_range":         "max body size must be between 0 and 10000000 bytes",
		"node.validation.max_body_size_required":      "max body size must be greater than 0 when the limit is enabled",
		"node.validation.async_ack_template":          "acknowledgement body template is invalid: check the ${ ... } placeholders and that the result is valid JSON",
		"node.validation.async_ack_spec":              "acknowledgement settings are invalid: allowed content types are application/json and text/plain, and the status code must be 2xx",
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
		"auth.reset_rate_limited":      "слишком много запросов восстановления, попробуйте позже",
		"auth.reset_token_invalid":     "ссылка для смены пароля недействительна или уже использована",
		"auth.reset_not_allowed":       "восстановление пароля для этой учётной записи недоступно",
		"node.not_found":               "узел не найден",
		"node.already_exists":          "узел с таким путём уже существует",
		"node.disabled":                "узел отключён; включите его перед replay",
		"node.limit_reached":           "достигнут лимит узлов, обратитесь к администратору",
		"node.paused":                  "узел в паузе",
		"audit.revert.entry_not_found": "запись журнала не найдена",
		"audit.revert.not_revertible":  "это действие журнала нельзя отменить",
		"audit.revert.field_invalid":   "выбранное поле нельзя отменить",
		"audit.revert.conflict":        "узел изменился после предпросмотра — проверьте изменения ещё раз",
		"audit.revert.not_held":        "часть значений не удержится в настройках узла",
		"audit.revert.node_gone":       "узел удалён",
		"team.slug_reserved":           "этот slug занят адресом шины (request, requestAsync, callback) — выберите другой",
		"team.external_url_invalid":    "внешняя ссылка должна начинаться с http:// или https:// и содержать хост, без параметров запроса",
		"rmq.test_rate_limited":        "слишком много проверок подключения к RabbitMQ, повторите через минуту",
		"kafka.rate_limited":           "слишком много запросов мониторинга Kafka, повторите через минуту",
		"kafka.period_too_long":        "произвольный период не может превышать 90 дней",
		"replay.too_old":               "нельзя повторить запрос с ошибкой старше 7 дней",
		"replay.body_unavailable":      "тело исходного запроса не сохранялось для этого узла — введите тело вручную, чтобы повторить",
		"replay.multipart_unavailable": "тело исходного запроса было multipart/form-data и не сохранялось — введите тело вручную, чтобы повторить",
		"replay.original_unavailable":  "оригинала уже нет в очереди, а копия в журнале обрезана — повтор отправил бы битое тело",
		"replay.body_truncated":        "копия тела в журнале обрезана — введите тело вручную, чтобы повторить",
		"replay.bad_params":            "параметры должны быть валидной query-строкой (a=1&b=2)",
		// повтор из логов за период (§85)
		"replay_period.no_window":       "для массового повтора нужны обе границы периода",
		"replay_period.sync_node":       "узел работает синхронно: асинхронный вход закрыт, поставить запросы в очередь нельзя",
		"replay_period.body_not_logged": "узел не логирует тело запроса — повторять нечего",
		"replay_period.logs_disabled":   "у узла выключено логирование или не задана таблица ClickHouse",
		"url.required":                  "параметр URL обязателен",
		"url.invalid":                   "целевой URL невалиден",
		"url.not_allowed":               "целевой URL не входит в allowlist",
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
		// node groups (§99)
		"node_group.not_found":           "группа не найдена",
		"node_group.already_exists":      "группа с таким именем уже существует",
		"node_group.in_use":              "группа используется узлами — сначала уберите узлы из группы",
		"node_group.name_length":         "длина имени группы должна быть от 1 до 100 символов",
		"node_group.description_length":  "описание должно быть не длиннее 500 символов",
		"node_group.sort_order_range":    "порядок должен быть от 0 до 100000",
		"node_group.invalid_direction":   "направление сдвига должно быть up или down",
		"node_group.too_many_to_reorder": "слишком много групп для перестроения порядка",
		// log mask patterns (§95)
		"log_mask.not_found":           "шаблон маскирования не найден",
		"log_mask.pattern_length":      "длина шаблона должна быть от 1 до 500 символов",
		"log_mask.pattern_invalid":     "шаблон должен быть корректным regex (RE2)",
		"log_mask.replacement_length":  "замена должна быть не длиннее 200 символов",
		"log_mask.description_length":  "описание должно быть не длиннее 500 символов",
		"log_mask.sort_order_negative": "порядок должен быть неотрицательным числом",
		// peer instances registry (§73)
		"instance.not_found":          "инстанс не найден",
		"instance.already_exists":     "инстанс с таким адресом уже подключён",
		"instance.url_invalid":        "адрес должен быть вида https://host[:port] — без пути, параметров и учётных данных",
		"instance.title_length":       "длина названия инстанса должна быть от 1 до 64 символов",
		"instance.comment_length":     "комментарий должен быть не длиннее 255 символов",
		"instance.status_invalid":     "недопустимый статус инстанса",
		"instance.probe_rate_limited": "слишком много проверок инстансов, повторите через минуту",
		// резервная копия конфигурации (§110)
		"backup.passphrase_required":    "укажите пароль файла резервной копии",
		"backup.passphrase_too_short":   "пароль файла резервной копии должен быть не короче %d символов",
		"backup.snapshot_invalid":       "конфигурация не прошла проверку восстановления, файл не создан — подробности в журнале Web Service",
		"backup.file_too_large":         "файл резервной копии слишком большой",
		"backup.import_request_invalid": "выберите файл резервной копии и введите его пароль",
		"backup.target_not_clean":       "восстановление возможно только на чистом экземпляре: здесь уже есть узлы или команды, кроме Default",
		"backup.restore_in_progress":    "восстановление уже выполняется",
		"backup.clickhouse_unavailable": "ClickHouse не подключён: базы команд и таблицы логов создать негде",
		"backup.options_invalid":        "недопустимые параметры восстановления",
		"backup.schema_newer":           "файл выгружен из более новой версии Nexus — сначала обновите этот экземпляр",
		"backup.decrypt_failed":         "неверный пароль или файл повреждён",
		"backup.not_backup_file":        "это не файл резервной копии конфигурации Nexus",
		"backup.unsupported_version":    "неподдерживаемая версия формата файла резервной копии",
		"backup.file_invalid":           "содержимое файла резервной копии не прошло проверку",
		"backup.confirm_host_mismatch":  "введённый адрес не совпадает с адресом этого сервера",
		"backup.digest_mismatch":        "файл, параметры или план восстановления отличаются от предпросмотра (состояние сервера изменилось) — проверьте файл заново",
		"backup.import_blocked":         "в плане восстановления есть блокирующие проблемы — проверьте файл заново",
		"backup.clickhouse_failed":      "не удалось создать базы или таблицы ClickHouse; PostgreSQL не изменён, восстановление можно повторить",
		// logs export (§115)
		"logs_export.busy":                 "на этом сервере уже идут другие выгрузки логов — повторите через полминуты",
		"logs_export.timeout":              "ClickHouse не успел отобрать записи — задайте период или сузьте поиск",
		"logs_export.sheet_logs":           "Логи",
		"logs_export.sheet_params":         "Параметры",
		"logs_export.param":                "Параметр",
		"logs_export.value":                "Значение",
		"logs_export.cell_truncated":       "…[обрезано, всего символов: %d]",
		"logs_export.status.complete":      "полная",
		"logs_export.status.limited_rows":  "ограничена строками: подходящих записей больше, чем выгружено",
		"logs_export.status.limited_bytes": "ограничена объёмом: достигнут предел размера выгрузки",
		"logs_export.status.incomplete":    "НЕПОЛНАЯ: ClickHouse отказал посреди выгрузки",
		"logs_export.status.aborted":       "прервана",
		"logs_export.reason.timeout":       "таймаут запроса",
		"logs_export.reason.unavailable":   "ClickHouse недоступен",
		"logs_export.reason.query_failed":  "ошибка запроса, см. журнал сервиса Web",
		"logs_export.bodies_on":            "выгружены, до %d символов в ячейке",
		"logs_export.bodies_off":           "не выгружались",
		"logs_export.unbounded":            "не задан",
		"logs_export.yes":                  "да",
		"logs_export.no":                   "нет",
		"logs_export.state.all":            "все",
		"logs_export.state.ok":             "OK",
		"logs_export.state.err":            "ошибки",
		"logs_export.done.all":             "любой",
		"logs_export.done.yes":             "завершён (done = 1)",
		"logs_export.done.no":              "не завершён (done = 0)",
		"logs_export.p.status":             "Полнота",
		"logs_export.p.written":            "Выгружено записей",
		"logs_export.p.selected":           "Отобрано записей",
		"logs_export.p.more":               "Подходящих записей больше предела строк",
		"logs_export.p.bytes":              "Текст ячеек, байт",
		"logs_export.p.bodies":             "Тела запроса и ответа",
		"logs_export.p.truncated_bodies":   "Обрезано тел",
		"logs_export.p.truncated_cells":    "Ячеек обрезано по пределу Excel",
		"logs_export.p.vanished":           "Записей удалено во время выгрузки",
		"logs_export.p.max_rows":           "Предел строк",
		"logs_export.p.max_bytes":          "Предел объёма, байт",
		"logs_export.p.node":               "Узел",
		"logs_export.p.table":              "Таблица ClickHouse",
		"logs_export.p.from":               "Период с",
		"logs_export.p.to":                 "Период по",
		"logs_export.p.search":             "Поиск",
		"logs_export.p.state":              "Состояние",
		"logs_export.p.done":               "Прогон доставки",
		"logs_export.p.method":             "Method",
		"logs_export.p.client":             "Клиент",
		"logs_export.p.user":               "Выгрузил",
		"logs_export.p.started":            "Начало",
		"logs_export.p.finished":           "Окончание",
		"logs_export.p.tz":                 "Часовой пояс дат",
		"logs_export.p.version":            "Версия Nexus",
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
		"ch_sync.foreign_database":                    "синхронизация схемы недоступна: база ClickHouse принадлежит другой ноде Nexus",
		"queue.foreign_database":                      "очистка недоступна: таблица ClickHouse принадлежит другой ноде Nexus или постороннему писателю — Nexus только читает её",
		"breaker.unavailable":                         "состояние защиты узла недоступно: Redis не сконфигурирован или не отвечает",
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
		"node.validation.group_id":                    "выбранной группы не существует — выберите другую",
		"node.validation.external_table_conflict":     "внешняя таблица несовместима с шаблоном CH-таблицы — шаблон означает, что таблицей управляет Nexus",
		"node.validation.logs_not_configured":         "логирование включено, но не задано имя таблицы ClickHouse — выберите шаблон или укажите имя таблицы",
		"node.validation.clickhouse_table_format":     "имя таблицы ClickHouse должно быть в формате db.table из латинских букв, цифр и подчёркивания (напр. nexus_default.my_table)",
		"node.validation.clickhouse_table_foreign":    "эта база ClickHouse принадлежит другой ноде Nexus; укажите таблицу своей ноды или включите режим внешней таблицы",
		"node.validation.max_body_size_range":         "максимальный размер тела должен быть от 0 до 10000000 байт",
		"node.validation.max_body_size_required":      "максимальный размер тела должен быть больше 0, когда лимит включён",
		"node.validation.async_ack_template":          "шаблон тела ответа неверен: проверьте подстановки ${ ... } и то, что результат — валидный JSON",
		"node.validation.async_ack_spec":              "настройки ответа неверны: допустимы форматы application/json и text/plain, код ответа — только 2xx",
		"node.validation.rmq_host":                    "хост RabbitMQ обязателен (1..253 символа)",
		"node.validation.rmq_queue":                   "имя очереди RabbitMQ — 1..255 символов, только буквы, цифры, точка, дефис и подчёркивание",
		"node.validation.pull_interval_sec":           "интервал опроса должен быть от 1 до 3600 секунд",
		"node.validation.pull_batch_size":             "размер батча должен быть от 1 до 1000",
		"node.validation.pull_prefetch":               "prefetch должен быть от 1 до 1000",
	},
}
