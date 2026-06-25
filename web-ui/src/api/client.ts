import axios from "axios";

import { queryClient } from "../lib/queryClient";

// Глобальный axios-клиент: одноимённый origin (см. §17.1 — Web Service отдаёт
// и API, и SPA из одного бинаря), куки автоматически прикрепляются.
const axiosInstance = axios.create({
  withCredentials: true,
  timeout: 30_000,
  headers: { "Content-Type": "application/json" },
});

// На 401 редиректим на /login глобально (см. §17.6 ТЗ). Перед редиректом
// чистим react-query-кеш: следующий логин (возможно, другим пользователем
// или с другой текущей командой) не должен видеть прежние данные (Phase AUD.6).
axiosInstance.interceptors.response.use(
  (r) => r,
  (err) => {
    if (err?.response?.status === 401 && location.pathname !== "/login") {
      queryClient.clear();
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
  // §39: path-passthrough — приклеивать хвост входящего пути к target URL.
  path_passthrough?: boolean;
  status: "enabled" | "disabled" | "paused";
  auth_type: string;
  // §41: динамическая авторизация (исходящая token/basic_from_request).
  auth_dynamic_source?: string;
  auth_dynamic_field?: string;
  auth_dynamic_strip_prefix?: string;
  // §41: динамическая авторизация на входе (источник+поле для token/basic).
  incoming_auth_dynamic_source?: string;
  incoming_auth_dynamic_field?: string;
  clickhouse_table: string;
  clickhouse_template_id: string;
  forward_headers: string[];
  log_request_body: boolean;
  log_response_body: boolean;
  log_headers: boolean;
  logging_enabled: boolean;
  max_body_size_enabled: boolean;
  max_body_size: number;
  // §36: авто-репроцессор DLQ — TTL повтора и минимальная задержка между повторами (сек).
  dlq_ttl_seconds?: number;
  dlq_retry_delay_seconds?: number;
  // §29: произвольный комментарий-описание узла.
  comment?: string;
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

// §41: справочник полей запроса (см. /api/request-fields).
export type RequestFieldCatalogEntry = {
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
  // §41 («Down»): последний исходящий вызов узла завершился ошибкой.
  last_error: boolean;
};
// §43.A: totals = СУММА строк items за выбранный период (CH, уникальные
// запросы). KPI шапки берёт incoming/outgoing/errors отсюда → шапка сходится
// с таблицей. error_rate = errors/incoming (0..1).
export type OverviewTotals = {
  incoming: number;
  outgoing: number;
  errors: number;
  error_rate: number;
};
export type NodesThroughputResp = {
  items: NodeThroughput[];
  totals: OverviewTotals;
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

// Kafka monitoring (§4 spec, /api/kafka/*). Admin-only.
export type KafkaSeverity = "ok" | "warn" | "err";
export type KafkaOverview = {
  period: { from: string; to: string };
  summary: {
    produced_total: number;
    consumed_total: number;
    failed_produced: number;
    failed_consumed: number;
    current_lag: number;
    in_flight_now: number;
  };
  delta_vs_previous_period: { produced: number; consumed: number; has_delta: boolean };
  error_rate: number;
  broker_health: {
    brokers_total: number;
    brokers_online: number;
    under_replicated_partitions: number;
    offline_partitions: number;
  };
  health: { severity: KafkaSeverity; reason: string };
  prometheus_available: boolean;
  kafka_available: boolean;
};
export type KafkaPoint = { t: number; v: number };
export type KafkaTimeseries = {
  step_seconds: number;
  series: Record<string, KafkaPoint[]>;
  prometheus_available: boolean;
};
export type KafkaTopicGroup = { group: string; lag_total: number; members: number };
export type KafkaTopic = {
  name: string;
  partitions: number;
  replication_factor: number;
  size_bytes: number;
  messages_estimate: number;
  retention_ms: number;
  consumer_groups: KafkaTopicGroup[];
  under_replicated: number;
  offline_partitions: number;
};
export type KafkaTopicsResp = { topics: KafkaTopic[]; kafka_available: boolean };
export type KafkaByNode = {
  top_producers: { node_path: string; produced: number; share: number }[];
  top_failures: { node_path: string; failed: number; rate: number }[];
  prometheus_available: boolean;
};
export type KafkaBrokerPing = { addr: string; elapsed_ms: number; ok: boolean; warn?: string };
export type KafkaTestResp = { ok: boolean; brokers: KafkaBrokerPing[]; kafka_available: boolean };
