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
  timeout_ms: number;
  retry_count: number;
  retry_backoff_ms: number;
  dlq_ttl_seconds: number;
  max_body_size_enabled: boolean;
  max_body_size: number;
  rmq_host: string;
  rmq_queue: string;
  pull_interval_sec: number;
  pull_batch_size: number;
  pull_prefetch: number;
};

export type NodeFieldError = { field: string; code: string };

const PATH_RE = /^[a-zA-Z0-9][a-zA-Z0-9/_-]*$/;
const PARAM_NAME_RE = /^[a-zA-Z][a-zA-Z0-9_-]*$/;

export function validateNodeForm(f: NodeFormLimits): NodeFieldError | null {
  if (f.path.length < 1 || f.path.length > 255) {
    return { field: "path", code: "node.validation.path_length" };
  }
  if (!PATH_RE.test(f.path)) {
    return { field: "path", code: "node.validation.path_format" };
  }
  const isPull = f.root_method === "RabbitMQAsync";
  if ((f.url_mode === "static" || isPull) && f.target_url.trim() === "") {
    return { field: "target_url", code: "node.validation.target_url_required" };
  }
  if (f.target_url.length > 2048) {
    return { field: "target_url", code: "node.validation.target_url_length" };
  }
  if (f.url_mode === "from_request" && !isPull) {
    if (f.url_param_name.length < 1 || f.url_param_name.length > 64) {
      return { field: "url_param_name", code: "node.validation.url_param_name_length" };
    }
    if (!PARAM_NAME_RE.test(f.url_param_name)) {
      return { field: "url_param_name", code: "node.validation.url_param_name_format" };
    }
  }
  if (f.timeout_ms < 100 || f.timeout_ms > 300000) {
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
  if (f.max_body_size_enabled && f.max_body_size <= 0) {
    return { field: "max_body_size", code: "node.validation.max_body_size_required" };
  }
  if (f.max_body_size < 0 || f.max_body_size > 10_000_000) {
    return { field: "max_body_size", code: "node.validation.max_body_size_range" };
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
