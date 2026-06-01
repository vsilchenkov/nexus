import axios from "axios";

// Глобальный axios-клиент: одноимённый origin (см. §17.1 — Web Service отдаёт
// и API, и SPA из одного бинаря), куки автоматически прикрепляются.
const axiosInstance = axios.create({
  withCredentials: true,
  timeout: 30_000,
  headers: { "Content-Type": "application/json" },
});

// На 401 редиректим на /login глобально (см. §17.6 ТЗ).
axiosInstance.interceptors.response.use(
  (r) => r,
  (err) => {
    if (err?.response?.status === 401 && location.pathname !== "/login") {
      location.href = "/login";
    }
    return Promise.reject(err);
  },
);

export const api = {
  async get<T>(url: string, params?: Record<string, unknown>): Promise<T> {
    const r = await axiosInstance.get<T>(url, { params });
    return r.data;
  },
  async post<T>(url: string, body?: unknown): Promise<T> {
    const r = await axiosInstance.post<T>(url, body);
    return r.data;
  },
  async put<T>(url: string, body?: unknown): Promise<T> {
    const r = await axiosInstance.put<T>(url, body);
    return r.data;
  },
  async patch<T>(url: string, body?: unknown): Promise<T> {
    const r = await axiosInstance.patch<T>(url, body);
    return r.data;
  },
  async del(url: string): Promise<void> {
    await axiosInstance.delete(url);
  },
};

export type RootMethod = "request" | "requestAsync" | "RabbitMQAsync";
export type HTTPMethod = "GET" | "POST" | "PUT" | "DELETE";

// §27: runtime-health Puller-воркера узла RabbitMQAsync (только для него).
export type RMQStatus = {
  degraded: boolean;
  reason?: string;
  connection_state: "down" | "connecting" | "up";
  queue_depth: number;
  consumer_count: number;
  attempts: number;
  since?: string;
};

export type Node = {
  id: string;
  path: string;
  root_method: RootMethod;
  incoming_method?: HTTPMethod;
  outgoing_method?: HTTPMethod;
  url_mode: "static" | "from_request";
  target_url: string;
  status: "enabled" | "disabled" | "paused";
  auth_type: string;
  clickhouse_table: string;
  clickhouse_template_id: string;
  forward_headers: string[];
  log_request_body: boolean;
  log_response_body: boolean;
  log_headers: boolean;
  logging_enabled: boolean;
  max_body_size_enabled: boolean;
  max_body_size: number;
  // §27: RabbitMQAsync (пустые/нулевые для request/requestAsync).
  rmq_host?: string;
  rmq_port?: number;
  rmq_vhost?: string;
  rmq_user?: string;
  rmq_password_set?: boolean;
  rmq_queue?: string;
  rmq_use_tls?: boolean;
  pull_interval_sec?: number;
  pull_batch_size?: number;
  pull_prefetch?: number;
  rmq_status?: RMQStatus;
  created_at: string;
  updated_at: string;
};

// §27: ответ POST /api/nodes/test-rmq.
export type RMQTestCheck = {
  ok: boolean;
  elapsed_ms: number;
  error?: string;
  message_count?: number;
  consumer_count?: number;
};
export type RMQTestResult = {
  ok: boolean;
  checks: { connect: RMQTestCheck; auth: RMQTestCheck; queue: RMQTestCheck };
};

// §23: каталог разрешённых хостов (см. /api/allowed-hosts/*).
export type HostKind = "exact" | "wildcard" | "regex";
export type HostAllowlistEntry = {
  id: string;
  pattern: string;
  kind: HostKind;
  description: string;
  usage_count: number;
  created_by: string;
  created_at: string;
  updated_at: string;
};

// §24: справочник заголовков (см. /api/headers).
export type HeaderCatalogEntry = {
  id: string;
  name: string;
  description: string;
  usage_count: number;
  created_by: string;
  created_at: string;
  updated_at: string;
};

// §21: метрики панели (см. /api/metrics/*).
export type OverviewKPI = {
  incoming_24h: number;
  outgoing_24h: number;
  kafka_queue: number;
  errors_24h: number;
  error_rate: number;
  prometheus_available: boolean;
};
export type NodeThroughput = {
  node: string;
  in: number;
  out: number;
  errors: number;
  p95_ms: number;
  spark: number[];
};
export type NodesThroughputResp = {
  items: NodeThroughput[];
  prometheus_available: boolean;
};
export type NodeMetricsResp = {
  kpi: {
    total: number;
    delivered: number;
    errors: number;
    p95_ms: number;
    p99_ms: number;
  };
  series: { ts: number; count: number; errors: number }[];
  chart_available: boolean;
  range_ms: number;
};

// §19: шаблоны DDL для таблиц логов ClickHouse.
export type CHColumnOverride = { name: string; codec: string };
export type CHTemplateIndex = { name: string; expr: string; type: string; granularity: number };
export type CHTemplateSpec = {
  engine: string;
  partition_by: string;
  order_by: string[];
  column_overrides?: CHColumnOverride[];
  indexes?: CHTemplateIndex[];
  ttl_mode: "none" | "ttl_days";
};
export type CHTemplate = {
  id: string;
  name: string;
  description: string;
  spec: CHTemplateSpec;
  is_default: boolean;
  created_at: string;
  updated_at: string;
};
