import { useQuery } from "@tanstack/react-query";

import { api, type NodeMetricsResp } from "../../api/client";
import { type Period, periodParams, periodKey } from "../../lib/period";
import { useStableData } from "../../lib/useStableData";

// METRICS_REFETCH_MS — единый интервал авто-обновления метрик (§28, Пункт 2):
// все виджеты/графики метрик обновляются онлайн без перезагрузки страницы.
export const METRICS_REFETCH_MS = 12_000;

// useNodeMetrics — KPI + ряд графика узла из /api/metrics/nodes/:id (§21/§28).
// Анти-мерцание через useStableData (держим последний chart_available=true).
export function useNodeMetrics(id: string | undefined, period: Period) {
  const q = useQuery({
    queryKey: ["node-metrics", id, periodKey(period)],
    queryFn: () => api.get<NodeMetricsResp>(`/api/metrics/nodes/${id}`, periodParams(period)),
    enabled: !!id,
    refetchInterval: METRICS_REFETCH_MS,
  });
  const data = useStableData(q.data, `${id ?? ""}:${periodKey(period)}`, (d) => d.chart_available);
  return { ...q, data };
}
