import { useQuery } from "@tanstack/react-query";

import { api, type NodeMetricsResp } from "../../api/client";
import { type Period, periodParams, periodKey } from "../../lib/period";
import { useStableData } from "../../lib/useStableData";

// METRICS_REFETCH_MS — фолбэк интервала авто-обновления метрик (§28, Пункт 2),
// если сервер не вернул значение. Реальный интервал настраивается оператором
// (§43.C, app_settings.general.metrics_refetch_ms) и отдаётся в
// /api/settings/public — см. useMetricsRefetchMs.
export const METRICS_REFETCH_MS = 12_000;

// useMetricsRefetchMs — настраиваемый интервал автообновления метрик из
// public-настроек (§43.C). Кешируется (staleTime 5м) и переиспользует тот же
// query, что и useNodeUrlBuilder. Фолбэк — METRICS_REFETCH_MS.
export function useMetricsRefetchMs(): number {
  const q = useQuery({
    queryKey: ["public-settings"],
    queryFn: () => api.get<{ metrics_refetch_ms?: number }>("/api/settings/public"),
    staleTime: 5 * 60_000,
  });
  return q.data?.metrics_refetch_ms ?? METRICS_REFETCH_MS;
}

// useNodeMetrics — KPI + ряд графика узла из /api/metrics/nodes/:id (§21/§28).
// Анти-мерцание через useStableData (держим последний chart_available=true).
export function useNodeMetrics(id: string | undefined, period: Period) {
  const refetch = useMetricsRefetchMs();
  const q = useQuery({
    queryKey: ["node-metrics", id, periodKey(period)],
    queryFn: () => api.get<NodeMetricsResp>(`/api/metrics/nodes/${id}`, periodParams(period)),
    enabled: !!id,
    refetchInterval: refetch,
  });
  const data = useStableData(q.data, `${id ?? ""}:${periodKey(period)}`, (d) => d.chart_available);
  return { ...q, data };
}
