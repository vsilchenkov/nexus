// Клиентская валидация формы узла (Phase AUD.7): зеркалит лимиты backend'а
// (domain.Node.Validate, см. internal/domain/node.go) и переиспользует те же
// i18n-коды node.validation.* — пользователь видит ошибку сразу, без
// round-trip'а до сервера. Backend-валидация остаётся источником истины.

export type NodeFormLimits = {
  path: string;
  root_method: "request" | "requestAsync" | "RabbitMQAsync";
  url_mode: "static" | "from_request";
  target_url: string;
  url_param_name: string;
  // §41: динамическая авторизация — обязательность выбора поля.
  auth_type: string;
  auth_dynamic_field: string;
  incoming_auth_type: string;
  incoming_auth_dynamic_source: string;
  incoming_auth_dynamic_field: string;
  // Раздельные Логин/Пароль basic-кредов (склеиваются в "login:password").
  auth_login: string;
  auth_password: string;
  incoming_auth_login: string;
  incoming_auth_password: string;
  timeout_ms: number;
  retry_count: number;
  retry_backoff_ms: number;
  dlq_ttl_seconds: number;
  dlq_retry_delay_seconds: number;
  circuit_breaker_threshold: number;
  circuit_breaker_cooldown_sec: number;
  max_body_size_enabled: boolean;
  max_body_size: number;
  // Гейт проверок карточки «Логирование»: при выключенном тумблере её поля
  // задизейблены — блокирующая ошибка по ним была бы ловушкой (см. Validate).
  logging_enabled: boolean;
  // §42: имя CH-таблицы (опционально) — формат db.table из [A-Za-z0-9_].
  clickhouse_table: string;
  // §64: внешняя таблица несовместима с шаблоном (шаблон = «управляет Nexus»).
  clickhouse_template_id: string;
  external_table: boolean;
  rmq_host: string;
  rmq_queue: string;
  pull_interval_sec: number;
  pull_batch_size: number;
  pull_prefetch: number;
};

export type NodeFieldError = { field: string; code: string };

// BasicAuthContext — оригинальные логины сохранённого узла (пустые для нового):
// правило «сменил логин — введи пароль заново». Сервер пароль не отдаёт и не
// может пересобрать "login:password" из одного нового логина.
export type BasicAuthContext = {
  orig_auth_login: string;
  orig_incoming_auth_login: string;
};

const PATH_RE = /^[a-zA-Z0-9][a-zA-Z0-9/_-]*$/;
const PARAM_NAME_RE = /^[a-zA-Z][a-zA-Z0-9_-]*$/;
// §42: db.table из [A-Za-z0-9_], ровно одна точка (зеркало isValidCHTableName).
const CH_TABLE_RE = /^[A-Za-z0-9_]+\.[A-Za-z0-9_]+$/;
// §69.2: абсолютный http(s)-адрес с непустым хостом (зеркало
// domain.AbsoluteHTTPURL: схема + host обязательны, «example.com/hook» — нет).
const TARGET_URL_RE = /^https?:\/\/[^/?#\s]+/i;

// validateNodePath — отдельная проверка path (§53: диалог «Скопировать узел»,
// где валидируется только новый путь). Возвращает i18n-код или null.
export function validateNodePath(path: string): string | null {
  if (path.length < 1 || path.length > 255) {
    return "node.validation.path_length";
  }
  if (!PATH_RE.test(path)) {
    return "node.validation.path_format";
  }
  return null;
}

// basicCredsError — общие проверки пары Логин/Пароль basic-авторизации
// (исходящей и входящей): формат логина, пробелы по краям, «пароль без
// логина», «сменил логин — введи пароль заново» (origLogin передаётся только
// для сохранённого узла).
function basicCredsError(
  authType: string,
  login: string,
  password: string,
  loginField: string,
  passwordField: string,
  origLogin?: string,
): NodeFieldError | null {
  if (authType !== "basic") {
    return null;
  }
  if (login.includes(":")) {
    return { field: loginField, code: "node.validation.login_colon" };
  }
  if (login !== login.trim()) {
    return { field: loginField, code: "node.validation.login_whitespace" };
  }
  if (password !== "" && password !== password.trim()) {
    return { field: passwordField, code: "node.validation.password_whitespace" };
  }
  if (password !== "" && login === "") {
    return { field: loginField, code: "node.validation.login_required_with_password" };
  }
  if (origLogin !== undefined && password === "" && login !== origLogin) {
    return { field: passwordField, code: "node.validation.password_required_on_login_change" };
  }
  return null;
}

export function validateNodeForm(f: NodeFormLimits, ctx?: BasicAuthContext): NodeFieldError | null {
  const pathCode = validateNodePath(f.path);
  if (pathCode) {
    return { field: "path", code: pathCode };
  }
  // Basic-креды: логин без «:» (разделитель формата "login:password" — пароль
  // двоеточия содержать может, логин нет); смена логина требует пароля заново.
  // Пробелы по краям логина/пароля — блокируем: сервер хранит байт-в-байт и
  // сравнивает без нормализации, невидимый хвостовой пробел из copy-paste =
  // «правильный пароль не подходит». Пароль, введённый без логина, тоже
  // блокируем: креды склеились бы в ":password" и логин молча терялся.
  const basicErr = basicCredsError(
    f.auth_type,
    f.auth_login,
    f.auth_password,
    "auth_login",
    "auth_password",
    ctx?.orig_auth_login,
  );
  if (basicErr) {
    return basicErr;
  }
  const inBasicErr = basicCredsError(
    f.incoming_auth_type,
    f.incoming_auth_login,
    f.incoming_auth_password,
    "incoming_auth_login",
    "incoming_auth_password",
    ctx?.orig_incoming_auth_login,
  );
  if (inBasicErr) {
    return inBasicErr;
  }
  const isPull = f.root_method === "RabbitMQAsync";
  if ((f.url_mode === "static" || isPull) && f.target_url.trim() === "") {
    return { field: "target_url", code: "node.validation.target_url_required" };
  }
  if (f.target_url.length > 2048) {
    return { field: "target_url", code: "node.validation.target_url_length" };
  }
  // §69.2: в static/pull адрес доставки обязан быть абсолютным http(s). Без
  // схемы узел сохранялся, а падал уже на доставке («unsupported protocol
  // scheme»). При from_request поле скрыто и не используется — остаточное
  // значение не блокирует сохранение (зеркало domain.Node.Validate).
  if (
    (f.url_mode === "static" || isPull) &&
    f.target_url.trim() !== "" &&
    !TARGET_URL_RE.test(f.target_url.trim())
  ) {
    return { field: "target_url", code: "node.validation.target_url_scheme" };
  }
  if (f.url_mode === "from_request" && !isPull) {
    if (f.url_param_name.length < 1 || f.url_param_name.length > 64) {
      return { field: "url_param_name", code: "node.validation.url_param_name_length" };
    }
    if (!PARAM_NAME_RE.test(f.url_param_name)) {
      return { field: "url_param_name", code: "node.validation.url_param_name_format" };
    }
  }
  // §41: исходящие token_from_request / basic_from_request требуют выбранного
  // поля. Для входящих token/basic поле обязательно только при source=query
  // (для source=header дефолт Authorization подставляется на бэкенде).
  if (
    (f.auth_type === "token_from_request" || f.auth_type === "basic_from_request") &&
    f.auth_dynamic_field.trim() === ""
  ) {
    return { field: "auth_dynamic_field", code: "node.validation.auth_field_required" };
  }
  if (
    (f.incoming_auth_type === "basic" || f.incoming_auth_type === "token") &&
    f.incoming_auth_dynamic_source === "query" &&
    f.incoming_auth_dynamic_field.trim() === ""
  ) {
    return { field: "incoming_auth_dynamic_field", code: "node.validation.auth_field_required" };
  }
  if (f.timeout_ms < 100 || f.timeout_ms > 600000) {
    return { field: "timeout_ms", code: "node.validation.timeout_ms" };
  }
  if (f.retry_count < 0 || f.retry_count > 10) {
    return { field: "retry_count", code: "node.validation.retry_count" };
  }
  if (f.retry_backoff_ms < 0 || f.retry_backoff_ms > 60000) {
    return { field: "retry_backoff_ms", code: "node.validation.retry_backoff_ms" };
  }
  if (f.dlq_ttl_seconds < 60 || f.dlq_ttl_seconds > 2592000) {
    return { field: "dlq_ttl_seconds", code: "node.validation.dlq_ttl_seconds" };
  }
  if (f.dlq_retry_delay_seconds < 1 || f.dlq_retry_delay_seconds > 86400) {
    return { field: "dlq_retry_delay_seconds", code: "node.validation.dlq_retry_delay_seconds" };
  }
  // §81.3: 0 = «как в конфигурации», поэтому проверяются только заданные
  // значения. Границы — зеркало domain.Node.Validate.
  if (f.circuit_breaker_threshold < 0 || f.circuit_breaker_threshold > 100) {
    return { field: "circuit_breaker_threshold", code: "node.validation.circuit_breaker_threshold" };
  }
  if (f.circuit_breaker_cooldown_sec < 0 || f.circuit_breaker_cooldown_sec > 3600) {
    return { field: "circuit_breaker_cooldown_sec", code: "node.validation.circuit_breaker_cooldown_sec" };
  }
  // Поля карточки «Логирование» проверяются только при включённом тумблере
  // (зеркало domain.Node.Validate): при выключенном они задизейблены, и
  // недозаполненное имя таблицы (дефолтный префикс «nexus_x.») — черновик,
  // а не ошибка.
  if (f.logging_enabled && f.max_body_size_enabled && f.max_body_size <= 0) {
    return { field: "max_body_size", code: "node.validation.max_body_size_required" };
  }
  if (f.max_body_size < 0 || f.max_body_size > 10_000_000) {
    return { field: "max_body_size", code: "node.validation.max_body_size_range" };
  }
  // §42: имя CH-таблицы опционально, но если задано — строго db.table.
  if (
    f.logging_enabled &&
    f.clickhouse_table.trim() !== "" &&
    !CH_TABLE_RE.test(f.clickhouse_table.trim())
  ) {
    return { field: "clickhouse_table", code: "node.validation.clickhouse_table_format" };
  }
  // §64: шаблон означает «таблицей управляет Nexus», external_table — обратное.
  if (f.external_table && f.clickhouse_template_id !== "") {
    return { field: "clickhouse_template_id", code: "node.validation.external_table_conflict" };
  }
  if (isPull) {
    if (f.rmq_host.trim() === "" || f.rmq_host.length > 253) {
      return { field: "rmq_host", code: "node.validation.rmq_host" };
    }
    if (f.rmq_queue.trim() === "" || f.rmq_queue.length > 255) {
      return { field: "rmq_queue", code: "node.validation.rmq_queue" };
    }
    if (f.pull_interval_sec < 1 || f.pull_interval_sec > 3600) {
      return { field: "pull_interval_sec", code: "node.validation.pull_interval_sec" };
    }
    if (f.pull_batch_size < 1 || f.pull_batch_size > 1000) {
      return { field: "pull_batch_size", code: "node.validation.pull_batch_size" };
    }
    if (f.pull_prefetch < 1 || f.pull_prefetch > 1000) {
      return { field: "pull_prefetch", code: "node.validation.pull_prefetch" };
    }
  }
  return null;
}
