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
  async del(url: string): Promise<void> {
    await axiosInstance.delete(url);
  },
};

export type Node = {
  id: string;
  path: string;
  root_method: "request" | "requestAsync";
  url_mode: "static" | "from_request";
  target_url: string;
  status: "enabled" | "disabled" | "paused";
  auth_type: string;
  clickhouse_table: string;
  clickhouse_template_id: string;
  created_at: string;
  updated_at: string;
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
